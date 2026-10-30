package controller

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/metrics"
	"github.com/dirien/skill-ci/controller/internal/retry"
)

const (
	// ConditionRetried records that the run was started again after an
	// infrastructure failure. Its message is the failure of the previous Job.
	ConditionRetried = "Retried"
	// ReasonInfrastructureFailure is the reason of the Retried condition and its event.
	ReasonInfrastructureFailure = "InfrastructureFailure"
)

// fail ends a run that produced no result. When the failure is an
// infrastructure one (see package retry) and the run has retries left, it is
// started again instead; otherwise the SkillEval becomes Failed with msg.
// jobFailedWithoutMessage says the Job failed and left no usable termination
// message. Callers pass only runs without scores and without a rejection:
// those are results, and they are never retried.
func (r *SkillEvalReconciler) fail(ctx context.Context, se *v1alpha1.SkillEval, msg string, jobFailedWithoutMessage bool) (ctrl.Result, error) {
	if se.Status.Retries < r.Config.MaxRetries {
		if reason, ok := retry.Classify(msg, jobFailedWithoutMessage); ok {
			return r.retryRun(ctx, se, reason, msg)
		}
	}
	return ctrl.Result{}, r.setPhase(ctx, se, v1alpha1.PhaseFailed, msg)
}

// retryRun sends the run back to Pending with a new Job name and tries to
// start it at once. The status is written before the old Job is deleted: if
// the write loses a race the Job still exists, and a crash in between leaves a
// finished Job that its TTL removes. The failed attempt keeps the trials it
// spent; the new Job spends them again when it is created, so the budget
// counts every Job started.
func (r *SkillEvalReconciler) retryRun(ctx context.Context, se *v1alpha1.SkillEval, reason retry.Reason, failure string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	oldJob := se.Status.JobName
	se.Status.Retries++
	meta.SetStatusCondition(&se.Status.Conditions, metav1.Condition{
		Type: ConditionRetried, Status: metav1.ConditionTrue, Reason: ReasonInfrastructureFailure, Message: failure,
	})
	msg := fmt.Sprintf("retry %d of %d after an infrastructure failure (%s)", se.Status.Retries, r.Config.MaxRetries, reason)
	if err := r.setPhase(ctx, se, v1alpha1.PhasePending, msg); err != nil {
		return ctrl.Result{}, err
	}
	metrics.CountRetry(reason)
	log.Info("retrying after an infrastructure failure", "skilleval", se.Name, "reason", string(reason),
		"retry", se.Status.Retries, "failure", failure)
	r.Recorder.Eventf(se, nil, corev1.EventTypeNormal, ReasonInfrastructureFailure, "Retry",
		"Retry %d of %d after an infrastructure failure (%s): %s", se.Status.Retries, r.Config.MaxRetries, reason, failure)

	job := &batchv1.Job{}
	job.Name, job.Namespace = oldJob, se.Namespace
	err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
	if err != nil && !apierrors.IsNotFound(err) {
		// The new Job has its own name, so the run goes on; the old one leaves with its TTL.
		log.Error(err, "deleting the failed job", "job", types.NamespacedName{Namespace: job.Namespace, Name: oldJob})
	}
	return r.reconcilePending(ctx, se)
}
