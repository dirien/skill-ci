package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/metrics"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/semver"
)

// Condition types set on SkillEvals.
const (
	// ConditionPolicy records what the tag policy decided.
	ConditionPolicy = "PolicyApplied"
	// ConditionRegressed records that the version scores worse than the stable one.
	ConditionRegressed = "Regressed"
	// ConditionRejected records that Tier 1 or Tier 2 failed for the run.
	ConditionRejected = "Rejected"
	// ConditionInconclusive records a drop below the stable version that the
	// trials do not show beyond noise.
	ConditionInconclusive = "Inconclusive"
)

// ReasonWorseThanStable is the reason of the Regressed condition and its event.
const ReasonWorseThanStable = "WorseThanStable"

// ReasonNotEnoughEvidence is the reason of the Inconclusive condition and its event.
const ReasonNotEnoughEvidence = "NotEnoughEvidence"

// Reasons of the Rejected condition and its event.
const (
	// ReasonTier1Failed means Tier 1 (static, security, quality) failed.
	ReasonTier1Failed = "Tier1Failed"
	// ReasonTier2Failed means Tier 2 (deduplication) failed.
	ReasonTier2Failed = "Tier2Failed"
)

// regression says which models of a candidate scored lower than the stable
// version, and which of those drops hold beyond noise.
type regression struct {
	regs []policy.Regression
	// regressed and inconclusive are the condition messages; empty when there is no such drop.
	regressed, inconclusive string
}

// has reports whether the model (agent/model key) regressed beyond noise. It is safe on nil.
func (g *regression) has(model string) bool { return g.hasKind(model, false) }

// unsure reports whether the model dropped, but not beyond noise. It is safe on nil.
func (g *regression) unsure(model string) bool { return g.hasKind(model, true) }

func (g *regression) hasKind(model string, inconclusive bool) bool {
	if g == nil {
		return false
	}
	return slices.ContainsFunc(g.regs, func(r policy.Regression) bool { return r.Model == model && r.Inconclusive == inconclusive })
}

// confirmed and doubtful say whether any model regressed beyond noise, or only maybe. Both are safe on nil.
func (g *regression) confirmed() bool { return g != nil && g.regressed != "" }
func (g *regression) doubtful() bool  { return g != nil && g.inconclusive != "" }

// applyPolicy looks at every SkillEval of the digest, and once all required
// models have a result it moves the beta and stable tags in Harbor.
func (r *SkillEvalReconciler) applyPolicy(ctx context.Context, se *v1alpha1.SkillEval) error {
	log := logf.FromContext(ctx)
	var list v1alpha1.SkillEvalList
	err := r.Reader.List(ctx, &list, client.InNamespace(se.Namespace),
		client.MatchingLabels{v1alpha1.LabelSkill: se.Labels[v1alpha1.LabelSkill]})
	if err != nil {
		return fmt.Errorf("listing skillevals of %s: %w", se.Spec.Skill, err)
	}

	byDigest := map[string][]*v1alpha1.SkillEval{}
	for i := range list.Items {
		e := &list.Items[i]
		byDigest[e.Spec.Digest] = append(byDigest[e.Spec.Digest], e)
	}
	outcomes := map[string]policy.Outcome{}
	newest := "" // digest of the newest fully evaluated version
	for digest, group := range byDigest {
		outcomes[digest] = r.Config.Thresholds.Evaluate(results(group, r.Config.Thresholds))
		if outcomes[digest].Complete && !outcomes[digest].Rejected && (newest == "" || semver.Compare(group[0].Spec.Version, byDigest[newest][0].Spec.Version) > 0) {
			newest = digest
		}
	}

	digest := se.Spec.Digest
	out := outcomes[digest]
	if !out.Complete {
		return nil
	}
	arts, err := r.Harbor.ListArtifacts(ctx, se.Spec.Skill)
	if err != nil {
		return fmt.Errorf("listing artifacts of %s: %w", se.Spec.Skill, err)
	}
	if !hasDigest(arts, digest) {
		log.Info("digest is gone from the registry, skipping policy", "skill", se.Spec.Skill, "digest", digest)
		return nil
	}

	reg := regressionOf(r.Config.Thresholds, byDigest, arts, digest)
	if reg != nil {
		out = out.Block(reg.regs)
	} else if out.Promote && yieldsToStable(r.Config.Thresholds, byDigest, arts, digest) {
		out.Promote = false
	}
	ops := policy.Plan(out, digest, digest == newest, arts)
	for _, op := range ops {
		if err := r.applyOp(ctx, se.Spec.Skill, op); err != nil {
			return err
		}
	}
	if len(ops) > 0 {
		if arts, err = r.Harbor.ListArtifacts(ctx, se.Spec.Skill); err != nil {
			return fmt.Errorf("listing artifacts of %s: %w", se.Spec.Skill, err)
		}
	}
	for d, group := range byDigest {
		metrics.SetStable(se.Spec.Skill, group[0].Spec.Version, harbor.Tagged(arts, d, policy.TagStable))
		metrics.SetObsolete(se.Spec.Skill, group[0].Spec.Version, outcomes[d].Obsolete)
		metrics.SetRejected(se.Spec.Skill, group[0].Spec.Version, outcomes[d].Rejected)
	}
	metrics.SetRegressed(se.Spec.Skill, se.Spec.Version, reg.confirmed())
	metrics.SetInconclusive(se.Spec.Skill, se.Spec.Version, reg.doubtful() && !reg.confirmed())
	return r.record(ctx, byDigest[digest], out, ops, reg)
}

