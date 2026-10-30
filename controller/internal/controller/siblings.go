package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
)

// The regression gate compares a candidate with the stable digest, so a
// verdict can go stale when a SkillEval of another digest finishes or the
// policy moves a tag. Those two events re-enqueue the other finished
// SkillEvals of the skill.
//
// This cannot loop. The trigger is a status update that changed the phase to
// Succeeded or changed the PolicyApplied message (which names tag changes).
// A re-run reconcile writes status only when a verdict, condition or tag
// changed, and the policy is deterministic for fixed scores, so the second
// pass finds nothing to change and writes nothing.

// siblingRequests maps a SkillEval to requests for the other finished
// SkillEvals of the same skill, across all digests.
func (r *SkillEvalReconciler) siblingRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	skill := obj.GetLabels()[v1alpha1.LabelSkill]
	if skill == "" {
		return nil
	}
	var list v1alpha1.SkillEvalList
	err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace()), client.MatchingLabels{v1alpha1.LabelSkill: skill})
	if err != nil {
		// A mapper cannot return an error; the siblings converge on the next event or restart.
		logf.FromContext(ctx).Error(err, "listing sibling skillevals", "skill", skill)
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		s := &list.Items[i]
		if s.Name == obj.GetName() || s.Status.Phase != v1alpha1.PhaseSucceeded {
			continue
		}
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(s)})
	}
	return reqs
}

// resultChanged passes only updates in which a SkillEval reached Succeeded or
// the tag policy reported a different outcome. Creates, deletes and every
// other status write are ignored.
func resultChanged() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldSE, ok1 := e.ObjectOld.(*v1alpha1.SkillEval)
			newSE, ok2 := e.ObjectNew.(*v1alpha1.SkillEval)
			if !ok1 || !ok2 {
				return false
			}
			finished := newSE.Status.Phase == v1alpha1.PhaseSucceeded && oldSE.Status.Phase != v1alpha1.PhaseSucceeded
			return finished || policyMessage(oldSE) != policyMessage(newSE)
		},
	}
}

// policyMessage returns the message of the PolicyApplied condition, or "".
func policyMessage(se *v1alpha1.SkillEval) string {
	if c := meta.FindStatusCondition(se.Status.Conditions, ConditionPolicy); c != nil {
		return c.Message
	}
	return ""
}
