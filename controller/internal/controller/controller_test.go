package controller

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/evals"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/models"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/runner"
)

const (
	ns      = "skill-ci"
	digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var (
	haiku  = models.Model{Agent: "claude-code", Model: "claude-haiku-5-5", Required: true}
	sonnet = models.Model{Agent: "claude-code", Model: "claude-sonnet-5-5", Required: true}
)

type env struct {
	t      *testing.T
	client client.Client
	harbor *harbor.Fake
	r      *SkillEvalReconciler
}

func newEnv(t *testing.T, cfg Config, budget int, repos map[string][]harbor.Artifact, objs ...client.Object) *env {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&v1alpha1.SkillEval{}, &batchv1.Job{}).WithObjects(objs...).Build()
	cfg.Namespace, cfg.Project, cfg.Attempts = ns, "skills", 1
	cfg.Runner = runner.Config{Image: "runner:1", Registry: "harbor.local", Project: "skills"}
	h := harbor.NewFake(repos)
	return &env{t: t, client: c, harbor: h, r: &SkillEvalReconciler{
		Client: c, Reader: c, Harbor: h, Recorder: events.NewFakeRecorder(100), Config: cfg, Budget: NewBudget(budget),
	}}
}

func (e *env) create(skill, version, digest string, m models.Model) *v1alpha1.SkillEval {
	e.t.Helper()
	se := evals.New(ns, evals.Request{Skill: skill, Version: version, Digest: digest, Model: m, Attempts: 1, Trigger: v1alpha1.TriggerPush})
	if err := e.client.Create(context.Background(), se); err != nil {
		e.t.Fatal(err)
	}
	return se
}

func (e *env) reconcile(se *v1alpha1.SkillEval) ctrl.Result {
	e.t.Helper()
	res, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(se)})
	if err != nil {
		e.t.Fatalf("Reconcile(%s): %v", se.Name, err)
	}
	return res
}

func (e *env) get(se *v1alpha1.SkillEval) *v1alpha1.SkillEval {
	e.t.Helper()
	var got v1alpha1.SkillEval
	if err := e.client.Get(context.Background(), client.ObjectKeyFromObject(se), &got); err != nil {
		e.t.Fatal(err)
	}
	return &got
}

// finishJob marks the Job complete (or failed) and leaves a runner pod with the termination message.
func (e *env) finishJob(se *v1alpha1.SkillEval, message string, failed bool) {
	e.t.Helper()
	ctx := context.Background()
	var job batchv1.Job
	if err := e.client.Get(ctx, client.ObjectKeyFromObject(se), &job); err != nil {
		e.t.Fatal(err)
	}
	cond := batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}
	if failed {
		cond = batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit"}
	}
	job.Status.Conditions = []batchv1.JobCondition{cond}
	if err := e.client.Status().Update(ctx, &job); err != nil {
		e.t.Fatal(err)
	}
	if message == "" {
		return
	}
	pod := &corev1.Pod{
		Name: se.Name + "-abcde", Namespace: ns, Labels: map[string]string{"batch.kubernetes.io/job-name": se.Name},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: runner.ContainerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: message}},
		}}},
	}
	if err := e.client.Create(ctx, pod); err != nil {
		e.t.Fatal(err)
	}
}

// run takes a SkillEval from Pending to a finished Job and reconciles the result.
func (e *env) run(se *v1alpha1.SkillEval, message string) *v1alpha1.SkillEval {
	e.t.Helper()
	e.reconcile(se) // Pending -> Running
	e.finishJob(se, message, false)
	e.reconcile(se) // Running -> Succeeded
	return e.get(se)
}

func tags(t *testing.T, h *harbor.Fake, skill, digest string) []string {
	t.Helper()
	arts, err := h.ListArtifacts(context.Background(), skill)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range arts {
		if a.Digest == digest {
			s := slices.Clone(a.Tags)
			slices.Sort(s)
			return s
		}
	}
	t.Fatalf("digest %s not found", digest)
	return nil
}