// regressionOf compares the digest with the current stable digest of the skill.
// It returns nil when there is no other stable version or nothing got worse.
func regressionOf(th policy.Thresholds, byDigest map[string][]*v1alpha1.SkillEval, arts []harbor.Artifact, digest string) *regression {
	for _, a := range arts {
		stable := byDigest[a.Digest]
		if a.Digest == digest || !a.HasTag(policy.TagStable) || len(stable) == 0 {
			continue
		}
		regs := th.Regressions(withScores(byDigest[digest]), withScores(stable))
		if len(regs) == 0 {
			return nil
		}
		g := &regression{regs: regs}
		cand, base := byDigest[digest][0].Spec.Version, stable[0].Spec.Version
		var hard, soft []string
		for _, d := range regs {
			if d.Inconclusive {
				soft = append(soft, inconclusiveText(d, cand, base, th))
			} else {
				hard = append(hard, regressedText(d, cand, base, th))
			}
		}
		if len(hard) > 0 {
			g.regressed = strings.Join(hard, "; ") + fmt.Sprintf(" (tolerance %.2f%s)", th.Regression, fixedRuleNote(regs))
		}
		g.inconclusive = strings.Join(soft, "; ")
		return g
	}
	return nil
}

// regressedText describes one confirmed drop: scores, and the interval when there is one.
func regressedText(d policy.Regression, candidate, stable string, th policy.Thresholds) string {
	text := fmt.Sprintf("%s scores %.2f with %s, stable %s scored %.2f", d.Model, d.Candidate, candidate, stable, d.Stable)
	if d.CI != nil {
		text += fmt.Sprintf(": difference %+.2f, %s CI %s", d.Diff(), th.Percent(), formatCI(d.CI))
	}
	return text
}

// inconclusiveText describes one drop that may be noise.
func inconclusiveText(d policy.Regression, candidate, stable string, th policy.Thresholds) string {
	return fmt.Sprintf("%s: %.2f with %s vs %.2f with stable %s, difference %+.2f, %s CI %s; not enough evidence to call it a regression",
		d.Model, d.Candidate, candidate, d.Stable, stable, d.Diff(), th.Percent(), formatCI(d.CI))
}

// fixedRuleNote says that a drop was judged by the fixed tolerance alone.
func fixedRuleNote(regs []policy.Regression) string {
	if slices.ContainsFunc(regs, func(r policy.Regression) bool { return !r.Inconclusive && r.CI == nil }) {
		return "; fixed rule, no trial scores to test it"
	}
	return ""
}

// yieldsToStable reports whether the digest is older than the current stable
// digest and the stable one does not score worse than it. An older version
// then must not take stable over: two equally good versions would otherwise
// swap the tag every time one of them is reconciled. An older version still
// wins when the newer stable one regresses against it.
func yieldsToStable(th policy.Thresholds, byDigest map[string][]*v1alpha1.SkillEval, arts []harbor.Artifact, digest string) bool {
	for _, a := range arts {
		stable := byDigest[a.Digest]
		if a.Digest == digest || !a.HasTag(policy.TagStable) || len(stable) == 0 {
			continue
		}
		if semver.Compare(stable[0].Spec.Version, byDigest[digest][0].Spec.Version) <= 0 {
			return false
		}
		// Only a drop beyond noise lets the older version take stable back.
		return len(policy.Confirmed(th.Regressions(withScores(stable), withScores(byDigest[digest])))) == 0
	}
	return false
}

