package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/metrics"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/retry"
)

const (
	envTimeout = `{"error":"no scores in result.json: EnvironmentStartTimeoutError: Environment start timed out after 600.0 seconds"}`
	agentTO    = `{"error":"no scores in result.json: AgentTimeoutError: Agent execution timed out after 900.0 seconds"}`
	installErr = `{"error":"no scores in result.json: NonZeroAgentExitCodeError: Command failed (exit 1): apt-get update && apt-get install -y nodejs npm procps Connection to remote host was lost."}`
	goodResult = `{"withSkill":0.95,"withoutSkill":0.36,"lift":0.59}`
)

func retryEnv(t *testing.T, maxRetries, budget int, objs ...client.Object) *env {
	t.Helper()
	cfg := Config{MaxConcurrentJobs: 4, MaxRetries: maxRetries, Thresholds: policy.DefaultThresholds()}
	return newEnv(t, cfg, budget, map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}}, objs...)
}

// jobExists reports whether the named Job is in the cluster.
func (e *env) jobExists(name string) bool {
	e.t.Helper()
	err := e.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &batchv1.Job{})
	if err != nil && !apierrors.IsNotFound(err) {
		e.t.Fatal(err)
	}
	return err == nil
}

// remaining reads the trials left in the budget.
func (e *env) remaining() int {
	e.r.Budget.mu.Lock()
	defer e.r.Budget.mu.Unlock()
	return e.r.Budget.remaining
}

// retryCounts reads skillci_retries_total per reason from a fresh registry.
func retryCounts(t *testing.T) map[string]float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	metrics.Register(reg)
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range fams {
		if f.GetName() != "skillci_retries_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			out[m.GetLabel()[0].GetValue()] = m.GetCounter().GetValue()
		}
	}
	return out
}

func TestTransientFailureIsRetriedOnce(t *testing.T) {
	before := retryCounts(t)
	e := retryEnv(t, 1, 200)
	se := e.create("acme", "v1.0.0", digestA, haiku)
	e.reconcile(se) // Pending -> Running, job "<name>"
	first := e.get(se).Status.JobName
	if first != se.Name {
		t.Fatalf("first job = %q, want %q", first, se.Name)
	}

	e.finishJob(se, envTimeout, true)
	e.reconcile(se) // transient: retried
	got := e.get(se)
	if got.Status.Phase != v1alpha1.PhaseRunning || got.Status.Retries != 1 || got.Status.JobName != se.Name+"-r1" {
		t.Fatalf("after first failure: phase=%s retries=%d job=%q, want Running/1/%s-r1", got.Status.Phase, got.Status.Retries, got.Status.JobName, se.Name)
	}
	if got.Status.CompletedAt != nil {
		t.Error("a retried run must not be marked completed")
	}
	if e.jobExists(first) || !e.jobExists(got.Status.JobName) {
		t.Errorf("old job exists = %v, new job exists = %v; want false, true", e.jobExists(first), e.jobExists(got.Status.JobName))
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, ConditionRetried)
	if cond == nil || cond.Reason != ReasonInfrastructureFailure || !strings.Contains(cond.Message, "EnvironmentStartTimeoutError") {
		t.Errorf("Retried condition = %+v", cond)
	}
	// Both Jobs spent their trials: attempts 1 x 2 arms each.
	if left := e.remaining(); left != 200-4 {
		t.Errorf("budget left = %d, want %d", left, 200-4)
	}
	if d := retryCounts(t)["env_start_timeout"] - before["env_start_timeout"]; d != 1 {
		t.Errorf("skillci_retries_total{reason=env_start_timeout} grew by %v, want 1", d)
	}
	rec, ok := e.r.Recorder.(*events.FakeRecorder)
	if !ok {
		t.Fatalf("recorder is %T", e.r.Recorder)
	}
	if ev := <-rec.Events; !strings.Contains(ev, "Normal") || !strings.Contains(ev, ReasonInfrastructureFailure) {
		t.Errorf("event = %q, want a Normal %s event", ev, ReasonInfrastructureFailure)
	}

	// The second transient failure is final.
	e.finishJob(se, installErr, true)
	e.reconcile(se)
	got = e.get(se)
	if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Retries != 1 || !strings.Contains(got.Status.Message, "apt-get") {
		t.Errorf("after second failure: phase=%s retries=%d message=%q, want Failed/1/apt-get", got.Status.Phase, got.Status.Retries, got.Status.Message)
	}
	if got.Status.CompletedAt == nil {
		t.Error("completedAt not set on the final failure")
	}
	if d := retryCounts(t)["connection_lost"] - before["connection_lost"]; d != 0 {
		t.Errorf("a final failure counted %v retries", d)
	}
}