func TestPendingCreatesJob(t *testing.T) {
	e := newEnv(t, Config{MaxConcurrentJobs: 4}, 200, nil)
	se := e.create("acme", "v1.0.0", digestA, haiku)
	e.reconcile(se)

	got := e.get(se)
	if got.Status.Phase != v1alpha1.PhaseRunning || got.Status.JobName != se.Name {
		t.Fatalf("status = %+v, want Running with the job name", got.Status)
	}
	var job batchv1.Job
	if err := e.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: se.Name}, &job); err != nil {
		t.Fatalf("job not created: %v", err)
	}
	if job.Spec.Template.Spec.Containers[0].Image != "runner:1" {
		t.Errorf("image = %s", job.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestPendingGuards(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		budget      int
		running     int
		wantPhase   v1alpha1.Phase
		wantJob     bool
		wantRequeue bool
		wantMsgSnip string
	}{
		{"dry run plans without a job", Config{DryRun: true, MaxConcurrentJobs: 4}, 200, 0, v1alpha1.PhasePlanned, false, false, "2 trials"},
		{"all slots busy stays pending", Config{MaxConcurrentJobs: 2}, 200, 2, v1alpha1.PhasePending, false, true, "waiting for a free slot"},
		{"free slot runs", Config{MaxConcurrentJobs: 2}, 200, 1, v1alpha1.PhaseRunning, true, false, ""},
		{"budget too small stays pending", Config{MaxConcurrentJobs: 4}, 1, 0, v1alpha1.PhasePending, false, false, "budget exhausted"},
		{"budget exactly enough", Config{MaxConcurrentJobs: 4}, 2, 0, v1alpha1.PhaseRunning, true, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var busy []client.Object
			for i := range tt.running {
				busy = append(busy, &v1alpha1.SkillEval{
					Name: "busy-" + string(rune('a'+i)), Namespace: ns,
					Status: v1alpha1.SkillEvalStatus{Phase: v1alpha1.PhaseRunning},
				})
			}
			e := newEnv(t, tt.cfg, tt.budget, nil, busy...)
			// The fake client drops status on create, so set it explicitly.
			for _, o := range busy {
				if err := e.client.Status().Update(context.Background(), o); err != nil {
					t.Fatal(err)
				}
			}
			se := e.create("acme", "v1.0.0", digestA, haiku)
			res := e.reconcile(se)

			got := e.get(se)
			if got.Status.Phase != tt.wantPhase {
				t.Errorf("phase = %s, want %s", got.Status.Phase, tt.wantPhase)
			}
			if !strings.Contains(got.Status.Message, tt.wantMsgSnip) {
				t.Errorf("message = %q, want it to contain %q", got.Status.Message, tt.wantMsgSnip)
			}
			if (res.RequeueAfter > 0) != tt.wantRequeue {
				t.Errorf("RequeueAfter = %v, want requeue %v", res.RequeueAfter, tt.wantRequeue)
			}
			var jobs batchv1.JobList
			if err := e.client.List(context.Background(), &jobs); err != nil {
				t.Fatal(err)
			}
			if (len(jobs.Items) > 0) != tt.wantJob {
				t.Errorf("jobs = %d, want job %v", len(jobs.Items), tt.wantJob)
			}
		})
	}
}

func TestRunningResult(t *testing.T) {
	tests := []struct {
		name                            string
		message                         string
		failedJob                       bool
		wantPhase                       v1alpha1.Phase
		wantWith, wantWithout, wantLift string
		wantVerdict                     v1alpha1.Verdict
		wantMsg                         string
	}{
		{"scores", `{"withSkill":0.95,"withoutSkill":0.36,"lift":0.59}`, false, v1alpha1.PhaseSucceeded, "0.95", "0.36", "+0.59", v1alpha1.VerdictHelps, ""},
		{"negative lift", `{"withSkill":0.5,"withoutSkill":0.7,"lift":-0.2}`, false, v1alpha1.PhaseSucceeded, "0.50", "0.70", "-0.20", v1alpha1.VerdictHurts, ""},
		{"zero lift is unsigned noise free", `{"withSkill":0.5,"withoutSkill":0.5,"lift":-0.001}`, false, v1alpha1.PhaseSucceeded, "0.50", "0.50", "+0.00", v1alpha1.VerdictNeutral, ""},
		{"obsolete", `{"withSkill":0.9,"withoutSkill":0.9,"lift":0}`, false, v1alpha1.PhaseSucceeded, "0.90", "0.90", "+0.00", v1alpha1.VerdictObsolete, ""},
		{"runner error", `{"error":"no score produced"}`, true, v1alpha1.PhaseFailed, "", "", "", "", "no score produced"},
		{"no message, job failed", "", true, v1alpha1.PhaseFailed, "", "", "", "", "job failed: BackoffLimitExceeded"},
		{"no message, job complete", "", false, v1alpha1.PhaseFailed, "", "", "", "", "no termination message"},
		{"garbage message", "Traceback (most recent call last)", true, v1alpha1.PhaseFailed, "", "", "", "", "not a result"},
		{"job failed with scores is a failure", `{"withSkill":1,"withoutSkill":0,"lift":1}`, true, v1alpha1.PhaseFailed, "", "", "", "", "job failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Config{MaxConcurrentJobs: 4, Thresholds: policy.DefaultThresholds()}, 200,
				map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}})
			se := e.create("acme", "v1.0.0", digestA, haiku)
			e.reconcile(se)
			e.finishJob(se, tt.message, tt.failedJob)
			e.reconcile(se)

			got := e.get(se).Status
			if got.Phase != tt.wantPhase || got.WithSkill != tt.wantWith || got.WithoutSkill != tt.wantWithout ||
				got.Lift != tt.wantLift || got.Verdict != tt.wantVerdict {
				t.Errorf("status = %+v", got)
			}
			if !strings.Contains(got.Message, tt.wantMsg) {
				t.Errorf("message = %q, want it to contain %q", got.Message, tt.wantMsg)
			}
			if got.CompletedAt == nil {
				t.Error("completedAt not set")
			}
		})
	}
}

