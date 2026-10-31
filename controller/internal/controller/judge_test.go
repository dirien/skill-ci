package controller

import (
	"context"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/models"
	"github.com/dirien/skill-ci/controller/internal/network"
	"github.com/dirien/skill-ci/controller/internal/platform"
	"github.com/dirien/skill-ci/controller/internal/policy"
)

// jobSecretKey returns the llm-keys key of a secret variable of the Job, "" when absent.
func jobSecretKey(t *testing.T, e *env, jobName, name string) string {
	t.Helper()
	var job batchv1.Job
	if err := e.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: jobName}, &job); err != nil {
		t.Fatal(err)
	}
	for _, v := range job.Spec.Template.Spec.Containers[0].Env {
		if v.Name == name && v.ValueFrom != nil && v.ValueFrom.SecretKeyRef != nil {
			return v.ValueFrom.SecretKeyRef.Key
		}
	}
	return ""
}

func TestJobFollowsTheSkillCIConfig(t *testing.T) {
	tests := []struct {
		name      string
		spec      v1alpha1.SkillCIConfigSpec
		mode      string
		wantPhase v1alpha1.Phase
		wantEnv   map[string]string // "" means absent
		wantKey   string
		wantHosts []string
		wantMsg   string
		entry     *models.Model // default haiku
	}{
		{
			name: "built-in defaults", mode: "none", wantPhase: v1alpha1.PhaseRunning,
			wantEnv: map[string]string{"SKILL_VERSION": "v1.0.0", "HUBBLE_SERVER": "", "SKILL_EVAL_LLM_PROVIDER": "anthropic",
				"SKILL_EVAL_LLM_MODEL": "", "SKILL_EVAL_LLM_BASE_URL": ""},
			wantKey: "anthropic", wantHosts: []string{"api.anthropic.com"},
		},
		{
			name: "hubble relay and an openai-compatible judge", mode: "none", wantPhase: v1alpha1.PhaseRunning,
			spec: v1alpha1.SkillCIConfigSpec{
				Observability: v1alpha1.Observability{HubbleRelay: "hubble-relay.kube-system.svc.cluster.local:80"},
				Judge:         v1alpha1.Judge{Provider: "openai-compatible", Model: "gpt-oss-120b", BaseURL: "https://llm.example.com/v1", SecretKey: "gateway"},
			},
			wantEnv: map[string]string{"SKILL_VERSION": "v1.0.0", "HUBBLE_SERVER": "hubble-relay.kube-system.svc.cluster.local:80",
				"SKILL_EVAL_LLM_PROVIDER": "openai-compatible", "SKILL_EVAL_LLM_MODEL": "gpt-oss-120b", "SKILL_EVAL_LLM_BASE_URL": "https://llm.example.com/v1"},
			wantKey: "gateway", wantHosts: []string{"api.anthropic.com", "llm.example.com"},
		},
		{
			name: "a judge the runner does not support for the agent fails without a Job", mode: "full", wantPhase: v1alpha1.PhaseFailed,
			spec:    v1alpha1.SkillCIConfigSpec{Judge: v1alpha1.Judge{Provider: "openai-compatible", BaseURL: "https://llm.example.com/v1"}},
			wantMsg: `judge provider "openai-compatible" cannot score agent "codex"`,
			entry:   &models.Model{Agent: "codex", AgentVersion: "0.161.0", Model: "openai/gpt-6.1-sol"},
		},
		{
			name: "an nv_build judge with network full", mode: "full", wantPhase: v1alpha1.PhaseRunning,
			spec:    v1alpha1.SkillCIConfigSpec{Judge: v1alpha1.Judge{Provider: "nv_build", Model: "nvidia/llama-4-judge", SecretKey: "nvidia"}},
			wantEnv: map[string]string{"SKILL_EVAL_LLM_PROVIDER": "nv_build", "SKILL_EVAL_LLM_MODEL": "nvidia/llama-4-judge", "SKILL_EVAL_LLM_BASE_URL": ""},
			wantKey: "nvidia",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repos := map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"},
				Annotations: map[string]string{network.AnnotationMode: tt.mode}}}}
			e := newEnv(t, Config{MaxConcurrentJobs: 4, Thresholds: policy.DefaultThresholds()}, 200, repos,
				skillCIConfig(v1alpha1.SkillCIConfigName, tt.spec))
			e.r.CRDs = allCRDs
			e.r.Config.Platform = &platform.Source{Reader: e.client, Namespace: ns}
			entry := haiku
			if tt.entry != nil {
				entry = *tt.entry
			}
			se := e.create("acme", "v1.0.0", digestA, entry)
			e.reconcile(se)
			got := e.get(se)
			if got.Status.Phase != tt.wantPhase || !strings.Contains(got.Status.Message, tt.wantMsg) {
				t.Fatalf("phase=%s message=%q, want %s %q", got.Status.Phase, got.Status.Message, tt.wantPhase, tt.wantMsg)
			}
			if tt.wantPhase != v1alpha1.PhaseRunning {
				if _, ok := jobEnv(t, e.client, se.Name); ok {
					t.Error("a run that cannot start has a Job")
				}
				return
			}
			env, ok := jobEnv(t, e.client, got.Status.JobName)
			if !ok {
				t.Fatal("no Job")
			}
			for name, want := range tt.wantEnv {
				if v, present := env[name]; v != want || present != (want != "") {
					t.Errorf("%s = %q (set: %v), want %q", name, v, present, want)
				}
			}
			if key := jobSecretKey(t, e, got.Status.JobName, "SKILL_EVAL_LLM_API_KEY"); key != tt.wantKey {
				t.Errorf("SKILL_EVAL_LLM_API_KEY from llm-keys/%s, want llm-keys/%s", key, tt.wantKey)
			}
			if hosts := got.Status.Network.Hosts; strings.Join(hosts, ",") != strings.Join(tt.wantHosts, ",") {
				t.Errorf("network hosts %v, want %v", hosts, tt.wantHosts)
			}
		})
	}
}