func TestRetriedRunCanSucceed(t *testing.T) {
	e := retryEnv(t, 1, 200)
	se := e.create("acme", "v1.0.0", digestA, haiku)
	e.reconcile(se)
	e.finishJob(se, installErr, true)
	e.reconcile(se)
	e.finishJob(se, goodResult, false)
	e.reconcile(se)
	got := e.get(se).Status
	if got.Phase != v1alpha1.PhaseSucceeded || got.Lift != "+0.59" || got.Retries != 1 {
		t.Errorf("status = %+v, want Succeeded with lift +0.59 after 1 retry", got)
	}
	if meta.FindStatusCondition(got.Conditions, ConditionRetried) == nil {
		t.Error("the Retried condition must stay after the retry succeeded")
	}
}

func TestRetryDecisions(t *testing.T) {
	const rejected = `{"tiers":{"tier1":"failed","tier2":"skipped","tier3":"skipped"},"publication":"failed","findings":["tier1/security: hardcoded credential"]}`
	tests := []struct {
		name        string
		maxRetries  int
		message     string
		failedJob   bool
		podEvicted  bool
		wantPhase   v1alpha1.Phase
		wantRetries int
		wantReason  string // metric reason expected to grow, empty for none
	}{
		{"env start timeout", 1, envTimeout, true, false, v1alpha1.PhaseRunning, 1, "env_start_timeout"},
		{"install step lost connection", 1, installErr, true, false, v1alpha1.PhaseRunning, 1, "connection_lost"},
		{"job failed without a message", 1, "", true, false, v1alpha1.PhaseRunning, 1, "job_lost"},
		{"evicted pod with a log tail", 1, "Traceback (most recent call last)", true, true, v1alpha1.PhaseRunning, 1, "job_lost"},
		{"agent timeout is a real result", 1, agentTO, true, false, v1alpha1.PhaseFailed, 0, ""},
		{"runner configuration error", 1, `{"error":"missing required env: AGENT"}`, true, false, v1alpha1.PhaseFailed, 0, ""},
		{"crash log tail without an evicted pod", 1, "Traceback (most recent call last)", true, false, v1alpha1.PhaseFailed, 0, ""},
		{"job complete without a message", 1, "", false, false, v1alpha1.PhaseFailed, 0, ""},
		{"rejected run is a result", 1, rejected, false, false, v1alpha1.PhaseSucceeded, 0, ""},
		{"scores next to an infrastructure error", 1, `{"withSkill":0.9,"withoutSkill":0.4,"lift":0.5,"error":"EnvironmentStartTimeoutError: x"}`, true, false, v1alpha1.PhaseFailed, 0, ""},
		{"job failed after writing scores", 1, goodResult, true, false, v1alpha1.PhaseFailed, 0, ""},
		{"retries disabled", 0, envTimeout, true, false, v1alpha1.PhaseFailed, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := retryCounts(t)
			e := retryEnv(t, tt.maxRetries, 200)
			se := e.create("acme", "v1.0.0", digestA, haiku)
			e.reconcile(se)
			e.finishJob(se, tt.message, tt.failedJob)
			if tt.podEvicted {
				markEvicted(t, e, se)
			}
			e.reconcile(se)

			got := e.get(se).Status
			if got.Phase != tt.wantPhase || got.Retries != tt.wantRetries {
				t.Errorf("phase=%s retries=%d message=%q, want %s/%d", got.Phase, got.Retries, got.Message, tt.wantPhase, tt.wantRetries)
			}
			after := retryCounts(t)
			for reason, n := range after {
				want := 0.0
				if reason == tt.wantReason {
					want = 1
				}
				if n-before[reason] != want {
					t.Errorf("skillci_retries_total{reason=%q} grew by %v, want %v", reason, n-before[reason], want)
				}
			}
			if tt.wantRetries == 0 && meta.FindStatusCondition(got.Conditions, ConditionRetried) != nil {
				t.Error("Retried condition set on a run that was not retried")
			}
		})
	}
}