func TestManualSkillEvalGetsLabels(t *testing.T) {
	manual := &v1alpha1.SkillEval{
		Name: "by-hand", Namespace: ns,
		Spec: v1alpha1.SkillEvalSpec{Skill: "acme", Version: "v1.0.0", Digest: digestA, Agent: "claude-code",
			Model: "claude-haiku-5-5", Attempts: 1, Trigger: v1alpha1.TriggerManual},
	}
	e := newEnv(t, Config{MaxConcurrentJobs: 4}, 200, nil, manual)
	e.reconcile(manual)
	want := evals.Labels("acme", digestA, "claude-code", "claude-haiku-5-5")
	if got := e.get(manual); !reflect.DeepEqual(got.Labels, want) || got.Status.Phase != v1alpha1.PhaseRunning {
		t.Errorf("labels = %v, phase = %s", got.Labels, got.Status.Phase)
	}
}

func TestRunningWaitsForJob(t *testing.T) {
	e := newEnv(t, Config{MaxConcurrentJobs: 4}, 200, nil)
	se := e.create("acme", "v1.0.0", digestA, haiku)
	e.reconcile(se)
	e.reconcile(se) // job still active
	if got := e.get(se).Status.Phase; got != v1alpha1.PhaseRunning {
		t.Errorf("phase = %s, want Running", got)
	}
}

func score(lift float64) string {
	return strings.NewReplacer("LIFT", format(lift, "%.2f"), "WITH", format(0.4+lift, "%.2f")).
		Replace(`{"withSkill":WITH,"withoutSkill":0.40,"lift":LIFT}`)
}