// withScores converts the finished SkillEvals of one digest into with-skill scores.
func withScores(group []*v1alpha1.SkillEval) []policy.Score {
	var out []policy.Score
	for _, e := range group {
		if e.Status.Phase != v1alpha1.PhaseSucceeded || rejectedRun(e) {
			continue
		}
		if with, _, _, err := scores(e); err == nil {
			with2, _ := trialScores(e)
			out = append(out, policy.Score{Model: e.Spec.Agent + "/" + e.Spec.Model, Required: e.Spec.Required, With: with, Samples: with2})
		}
	}
	return out
}

func (r *SkillEvalReconciler) applyOp(ctx context.Context, skill string, op policy.Op) error {
	verb, apply := "removing", r.Harbor.RemoveTag
	if op.Add {
		verb, apply = "adding", r.Harbor.AddTag
	}
	if err := apply(ctx, skill, op.Digest, op.Tag); err != nil {
		return fmt.Errorf("%s tag %s on %s@%s: %w", verb, op.Tag, skill, op.Digest, err)
	}
	logf.FromContext(ctx).Info("changed tag", "skill", skill, "digest", op.Digest, "tag", op.Tag, "add", op.Add)
	return nil
}

// record sets the PolicyApplied condition on every SkillEval of the digest and
// emits an event when something changed.
func (r *SkillEvalReconciler) record(ctx context.Context, group []*v1alpha1.SkillEval, out policy.Outcome, ops []policy.Op, reg *regression) error {
	reason, eventType := "Evaluated", corev1.EventTypeNormal
	switch {
	case out.Rejected:
		reason, eventType = "Rejected", corev1.EventTypeWarning
	case out.Demote:
		reason, eventType = "Demoted", corev1.EventTypeWarning
	case out.Promote:
		reason = "Promoted"
	case out.Obsolete:
		reason, eventType = "Obsolete", corev1.EventTypeWarning
	}
	msg := "no tag changes"
	if len(ops) > 0 {
		changes := make([]string, len(ops))
		for i, op := range ops {
			sign := "-"
			if op.Add {
				sign = "+"
			}
			changes[i] = sign + op.Tag
		}
		msg = "tag changes: " + strings.Join(changes, " ")
	}
	msg += evidenceNote(group, r.Config.Thresholds)
	for _, e := range group {
		changed := meta.SetStatusCondition(&e.Status.Conditions, metav1.Condition{
			Type: ConditionPolicy, Status: metav1.ConditionTrue, Reason: reason, Message: msg,
			ObservedGeneration: e.Generation,
		})
		regChanged, incChanged, verdictChanged := markRegression(e, reg, r.Config.Thresholds)
		rejReason, rejMsg, rejChanged := markRejected(e)
		if !changed && !regChanged && !incChanged && !verdictChanged && !rejChanged && len(ops) == 0 {
			continue
		}
		if err := r.Status().Update(ctx, e); err != nil {
			return fmt.Errorf("recording policy on %s: %w", e.Name, err)
		}
		if changed || len(ops) > 0 {
			r.Recorder.Eventf(e, nil, eventType, reason, "ApplyPolicy", "%s", msg)
		}
		if regChanged && reg.confirmed() {
			r.Recorder.Eventf(e, nil, corev1.EventTypeWarning, ReasonWorseThanStable, "ApplyPolicy", "%s", reg.regressed)
		}
		if incChanged && reg.doubtful() {
			r.Recorder.Eventf(e, nil, corev1.EventTypeNormal, ReasonNotEnoughEvidence, "ApplyPolicy", "%s", reg.inconclusive)
		}
		if rejChanged && rejReason != "" {
			r.Recorder.Eventf(e, nil, corev1.EventTypeWarning, rejReason, "ApplyPolicy", "%s", rejMsg)
		}
	}
	return nil
}

// evidenceNote explains, for the PolicyApplied message, where the gate had no
// evidence: a lift that reaches the promote threshold but not beyond noise, and
// runs that carry no trial scores and so use the fixed thresholds.
func evidenceNote(group []*v1alpha1.SkillEval, th policy.Thresholds) string {
	var unproven, fixed []string
	for _, e := range group {
		if !e.Spec.Required || e.Status.Phase != v1alpha1.PhaseSucceeded || rejectedRun(e) {
			continue
		}
		_, _, lift, err := scores(e)
		if err != nil {
			continue
		}
		model := e.Spec.Agent + "/" + e.Spec.Model
		switch ci := liftCI(e, th); {
		case ci == nil:
			fixed = append(fixed, model)
		case lift >= th.Promote-1e-9 && ci.Lower <= 0:
			unproven = append(unproven, fmt.Sprintf("%s (%+.2f, %s CI %s)", model, lift, th.Percent(), formatCI(ci)))
		}
	}
	var note string
	if len(unproven) > 0 {
		note += "; lift not beyond noise on " + strings.Join(unproven, ", ")
	}
	if len(fixed) > 0 {
		note += "; fixed thresholds, no trial scores from " + strings.Join(fixed, ", ")
	}
	return note
}

