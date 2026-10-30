package controller

import (
	"context"
	"fmt"
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

// ConditionPolicy is the condition type that records what the tag policy decided.
const ConditionPolicy = "PolicyApplied"

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
		outcomes[digest] = r.Config.Thresholds.Evaluate(results(group))
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
	return r.record(ctx, byDigest[digest], out, ops)
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
func (r *SkillEvalReconciler) record(ctx context.Context, group []*v1alpha1.SkillEval, out policy.Outcome, ops []policy.Op) error {
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
		if !changed && len(ops) == 0 {
			continue
		}
		if err := r.Status().Update(ctx, e); err != nil {
			return fmt.Errorf("recording policy on %s: %w", e.Name, err)
		}
		r.Recorder.Eventf(e, nil, eventType, reason, "ApplyPolicy", "%s", msg)
	}
	return nil
}

// results converts the SkillEvals of one digest into policy input.
func results(group []*v1alpha1.SkillEval) []policy.Result {
	out := make([]policy.Result, 0, len(group))
	for _, e := range group {
		res := policy.Result{Required: e.Spec.Required, Succeeded: e.Status.Phase == v1alpha1.PhaseSucceeded, Verdict: e.Status.Verdict}
		if res.Succeeded {
			_, _, lift, err := scores(e)
			if err != nil { // unreadable scores count as not evaluated
				res.Succeeded = false
			}
			res.Lift = lift
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