func TestTagPolicy(t *testing.T) {
	type eval struct {
		version, digest string
		model           models.Model
		lift            float64
		msg             string // overrides the score derived from lift
	}
	tests := []struct {
		name  string
		start map[string][]harbor.Artifact
		evals []eval
		runs  int                 // how many of the evals finish; 0 means all
		want  map[string][]string // digest -> sorted tags afterwards
	}{
		{
			name:  "waits for every required model",
			start: map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}},
			evals: []eval{{version: "v1.0.0", digest: digestA, model: haiku, lift: 0.5}, {version: "v1.0.0", digest: digestA, model: sonnet}},
			runs:  1,
			want:  map[string][]string{digestA: {"v1.0.0"}},
		},
		{
			name:  "promotes when all required help by 0.10",
			start: map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}},
			evals: []eval{{version: "v1.0.0", digest: digestA, model: haiku, lift: 0.59}, {version: "v1.0.0", digest: digestA, model: sonnet, lift: 0.10}},
			want:  map[string][]string{digestA: {"beta", "stable", "v1.0.0"}},
		},
		{
			name:  "one weak model blocks stable but still gets beta",
			start: map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}},
			evals: []eval{{version: "v1.0.0", digest: digestA, model: haiku, lift: 0.59}, {version: "v1.0.0", digest: digestA, model: sonnet, lift: 0.09}},
			want:  map[string][]string{digestA: {"beta", "v1.0.0"}},
		},
		{
			name: "new version moves beta and stable",
			start: map[string][]harbor.Artifact{"acme": {
				{Digest: digestA, Tags: []string{"v1.0.0", "beta", "stable"}},
				{Digest: digestB, Tags: []string{"v1.1.0"}},
			}},
			evals: []eval{{version: "v1.1.0", digest: digestB, model: haiku, lift: 0.3}, {version: "v1.1.0", digest: digestB, model: sonnet, lift: 0.3}},
			want:  map[string][]string{digestA: {"v1.0.0"}, digestB: {"beta", "stable", "v1.1.0"}},
		},
		{
			name: "hurting version gets beta but never takes stable",
			start: map[string][]harbor.Artifact{"acme": {
				{Digest: digestA, Tags: []string{"v1.0.0", "beta", "stable"}},
				{Digest: digestB, Tags: []string{"v1.1.0"}},
			}},
			evals: []eval{{version: "v1.1.0", digest: digestB, model: haiku, lift: 0.3}, {version: "v1.1.0", digest: digestB, model: sonnet, lift: -0.10}},
			want:  map[string][]string{digestA: {"stable", "v1.0.0"}, digestB: {"beta", "v1.1.0"}},
		},
		{
			name:  "demotes a stable digest that now hurts a required model",
			start: map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0", "beta", "stable"}}}},
			evals: []eval{{version: "v1.0.0", digest: digestA, model: haiku, lift: 0.3}, {version: "v1.0.0", digest: digestA, model: sonnet, lift: -0.2}},
			want:  map[string][]string{digestA: {"beta", "v1.0.0"}},
		},
		{
			name:  "obsolete never touches stable and never deletes",
			start: map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0", "stable"}}}},
			evals: []eval{
				{version: "v1.0.0", digest: digestA, model: haiku, msg: `{"withSkill":0.9,"withoutSkill":0.9,"lift":0}`},
				{version: "v1.0.0", digest: digestA, model: sonnet, msg: `{"withSkill":0.9,"withoutSkill":0.9,"lift":0}`},
			},
			want: map[string][]string{digestA: {"beta", "stable", "v1.0.0"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200, tt.start)
			var created []*v1alpha1.SkillEval
			for _, ev := range tt.evals {
				created = append(created, e.create("acme", ev.version, ev.digest, ev.model))
			}
			for i, se := range created {
				if tt.runs > 0 && i >= tt.runs {
					break
				}
				msg := tt.evals[i].msg
				if msg == "" {
					msg = score(tt.evals[i].lift)
				}
				e.run(se, msg)
			}
			for digest, want := range tt.want {
				if got := tags(t, e.harbor, "acme", digest); !reflect.DeepEqual(got, want) {
					t.Errorf("tags of %s = %v, want %v", digest[:10], got, want)
				}
			}
		})
	}
}

func TestPolicyRecordsCondition(t *testing.T) {
	e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
		map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}})
	a := e.create("acme", "v1.0.0", digestA, haiku)
	b := e.create("acme", "v1.0.0", digestA, sonnet)
	e.run(a, score(0.5))
	e.run(b, score(0.5))

	// The policy run of the second eval records the decision on both.
	for _, se := range []*v1alpha1.SkillEval{a, b} {
		cond := findCondition(e.get(se).Status.Conditions, ConditionPolicy)
		if cond == nil || cond.Reason != "Promoted" || !strings.Contains(cond.Message, "+stable") {
			t.Errorf("condition on %s = %+v", se.Name, cond)
		}
	}
}

func findCondition(cs []metav1.Condition, t string) *metav1.Condition {
	for i := range cs {
		if cs[i].Type == t {
			return &cs[i]
		}
	}
	return nil
}