// markRegression sets or clears the Regressed and Inconclusive conditions and
// the verdict of one SkillEval. Without a drop the verdict goes back to the one
// the scores earn. It reports whether each condition and the verdict changed.
func markRegression(e *v1alpha1.SkillEval, reg *regression, th policy.Thresholds) (regressed, inconclusive, changed bool) {
	regressed = setCondition(e, ConditionRegressed, ReasonWorseThanStable, reg.confirmed(), func() string { return reg.regressed })
	inconclusive = setCondition(e, ConditionInconclusive, ReasonNotEnoughEvidence, reg.doubtful(), func() string { return reg.inconclusive })
	verdict := e.Status.Verdict
	if _, without, lift, err := scores(e); err == nil {
		verdict = th.Verdict(lift, without, liftCI(e, th))
	}
	model := e.Spec.Agent + "/" + e.Spec.Model
	switch {
	case reg.has(model):
		verdict = v1alpha1.VerdictRegressed
	case reg.unsure(model):
		verdict = v1alpha1.VerdictInconclusive
	}
	if rejectedRun(e) {
		verdict = v1alpha1.VerdictRejected
	}
	if verdict != e.Status.Verdict {
		e.Status.Verdict, changed = verdict, true
	}
	return regressed, inconclusive, changed
}

// setCondition sets the condition of the given type when on, removes it
// otherwise, and reports whether that changed it.
func setCondition(e *v1alpha1.SkillEval, typ, reason string, on bool, message func() string) bool {
	if !on {
		return meta.RemoveStatusCondition(&e.Status.Conditions, typ)
	}
	return meta.SetStatusCondition(&e.Status.Conditions, metav1.Condition{
		Type: typ, Status: metav1.ConditionTrue, Reason: reason, Message: message(), ObservedGeneration: e.Generation,
	})
}

// markRejected sets or clears the Rejected condition of one SkillEval. The
// message lists the findings. It returns the reason and message for the event
// and whether anything changed; the reason is empty when the run is not rejected.
func markRejected(e *v1alpha1.SkillEval) (reason, message string, changed bool) {
	if !rejectedRun(e) {
		return "", "", meta.RemoveStatusCondition(&e.Status.Conditions, ConditionRejected)
	}
	reason = ReasonTier2Failed
	if e.Status.Tiers.Tier1 == v1alpha1.TierFailed {
		reason = ReasonTier1Failed
	}
	message = strings.Join(e.Status.Findings, "; ")
	if message == "" {
		message = map[string]string{ReasonTier1Failed: "Tier 1 failed", ReasonTier2Failed: "Tier 2 failed"}[reason]
	}
	changed = meta.SetStatusCondition(&e.Status.Conditions, metav1.Condition{
		Type: ConditionRejected, Status: metav1.ConditionTrue, Reason: reason, Message: message,
		ObservedGeneration: e.Generation,
	})
	return reason, message, changed
}

// results converts the SkillEvals of one digest into policy input.
func results(group []*v1alpha1.SkillEval, th policy.Thresholds) []policy.Result {
	out := make([]policy.Result, 0, len(group))
	for _, e := range group {
		res := policy.Result{Required: e.Spec.Required, Succeeded: e.Status.Phase == v1alpha1.PhaseSucceeded}
		if res.Succeeded && rejectedRun(e) {
			// Finished without usable scores: Tier 3 did not run.
			res.Rejected, res.Verdict = true, v1alpha1.VerdictRejected
		} else if res.Succeeded {
			_, without, lift, err := scores(e)
			if err != nil { // unreadable scores count as not evaluated
				res.Succeeded = false
			}
			// The stored verdict may be Regressed; the no-skill bands decide here.
			res.Lift, res.LiftCI = lift, liftCI(e, th)
			res.Verdict = th.Verdict(lift, without, res.LiftCI)
		}
		out = append(out, res)
	}
	return out
}

func hasDigest(arts []harbor.Artifact, digest string) bool {
	for _, a := range arts {
		if a.Digest == digest {
			return true
		}
	}
	return false
}
