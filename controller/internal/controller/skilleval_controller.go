package controller

import (
	"context"
	"fmt"
	"maps"
	"math"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/evals"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/metrics"
	"github.com/dirien/skill-ci/controller/internal/runner"
)

const capacityRetry = 15 * time.Second

// SkillEvalReconciler runs a SkillEval as a Job and applies the tag policy to the result.
type SkillEvalReconciler struct {
	client.Client
	// Reader reads straight from the API server, so counts and policy never see a stale cache.
	Reader client.Reader
	// Harbor is the registry whose tags the policy manages.
	Harbor harbor.Client
	// Recorder emits Kubernetes events.
	Recorder events.EventRecorder
	// Config holds the shared settings.
	Config Config
	// Budget caps the trials started by this process.
	Budget *Budget
}

// SetupWithManager registers the reconciler. Status writes do not change the
// generation, so only spec changes and owned Job updates trigger a reconcile.
// A second watch on SkillEvals re-enqueues the finished siblings of a skill
// when one of them reaches Succeeded or the policy changes tags (see siblings.go).
func (r *SkillEvalReconciler) SetupWithManager(mgr ctrl.Manager) error {
	err := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.SkillEval{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&batchv1.Job{}).
		Watches(&v1alpha1.SkillEval{}, handler.EnqueueRequestsFromMapFunc(r.siblingRequests), builder.WithPredicates(resultChanged())).
		Complete(r)
	if err != nil {
		return fmt.Errorf("setting up skilleval controller: %w", err)
	}
	return nil
}

// Reconcile moves a SkillEval through Pending, Running and Succeeded or Failed.
func (r *SkillEvalReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var se v1alpha1.SkillEval
	if err := r.Get(ctx, req.NamespacedName, &se); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	res, err := r.reconcile(ctx, &se)
	if apierrors.IsConflict(err) {
		// Another write updated the SkillEval first; start over from the latest version.
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	return res, err
}

func (r *SkillEvalReconciler) reconcile(ctx context.Context, se *v1alpha1.SkillEval) (ctrl.Result, error) {
	if err := r.ensureLabels(ctx, se); err != nil {
		return ctrl.Result{}, err
	}
	switch se.Status.Phase {
	case "", v1alpha1.PhasePending:
		return r.reconcilePending(ctx, se)
	case v1alpha1.PhaseRunning:
		return ctrl.Result{}, r.reconcileRunning(ctx, se)
	case v1alpha1.PhaseSucceeded:
		return ctrl.Result{}, r.afterSucceeded(ctx, se) // re-run after a restart: gauges and tags converge
	case v1alpha1.PhaseFailed, v1alpha1.PhasePlanned:
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, nil
}

// ensureLabels gives a hand-written SkillEval the labels the controller creates
// on its own ones, so dedupe and the tag policy can find it.
func (r *SkillEvalReconciler) ensureLabels(ctx context.Context, se *v1alpha1.SkillEval) error {
	want := evals.Labels(se.Spec.Skill, se.Spec.Digest, se.Spec.Agent, se.Spec.Model)
	missing := false
	for k, v := range want {
		missing = missing || se.Labels[k] != v
	}
	if !missing {
		return nil
	}
	if se.Labels == nil {
		se.Labels = map[string]string{}
	}
	maps.Copy(se.Labels, want)
	if err := r.Update(ctx, se); err != nil {
		return fmt.Errorf("labelling %s: %w", se.Name, err)
	}
	return nil
}

func (r *SkillEvalReconciler) reconcilePending(ctx context.Context, se *v1alpha1.SkillEval) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	trials := se.Spec.Attempts * 2 // attempts per arm, two arms
	if se.Status.Phase == "" {
		if err := r.setPhase(ctx, se, v1alpha1.PhasePending, ""); err != nil {
			return ctrl.Result{}, err
		}
	}

	if r.Config.DryRun {
		log.Info("dry run: would start a Job", "skill", se.Spec.Skill, "version", se.Spec.Version,
			"model", se.Spec.Model, "trials", trials)
		return ctrl.Result{}, r.setPhase(ctx, se, v1alpha1.PhasePlanned, fmt.Sprintf("dry run: would run %d trials", trials))
	}

	running, err := r.countRunning(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if r.Config.MaxConcurrentJobs > 0 && running >= r.Config.MaxConcurrentJobs {
		msg := fmt.Sprintf("waiting for a free slot (%d of %d jobs running)", running, r.Config.MaxConcurrentJobs)
		if err := r.setMessage(ctx, se, msg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: capacityRetry}, nil
	}
	if !r.Budget.Spend(trials) {
		log.Info("trial budget exhausted", "skilleval", se.Name, "trials", trials)
		return ctrl.Result{}, r.setMessage(ctx, se, "trial budget exhausted")
	}

	job := runner.NewJob(se, r.Config.Runner)
	if err := r.Create(ctx, job); err != nil {
		// Either way no new trials started: a retry after a lost status write
		// finds the Job it already created.
		r.Budget.Refund(trials)
		if !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("creating job %s: %w", job.Name, err)
		}
	}
	se.Status.JobName = job.Name
	return ctrl.Result{}, r.setPhase(ctx, se, v1alpha1.PhaseRunning, "")
}

func (r *SkillEvalReconciler) reconcileRunning(ctx context.Context, se *v1alpha1.SkillEval) error {
	var job batchv1.Job
	err := r.Get(ctx, types.NamespacedName{Namespace: se.Namespace, Name: se.Status.JobName}, &job)
	if apierrors.IsNotFound(err) {
		return r.setPhase(ctx, se, v1alpha1.PhaseFailed, "job "+se.Status.JobName+" not found")
	}
	if err != nil {
		return fmt.Errorf("getting job %s: %w", se.Status.JobName, err)
	}
	complete, failed, reason := jobOutcome(&job)
	if !complete && !failed {
		return nil // the next Job event reconciles again
	}

	msg, err := r.terminationMessage(ctx, &job)
	if err != nil {
		return err
	}
	if msg == "" && failed {
		return r.setPhase(ctx, se, v1alpha1.PhaseFailed, "job failed: "+reason)
	}
	res, err := runner.ParseResult(msg)
	switch {
	case err != nil:
		return r.setPhase(ctx, se, v1alpha1.PhaseFailed, err.Error())
	case res.Error != "":
		return r.setPhase(ctx, se, v1alpha1.PhaseFailed, res.Error)
	case failed:
		return r.setPhase(ctx, se, v1alpha1.PhaseFailed, "job failed: "+reason)
	}

	se.Status.WithSkill = format(*res.WithSkill, "%.2f")
	se.Status.WithoutSkill = format(*res.WithoutSkill, "%.2f")
	se.Status.Lift = format(*res.Lift, "%+.2f")
	se.Status.ReportDigest = res.Report
	se.Status.Verdict = r.Config.Thresholds.Verdict(*res.Lift, *res.WithoutSkill)
	if err := r.setPhase(ctx, se, v1alpha1.PhaseSucceeded, ""); err != nil {
		return err
	}
	return r.afterSucceeded(ctx, se)
}

// afterSucceeded publishes the scores as metrics and applies the tag policy.
func (r *SkillEvalReconciler) afterSucceeded(ctx context.Context, se *v1alpha1.SkillEval) error {
	with, without, lift, err := scores(se)
	if err != nil {
		return err
	}
	metrics.ObserveScores(se, with, without, lift)
	return r.applyPolicy(ctx, se)
}

// countRunning counts Running SkillEvals.
func (r *SkillEvalReconciler) countRunning(ctx context.Context) (int, error) {
	var list v1alpha1.SkillEvalList
	if err := r.Reader.List(ctx, &list, client.InNamespace(r.Config.Namespace)); err != nil {
		return 0, fmt.Errorf("listing skillevals: %w", err)
	}
	n := 0
	for _, e := range list.Items {
		if e.Status.Phase == v1alpha1.PhaseRunning {
			n++
		}
	}
	return n, nil
}

// terminationMessage returns the runner container's termination message, or "" if there is none.
func (r *SkillEvalReconciler) terminationMessage(ctx context.Context, job *batchv1.Job) (string, error) {
	var pods corev1.PodList
	err := r.Reader.List(ctx, &pods, client.InNamespace(job.Namespace), client.MatchingLabels{"batch.kubernetes.io/job-name": job.Name})
	if err != nil {
		return "", fmt.Errorf("listing pods of job %s: %w", job.Name, err)
	}
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name == runner.ContainerName && cs.State.Terminated != nil {
				return cs.State.Terminated.Message, nil
			}
		}
	}
	return "", nil
}