func TestModelsReconciler(t *testing.T) {
	cm := func(list, annotation string) *corev1.ConfigMap {
		c := &corev1.ConfigMap{Name: "skill-ci-models", Namespace: ns, Data: map[string]string{"models.yaml": list}}
		if annotation != "" {
			c.Annotations = map[string]string{AnnotationLastApplied: annotation}
		}
		return c
	}
	twoModels := "models:\n- {agent: claude-code, model: claude-haiku-5-5, required: true}\n- {agent: claude-code, model: claude-sonnet-5-5, required: true}\n"
	oneModel := "models:\n- {agent: claude-code, model: claude-haiku-5-5, required: true}\n"
	repos := func() map[string][]harbor.Artifact {
		return map[string][]harbor.Artifact{
			"acme": {
				{Digest: digestA, Tags: []string{"v1.0.0", "stable"}},
				{Digest: digestB, Tags: []string{"v1.1.0", "beta"}},
				{Digest: "sha256:cccc", Tags: []string{"v0.9.0"}}, // untagged beta/stable: not evaluated
			},
			"other": {{Digest: "sha256:dddd", Tags: []string{"stable"}}}, // no version tag: skipped
		}
	}
	tests := []struct {
		name       string
		cm         *corev1.ConfigMap
		wantModels []string // model of each created SkillEval, sorted
		wantDigest []string
	}{
		{"added model evaluates beta and stable digests", cm(twoModels, oneModel),
			[]string{"claude-sonnet-5-5", "claude-sonnet-5-5"}, []string{digestA[:32+7][7:], digestB[7 : 7+32]}},
		{"unchanged list does nothing", cm(twoModels, twoModels), nil, nil},
		{"removed model does nothing", cm(oneModel, twoModels), nil, nil},
		{"first sight treats every model as added", cm(oneModel, ""),
			[]string{"claude-haiku-5-5", "claude-haiku-5-5"}, []string{digestA[7 : 7+32], digestB[7 : 7+32]}},
		{"invalid list is ignored", cm("models: [", oneModel), nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Config{}, 200, repos(), tt.cm)
			r := &ModelsReconciler{Client: e.client, Harbor: e.harbor, ConfigMap: "skill-ci-models", Config: e.r.Config}
			req := ctrl.Request{Namespace: ns, Name: "skill-ci-models"}
			for range 2 { // the second pass must change nothing
				if _, err := r.Reconcile(context.Background(), req); err != nil {
					t.Fatal(err)
				}
			}

			var list v1alpha1.SkillEvalList
			if err := e.client.List(context.Background(), &list); err != nil {
				t.Fatal(err)
			}
			var gotModels, gotDigests []string
			for _, se := range list.Items {
				if se.Spec.Trigger != v1alpha1.TriggerModelChange {
					t.Errorf("trigger = %s, want model-change", se.Spec.Trigger)
				}
				gotModels = append(gotModels, se.Spec.Model)
				gotDigests = append(gotDigests, se.Labels[v1alpha1.LabelDigest])
			}
			slices.Sort(gotModels)
			slices.Sort(gotDigests)
			slices.Sort(tt.wantDigest)
			if !reflect.DeepEqual(gotModels, tt.wantModels) || !reflect.DeepEqual(gotDigests, tt.wantDigest) {
				t.Errorf("created models %v digests %v, want %v %v", gotModels, gotDigests, tt.wantModels, tt.wantDigest)
			}

			var after corev1.ConfigMap
			if err := e.client.Get(context.Background(), req.NamespacedName, &after); err != nil {
				t.Fatal(err)
			}
			if tt.name != "invalid list is ignored" {
				applied, err := models.Parse([]byte(after.Annotations[AnnotationLastApplied]))
				current, _ := models.FromConfigMap(&after)
				if err != nil || !reflect.DeepEqual(applied, current) {
					t.Errorf("last-applied = %v (%v), want %v", applied, err, current)
				}
			}
		})
	}
}

func TestStatusConflictRequeues(t *testing.T) {
	e := newEnv(t, Config{}, 0, nil)
	se := e.create("acme", "v1.0.0", digestA, haiku)
	// Every status write loses the optimistic-concurrency race.
	e.r.Client = interceptor.NewClient(e.client.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(_ context.Context, _ client.Client, _ string, obj client.Object, _ ...client.SubResourceUpdateOption) error {
			return apierrors.NewConflict(v1alpha1.GroupVersion.WithResource("skillevals").GroupResource(), obj.GetName(), nil)
		},
	})

	res, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(se)})
	if err != nil {
		t.Fatalf("Reconcile returned %v, want a requeue without error", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("RequeueAfter = 0, want a requeue after a conflict")
	}
}

// scoreWith renders a runner result with explicit with-skill and without-skill scores.
func scoreWith(with, without float64) string {
	return strings.NewReplacer("WITHOUT", format(without, "%.2f"), "WITH", format(with, "%.2f"), "LIFT", format(with-without, "%.2f")).
		Replace(`{"withSkill":WITH,"withoutSkill":WITHOUT,"lift":LIFT}`)
}

