package controller

import (
	"context"
	"slices"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/models"
	"github.com/dirien/skill-ci/controller/internal/policy"
)

var (
	opus292 = models.Model{Agent: "claude-code", AgentVersion: "2.1.292", Model: "claude-opus-5-5", Required: true}
	opus300 = models.Model{Agent: "claude-code", AgentVersion: "2.1.300", Model: "claude-opus-5-5", Required: true}
)

func TestAgentVersionFlowsThroughARun(t *testing.T) {
	tests := []struct {
		name      string
		model     models.Model
		message   string
		wantSpec  string
		wantEnv   string // AGENT_VERSION of the Job; empty means not set
		wantAfter string // status.agentVersion once the run is done
	}{
		{"pinned and confirmed", opus292, `{"withSkill":0.9,"withoutSkill":0.4,"agentVersion":"2.1.292"}`, "2.1.292", "2.1.292", "2.1.292"},
		{"the trials ran something else", opus292, `{"withSkill":0.9,"withoutSkill":0.4,"agentVersion":"2.1.299"}`, "2.1.292", "2.1.292", "2.1.299"},
		{"pinned, older runner says nothing", opus292, `{"withSkill":0.9,"withoutSkill":0.4}`, "2.1.292", "2.1.292", "2.1.292"},
		{"unpinned learns the default pin", haiku, `{"withSkill":0.9,"withoutSkill":0.4,"agentVersion":"2.1.292"}`, "", "", "2.1.292"},
		{"unpinned, older runner", haiku, `{"withSkill":0.9,"withoutSkill":0.4}`, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Config{MaxConcurrentJobs: 4, Thresholds: policy.DefaultThresholds()}, 200,
				map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}})
			se := e.create("acme", "v1.0.0", digestA, tt.model)
			e.reconcile(se)

			running := e.get(se)
			if running.Status.AgentVersion != tt.wantSpec {
				t.Errorf("status.agentVersion while running = %q, want the requested %q", running.Status.AgentVersion, tt.wantSpec)
			}
			var job batchv1.Job
			if err := e.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: se.Name}, &job); err != nil {
				t.Fatal(err)
			}
			got := ""
			for _, v := range job.Spec.Template.Spec.Containers[0].Env {
				if v.Name == "AGENT_VERSION" {
					got = v.Value
				}
			}
			if got != tt.wantEnv {
				t.Errorf("Job AGENT_VERSION = %q, want %q", got, tt.wantEnv)
			}

			e.finishJob(se, tt.message, false)
			e.reconcile(se)
			done := e.get(se)
			if done.Status.AgentVersion != tt.wantAfter {
				t.Errorf("status.agentVersion after the run = %q, want %q", done.Status.AgentVersion, tt.wantAfter)
			}
			if done.Spec.AgentVersion != tt.wantSpec {
				t.Errorf("spec.agentVersion = %q, want it untouched (%q)", done.Spec.AgentVersion, tt.wantSpec)
			}
		})
	}
}

func TestStaleAgentVersionLabelIsRemoved(t *testing.T) {
	manual := &v1alpha1.SkillEval{
		Name: "by-hand", Namespace: ns,
		Labels: map[string]string{v1alpha1.LabelAgentVersion: "9.9.9"},
		Spec: v1alpha1.SkillEvalSpec{Skill: "acme", Version: "v1.0.0", Digest: digestA, Agent: "claude-code",
			Model: "claude-haiku-5-5", Attempts: 1, Trigger: v1alpha1.TriggerManual},
	}
	e := newEnv(t, Config{MaxConcurrentJobs: 4}, 200, nil, manual)
	e.reconcile(manual)
	if v, ok := e.get(manual).Labels[v1alpha1.LabelAgentVersion]; ok {
		t.Errorf("an unpinned SkillEval keeps agent version label %q, want none", v)
	}
}

func modelsReconciler(e *env) (*ModelsReconciler, ctrl.Request) {
	return &ModelsReconciler{Client: e.client, Harbor: e.harbor, ConfigMap: "skill-ci-models", Config: e.r.Config},
		ctrl.Request{Namespace: ns, Name: "skill-ci-models"}
}