// markEvicted gives the Job's pod the status Kubernetes leaves on an evicted pod.
func markEvicted(t *testing.T, e *env, se *v1alpha1.SkillEval) {
	t.Helper()
	var pod corev1.Pod
	key := types.NamespacedName{Namespace: ns, Name: se.Name + "-abcde"}
	if err := e.client.Get(context.Background(), key, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Reason = "Evicted"
	if err := e.client.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
}

func TestMaxRetriesTwo(t *testing.T) {
	e := retryEnv(t, 2, 200)
	se := e.create("acme", "v1.0.0", digestA, haiku)
	e.reconcile(se)
	for want := 1; want <= 2; want++ {
		e.finishJob(se, envTimeout, true)
		e.reconcile(se)
		if got := e.get(se).Status; got.Retries != want || got.Phase != v1alpha1.PhaseRunning || got.JobName != se.Name+"-r"+string(rune('0'+want)) {
			t.Fatalf("retry %d: status = %+v", want, got)
		}
	}
	e.finishJob(se, envTimeout, true)
	e.reconcile(se)
	if got := e.get(se).Status; got.Phase != v1alpha1.PhaseFailed || got.Retries != 2 {
		t.Errorf("status = %+v, want Failed after 2 retries", got)
	}
}

func TestRetryWaitsForCapacity(t *testing.T) {
	e := retryEnv(t, 1, 200)
	e.r.Config.MaxConcurrentJobs = 1
	se := e.create("acme", "v1.0.0", digestA, haiku)
	e.reconcile(se) // takes the only slot
	e.finishJob(se, envTimeout, true)

	// Another run takes the slot the failed one frees.
	other := &v1alpha1.SkillEval{Name: "other", Namespace: ns}
	if err := e.client.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	setPhase := func(p v1alpha1.Phase) {
		t.Helper()
		other.Status.Phase = p
		if err := e.client.Status().Update(context.Background(), other); err != nil {
			t.Fatal(err)
		}
	}
	setPhase(v1alpha1.PhaseRunning)

	res := e.reconcile(se)
	got := e.get(se)
	if got.Status.Phase != v1alpha1.PhasePending || got.Status.Retries != 1 || res.RequeueAfter == 0 {
		t.Fatalf("status = %+v, requeue %v; want Pending, 1 retry, requeue", got.Status, res.RequeueAfter)
	}
	if e.jobExists(se.Name+"-r1") || e.jobExists(se.Name) {
		t.Error("a Job exists for the waiting retry")
	}
	if left := e.remaining(); left != 200-2 {
		t.Errorf("budget left = %d, want %d: a waiting retry spends nothing yet", left, 200-2)
	}

	setPhase(v1alpha1.PhaseSucceeded) // the slot frees up
	e.reconcile(se)
	if got := e.get(se).Status; got.Phase != v1alpha1.PhaseRunning || got.JobName != se.Name+"-r1" {
		t.Errorf("status = %+v, want Running with the retry Job", got)
	}
}

func TestRetryRespectsTrialBudget(t *testing.T) {
	e := retryEnv(t, 1, 2) // enough for exactly one Job
	se := e.create("acme", "v1.0.0", digestA, haiku)
	e.reconcile(se)
	e.finishJob(se, envTimeout, true)
	e.reconcile(se)
	got := e.get(se).Status
	if got.Phase != v1alpha1.PhasePending || got.Retries != 1 || !strings.Contains(got.Message, "budget exhausted") {
		t.Errorf("status = %+v, want Pending with 1 retry and an exhausted budget", got)
	}
	if e.jobExists(se.Name + "-r1") {
		t.Error("a retry Job was created beyond the budget")
	}
}

func TestRetriedCounterReasons(t *testing.T) {
	for _, r := range retry.Reasons() {
		if _, ok := retryCounts(t)[string(r)]; !ok {
			t.Errorf("skillci_retries_total{reason=%q} is not published at zero", r)
		}
	}
}