func TestRegressionBlocksPromotion(t *testing.T) {
	tests := []struct {
		name         string
		candidate    [2]float64 // with-skill on haiku and sonnet; stable scored 0.95 on both
		wantStable   string     // digest holding stable afterwards
		wantVerdicts [2]v1alpha1.Verdict
	}{
		{"much worse on both stays beta", [2]float64{0.69, 0.69}, digestA, [2]v1alpha1.Verdict{"Regressed", "Regressed"}},
		{"worse on one model only", [2]float64{0.69, 0.96}, digestA, [2]v1alpha1.Verdict{"Regressed", "Helps"}},
		{"drop within tolerance is promoted", [2]float64{0.91, 0.95}, digestB, [2]v1alpha1.Verdict{"Helps", "Helps"}},
		{"better is promoted", [2]float64{0.99, 0.99}, digestB, [2]v1alpha1.Verdict{"Helps", "Helps"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
				map[string][]harbor.Artifact{"acme": {
					{Digest: digestA, Tags: []string{"v1.0.0", "beta", "stable"}},
					{Digest: digestB, Tags: []string{"v1.2.0"}},
				}})
			for _, m := range []models.Model{haiku, sonnet} {
				e.run(e.create("acme", "v1.0.0", digestA, m), scoreWith(0.95, 0.36))
			}
			cand := []*v1alpha1.SkillEval{e.create("acme", "v1.2.0", digestB, haiku), e.create("acme", "v1.2.0", digestB, sonnet)}
			for i, se := range cand {
				e.run(se, scoreWith(tt.candidate[i], 0.36))
			}

			if got := tags(t, e.harbor, "acme", tt.wantStable); !slices.Contains(got, "stable") {
				t.Errorf("tags of %s = %v, want stable", tt.wantStable[:10], got)
			}
			if got := tags(t, e.harbor, "acme", digestB); !slices.Contains(got, "beta") {
				t.Errorf("tags of v1.2.0 = %v, want beta", got)
			}
			regressed := tt.wantVerdicts[0] == "Regressed" || tt.wantVerdicts[1] == "Regressed"
			for i, se := range cand {
				got := e.get(se)
				if got.Status.Verdict != tt.wantVerdicts[i] {
					t.Errorf("verdict[%d] = %s, want %s", i, got.Status.Verdict, tt.wantVerdicts[i])
				}
				cond := findCondition(got.Status.Conditions, ConditionRegressed)
				if (cond != nil) != regressed {
					t.Fatalf("Regressed condition = %+v, want present: %v", cond, regressed)
				}
				if cond != nil {
					for _, want := range []string{"claude-haiku-5-5", "v1.2.0", "v1.0.0", "0.95"} {
						if cond.Reason != ReasonWorseThanStable || !strings.Contains(cond.Message, want) {
							t.Errorf("condition = %+v, want reason %s and %q in message", cond, ReasonWorseThanStable, want)
						}
					}
				}
			}
		})
	}
}

func TestRegressionWithoutStableBehavesAsBefore(t *testing.T) {
	e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
		map[string][]harbor.Artifact{"acme": {{Digest: digestB, Tags: []string{"v1.2.0"}}}})
	for _, m := range []models.Model{haiku, sonnet} {
		e.run(e.create("acme", "v1.2.0", digestB, m), scoreWith(0.69, 0.36))
	}
	if got := tags(t, e.harbor, "acme", digestB); !slices.Contains(got, "stable") {
		t.Errorf("tags = %v, want stable when there is no stable version to regress from", got)
	}
}

func TestRegressionEmitsWarningEvent(t *testing.T) {
	e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
		map[string][]harbor.Artifact{"acme": {
			{Digest: digestA, Tags: []string{"v1.0.0", "stable"}},
			{Digest: digestB, Tags: []string{"v1.2.0"}},
		}})
	for _, m := range []models.Model{haiku, sonnet} {
		e.run(e.create("acme", "v1.0.0", digestA, m), scoreWith(0.95, 0.36))
		e.run(e.create("acme", "v1.2.0", digestB, m), scoreWith(0.69, 0.36))
	}
	rec, ok := e.r.Recorder.(*events.FakeRecorder)
	if !ok {
		t.Fatalf("recorder is %T", e.r.Recorder)
	}
	close(rec.Events)
	var found bool
	for ev := range rec.Events {
		found = found || strings.Contains(ev, "Warning") && strings.Contains(ev, ReasonWorseThanStable)
	}
	if !found {
		t.Error("no Warning WorseThanStable event")
	}
}

func TestReportDigestIsStored(t *testing.T) {
	const report = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
		map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}})
	se := e.run(e.create("acme", "v1.0.0", digestA, haiku), `{"withSkill":0.95,"withoutSkill":0.36,"lift":0.59,"report":"`+report+`"}`)
	if se.Status.ReportDigest != report {
		t.Errorf("reportDigest = %q, want %q", se.Status.ReportDigest, report)
	}
}