func modelList(t *testing.T, ms ...models.Model) string {
	t.Helper()
	b, err := models.Marshal(ms)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBumpingTheAgentVersionReevaluatesTaggedDigests(t *testing.T) {
	repos := map[string][]harbor.Artifact{"acme": {
		{Digest: digestA, Tags: []string{"v1.0.0", "stable"}},
		{Digest: digestB, Tags: []string{"v1.1.0", "beta"}},
	}}
	cm := &corev1.ConfigMap{Name: "skill-ci-models", Namespace: ns,
		Annotations: map[string]string{AnnotationLastApplied: modelList(t, opus292)},
		Data:        map[string]string{"models.yaml": modelList(t, opus300)}}
	e := newEnv(t, Config{}, 200, repos, cm)
	r, req := modelsReconciler(e)
	for range 2 { // the second pass must change nothing
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	var list v1alpha1.SkillEvalList
	if err := e.client.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("got %d SkillEvals, want one per tagged digest", len(list.Items))
	}
	var names []string
	for _, se := range list.Items {
		names = append(names, se.Name)
		if se.Spec.AgentVersion != "2.1.300" || se.Labels[v1alpha1.LabelAgentVersion] != "2.1.300" ||
			se.Spec.Trigger != v1alpha1.TriggerModelChange || !se.Spec.Required {
			t.Errorf("%s: spec %+v labels %v", se.Name, se.Spec, se.Labels)
		}
	}
	slices.Sort(names)
	want := []string{"acme-v1-0-0-claude-code-2-1-300-claude-opus-5-5", "acme-v1-1-0-claude-code-2-1-300-claude-opus-5-5"}
	if !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestRequiredFlagFollowsTheModelList(t *testing.T) {
	codex := models.Model{Agent: "codex", AgentVersion: "0.161.0", Model: "openai/gpt-x"}
	codexRequired := codex
	codexRequired.Required = true
	repos := map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0", "stable"}}}}
	cm := &corev1.ConfigMap{Name: "skill-ci-models", Namespace: ns,
		Annotations: map[string]string{AnnotationLastApplied: modelList(t, opus292, codex)},
		Data:        map[string]string{"models.yaml": modelList(t, opus292, codexRequired)}}
	e := newEnv(t, Config{}, 200, repos, cm)
	other := codex // the same model under another agent version is another entry
	other.AgentVersion = "0.162.0"
	byCodex, byOther, byClaude := e.create("acme", "v1.0.0", digestA, codex), e.create("acme", "v1.0.0", digestA, other), e.create("acme", "v1.0.0", digestA, opus292)

	r, req := modelsReconciler(e)
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		se   *v1alpha1.SkillEval
		want bool
	}{{byCodex, true}, {byOther, false}, {byClaude, true}} {
		if got := e.get(c.se).Spec.Required; got != c.want {
			t.Errorf("%s: required = %v, want %v", c.se.Name, got, c.want)
		}
	}
}

// A new agent version is a new entry: the candidate is held against the stable
// version run on the same harness release, not against a run on another one.
func TestPolicyComparesPerAgentVersion(t *testing.T) {
	repos := func() map[string][]harbor.Artifact {
		return map[string][]harbor.Artifact{"acme": {
			{Digest: digestA, Tags: []string{"v1.0.0", "beta", "stable"}},
			{Digest: digestB, Tags: []string{"v1.1.0"}},
		}}
	}
	tests := []struct {
		name       string
		candidate  models.Model
		wantStable string
		wantReg    bool
	}{
		{"same release is compared and regresses", opus292, digestA, true},
		{"another release has nothing to compare with", opus300, digestB, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200, repos())
			e.run(e.create("acme", "v1.0.0", digestA, opus292), scoreWith(0.95, 0.36))
			cand := e.create("acme", "v1.1.0", digestB, tt.candidate)
			e.run(cand, scoreWith(0.60, 0.36))
			if got := tags(t, e.harbor, "acme", tt.wantStable); !slices.Contains(got, "stable") {
				t.Errorf("tags of %s = %v, want stable", tt.wantStable[:10], got)
			}
			c := findCondition(e.get(cand).Status.Conditions, ConditionRegressed)
			if (c != nil) != tt.wantReg {
				t.Fatalf("Regressed condition = %+v, want present: %v", c, tt.wantReg)
			}
			if c != nil && !strings.Contains(c.Message, "claude-code@2.1.292/claude-opus-5-5") {
				t.Errorf("message %q does not name the entry with its agent version", c.Message)
			}
		})
	}
}