// setPhase writes the phase and counts the transition.
func (r *SkillEvalReconciler) setPhase(ctx context.Context, se *v1alpha1.SkillEval, phase v1alpha1.Phase, msg string) error {
	changed := se.Status.Phase != phase
	se.Status.Phase = phase
	se.Status.Message = msg
	if phase == v1alpha1.PhaseSucceeded || phase == v1alpha1.PhaseFailed {
		now := metav1.Now()
		se.Status.CompletedAt = &now
	}
	if err := r.Status().Update(ctx, se); err != nil {
		return fmt.Errorf("updating status of %s: %w", se.Name, err)
	}
	if changed {
		metrics.CountPhase(phase)
	}
	return nil
}

// setMessage updates only the status message, and only if it changed.
func (r *SkillEvalReconciler) setMessage(ctx context.Context, se *v1alpha1.SkillEval, msg string) error {
	if se.Status.Message == msg {
		return nil
	}
	se.Status.Message = msg
	if err := r.Status().Update(ctx, se); err != nil {
		return fmt.Errorf("updating status of %s: %w", se.Name, err)
	}
	return nil
}

// jobOutcome says whether the Job completed or failed, and why.
func jobOutcome(job *batchv1.Job) (complete, failed bool, reason string) {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			complete = true
		case batchv1.JobFailed:
			failed = true
			reason = c.Reason + ": " + c.Message
		}
	}
	return complete, failed, reason
}

// format renders v with the two-decimal layout, never as -0.00.
func format(v float64, layout string) string {
	v = math.Round(v*100) / 100
	if v == 0 {
		v = 0 // drops the sign of negative zero
	}
	return fmt.Sprintf(layout, v)
}

// scores reads the status strings back into numbers.
func scores(se *v1alpha1.SkillEval) (with, without, lift float64, err error) {
	for _, f := range []struct {
		dst *float64
		src string
	}{{&with, se.Status.WithSkill}, {&without, se.Status.WithoutSkill}, {&lift, se.Status.Lift}} {
		if *f.dst, err = strconv.ParseFloat(f.src, 64); err != nil {
			return 0, 0, 0, fmt.Errorf("skilleval %s has an unreadable score %q: %w", se.Name, f.src, err)
		}
	}
	return with, without, lift, nil
}
