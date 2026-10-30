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
)

// ReasonWorseThanStable is the reason of the Regressed condition and its event.
const ReasonWorseThanStable = "WorseThanStable"

// regression says which models of a candidate scored worse than the stable version.
type regression struct {
	regs    []policy.Regression
	message string
}

// has reports whether the model (agent/model key) regressed. It is safe on nil.
func (g *regression) has(model string) bool {
	if g == nil {
		return false
	}
	return slices.ContainsFunc(g.regs, func(r policy.Regression) bool { return r.Model == model })
}

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
		if outcomes[digest].Complete && (newest == "" || semver.Compare(group[0].Spec.Version, byDigest[newest][0].Spec.Version) > 0) {
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
	}
	metrics.SetRegressed(se.Spec.Skill, se.Spec.Version, reg != nil)
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
		msgs := make([]string, len(regs))
		for i, g := range regs {
			msgs[i] = fmt.Sprintf("%s scores %.2f with %s, stable %s scored %.2f",
				g.Model, g.Candidate, byDigest[digest][0].Spec.Version, stable[0].Spec.Version, g.Stable)
		}
		return &regression{regs: regs, message: strings.Join(msgs, "; ") + fmt.Sprintf(" (tolerance %.2f)", th.Regression)}
	}
	return nil
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
		return len(th.Regressions(withScores(stable), withScores(byDigest[digest]))) == 0
	}
	return false
}

// withScores converts the finished SkillEvals of one digest into with-skill scores.
func withScores(group []*v1alpha1.SkillEval) []policy.Score {
	var out []policy.Score
	for _, e := range group {
		if e.Status.Phase != v1alpha1.PhaseSucceeded {
			continue
		}
		if with, _, _, err := scores(e); err == nil {
			out = append(out, policy.Score{Model: e.Spec.Agent + "/" + e.Spec.Model, Required: e.Spec.Required, With: with})
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
	for _, e := range group {
		changed := meta.SetStatusCondition(&e.Status.Conditions, metav1.Condition{
			Type: ConditionPolicy, Status: metav1.ConditionTrue, Reason: reason, Message: msg,
			ObservedGeneration: e.Generation,
		})
		regChanged := markRegression(e, reg, r.Config.Thresholds)
		if !changed && !regChanged && len(ops) == 0 {
			continue
		}
		if err := r.Status().Update(ctx, e); err != nil {
			return fmt.Errorf("recording policy on %s: %w", e.Name, err)
		}
		if changed || len(ops) > 0 {
			r.Recorder.Eventf(e, nil, eventType, reason, "ApplyPolicy", "%s", msg)
		}
		if regChanged && reg != nil {
			r.Recorder.Eventf(e, nil, corev1.EventTypeWarning, ReasonWorseThanStable, "ApplyPolicy", "%s", reg.message)
		}
	}
	return nil
}

// markRegression sets or clears the Regressed condition and the Regressed
// verdict of one SkillEval. Without a regression the verdict goes back to the
// one the scores earn. It reports whether anything changed.
func markRegression(e *v1alpha1.SkillEval, reg *regression, th policy.Thresholds) bool {
	changed := false
	if reg == nil {
		changed = meta.RemoveStatusCondition(&e.Status.Conditions, ConditionRegressed)
	} else {
		changed = meta.SetStatusCondition(&e.Status.Conditions, metav1.Condition{
			Type: ConditionRegressed, Status: metav1.ConditionTrue, Reason: ReasonWorseThanStable,
			Message: reg.message, ObservedGeneration: e.Generation,
		})
	}
	verdict := e.Status.Verdict
	if _, without, lift, err := scores(e); err == nil {
		verdict = th.Verdict(lift, without)
	}
	if reg.has(e.Spec.Agent + "/" + e.Spec.Model) {
		verdict = v1alpha1.VerdictRegressed
	}
	if verdict != e.Status.Verdict {
		e.Status.Verdict, changed = verdict, true
	}
	return changed
}

// results converts the SkillEvals of one digest into policy input.
func results(group []*v1alpha1.SkillEval, th policy.Thresholds) []policy.Result {
	out := make([]policy.Result, 0, len(group))
	for _, e := range group {
		res := policy.Result{Required: e.Spec.Required, Succeeded: e.Status.Phase == v1alpha1.PhaseSucceeded}
		if res.Succeeded {
			_, without, lift, err := scores(e)
			if err != nil { // unreadable scores count as not evaluated
				res.Succeeded = false
			}
			// The stored verdict may be Regressed; the no-skill bands decide here.
			res.Lift, res.Verdict = lift, th.Verdict(lift, without)
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
