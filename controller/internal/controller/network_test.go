package controller

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/crds"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/models"
	"github.com/dirien/skill-ci/controller/internal/network"
	"github.com/dirien/skill-ci/controller/internal/platform"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/profiles"
)

// testMapper knows the scheme's kinds plus the optional Cilium and Agent Sandbox kinds.
func testMapper(s *runtime.Scheme) meta.RESTMapper {
	m := meta.NewDefaultRESTMapper(nil)
	for gvk := range s.AllKnownTypes() {
		m.Add(gvk, meta.RESTScopeNamespace)
	}
	for _, gvk := range []schema.GroupVersionKind{network.CiliumPolicyGVK, profiles.TemplateGVK, profiles.WarmPoolGVK} {
		m.Add(gvk, meta.RESTScopeNamespace)
	}
	return m
}

var allCRDs = crds.Static{network.CiliumPolicyCRD: true, profiles.TemplateCRD: true, profiles.WarmPoolCRD: true}

func readyProfile(name, warmPool string) *v1alpha1.SandboxProfile {
	return &v1alpha1.SandboxProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status:     v1alpha1.SandboxProfileStatus{WarmPoolName: warmPool, TemplateName: warmPool},
	}
}

func jobEnv(t *testing.T, c client.Client, name string) (map[string]string, bool) {
	t.Helper()
	var job batchv1.Job
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &job); err != nil {
		return nil, false
	}
	env := map[string]string{}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.ValueFrom == nil {
			env[e.Name] = e.Value
		}
	}
	return env, true
}

func getPolicy(t *testing.T, c client.Client, name string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(network.CiliumPolicyGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, u); err != nil {
		return nil
	}
	return u
}

func TestPendingNetworkAndProfile(t *testing.T) {
	codex := models.Model{Agent: "codex", AgentVersion: "0.161.0", Model: "openai/gpt-6.1-sol"}
	tests := []struct {
		name        string
		annotations map[string]string
		network     network.Policy
		crds        crds.Checker
		entry       models.Model
		profile     string // spec.sandboxProfile
		objs        []client.Object
		preset      *v1alpha1.NetworkSpec

		wantPhase  v1alpha1.Phase
		wantMsg    string
		wantReason string
		wantSpec   v1alpha1.NetworkSpec
		wantStatus *v1alpha1.NetworkStatus
		wantPolicy string // a host of the eval policy, or "world"; "" means no policy
		wantPool   string
		wantProf   string
	}{
		{
			name:       "nothing declared, no cilium: full, not enforced, bare sandbox",
			wantPhase:  v1alpha1.PhaseRunning,
			wantReason: network.ReasonNotEnforced,
			wantSpec:   v1alpha1.NetworkSpec{Mode: "full", Source: "default"},
			wantStatus: &v1alpha1.NetworkStatus{Mode: "full"},
		},
		{
			// No skillci.dev/network annotation on the digest means full, from the platform default.
			name:       "nothing declared with cilium: full through a policy",
			crds:       allCRDs,
			wantPhase:  v1alpha1.PhaseRunning,
			wantReason: network.ReasonEnforced,
			wantSpec:   v1alpha1.NetworkSpec{Mode: "full", Source: "default"},
			wantStatus: &v1alpha1.NetworkStatus{Mode: "full", Enforced: true, Policy: "skillci-net-acme-v1-0-0-claude-code-claude-haiku-5-5"},
			wantPolicy: "world",
		},
		{
			name:        "declared none with cilium allows the model host",
			annotations: map[string]string{network.AnnotationMode: "none"},
			crds:        allCRDs,
			wantPhase:   v1alpha1.PhaseRunning,
			wantReason:  network.ReasonEnforced,
			wantSpec:    v1alpha1.NetworkSpec{Mode: "none", Source: "skill"},
			wantStatus: &v1alpha1.NetworkStatus{Mode: "none", Enforced: true, Policy: "skillci-net-acme-v1-0-0-claude-code-claude-haiku-5-5",
				Hosts: []string{"api.anthropic.com"}},
			wantPolicy: "api.anthropic.com",
		},
		{
			name:        "declared allowlist expands bundles",
			annotations: map[string]string{network.AnnotationMode: "allowlist", network.AnnotationAllow: "@python,dl.k8s.io"},
			crds:        allCRDs,
			wantPhase:   v1alpha1.PhaseRunning,
			wantReason:  network.ReasonEnforced,
			wantSpec:    v1alpha1.NetworkSpec{Mode: "allowlist", Allow: []string{"@python", "dl.k8s.io"}, Source: "skill"},
			wantStatus: &v1alpha1.NetworkStatus{Mode: "allowlist", Enforced: true, Policy: "skillci-net-acme-v1-0-0-claude-code-claude-haiku-5-5",
				Hosts: []string{"api.anthropic.com", "dl.k8s.io", "files.pythonhosted.org", "pypi.org"}},
			wantPolicy: "files.pythonhosted.org",
		},
		{
			name:       "a spec that has a network is not looked up again",
			preset:     &v1alpha1.NetworkSpec{Mode: "none", Source: "skill"},
			crds:       allCRDs,
			wantPhase:  v1alpha1.PhaseRunning,
			wantReason: network.ReasonEnforced,
			wantSpec:   v1alpha1.NetworkSpec{Mode: "none", Source: "skill"},
			wantStatus: &v1alpha1.NetworkStatus{Mode: "none", Enforced: true, Policy: "skillci-net-acme-v1-0-0-claude-code-claude-haiku-5-5",
				Hosts: []string{"api.anthropic.com"}},
			wantPolicy: "api.anthropic.com",
		},
		{
			name:  "codex uses the host of llm-keys openai-base-url",
			entry: codex, annotations: map[string]string{network.AnnotationMode: "none"}, crds: allCRDs,
			objs: []client.Object{&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "llm-keys", Namespace: ns},
				Data: map[string][]byte{"openai-base-url": []byte("https://gateway.example.com/v1")}}},
			wantPhase:  v1alpha1.PhaseRunning,
			wantReason: network.ReasonEnforced,
			wantSpec:   v1alpha1.NetworkSpec{Mode: "none", Source: "skill"},
			wantStatus: &v1alpha1.NetworkStatus{Mode: "none", Enforced: true, Policy: "skillci-net-acme-v1-0-0-codex-0-161-0-openai-gpt-6-1-sol",
				Hosts: []string{"gateway.example.com"}},
			wantPolicy: "gateway.example.com",
		},
		{
			name:        "none without cilium fails without a job",
			annotations: map[string]string{network.AnnotationMode: "none"},
			wantPhase:   v1alpha1.PhaseFailed,
			wantMsg:     `network mode "none" needs Cilium (CiliumNetworkPolicy CRD not found); this cluster can only run full`,
			wantReason:  network.ReasonNoCilium,
			wantSpec:    v1alpha1.NetworkSpec{Mode: "none", Source: "skill"},
			wantStatus:  &v1alpha1.NetworkStatus{Mode: "none"},
		},
		{
			name:        "a mode the platform does not allow fails",
			annotations: map[string]string{network.AnnotationMode: "full"},
			network:     network.Policy{Default: "none", Allowed: []string{"none", "allowlist"}},
			crds:        allCRDs,
			wantPhase:   v1alpha1.PhaseFailed,
			wantMsg:     `network mode "full" is not allowed on this platform (allowed: none, allowlist)`,
			wantReason:  network.ReasonNotAllowed,
			wantSpec:    v1alpha1.NetworkSpec{Mode: "full", Source: "skill"},
			wantStatus:  &v1alpha1.NetworkStatus{Mode: "full"},
		},
		{
			name:        "an unknown bundle fails",
			annotations: map[string]string{network.AnnotationMode: "allowlist", network.AnnotationAllow: "@rust"},
			crds:        allCRDs,
			wantPhase:   v1alpha1.PhaseFailed,
			wantMsg:     `unknown network bundle "@rust" (defined: @apt, @github, @node, @python)`,
			wantReason:  network.ReasonUnknownBundle,
			wantSpec:    v1alpha1.NetworkSpec{Mode: "allowlist", Allow: []string{"@rust"}, Source: "skill"},
			wantStatus:  &v1alpha1.NetworkStatus{Mode: "allowlist"},
		},
		{
			name:       "enforcement cilium without the CRD fails",
			network:    network.Policy{Enforcement: network.EnforcementCilium},
			wantPhase:  v1alpha1.PhaseFailed,
			wantMsg:    "network enforcement is cilium, but the CiliumNetworkPolicy CRD was not found",
			wantReason: network.ReasonNoCilium,
			wantSpec:   v1alpha1.NetworkSpec{Mode: "full", Source: "default"},
			wantStatus: &v1alpha1.NetworkStatus{Mode: "full"},
		},
		{
			name:       "enforcement off runs none without a policy",
			network:    network.Policy{Enforcement: network.EnforcementOff},
			preset:     &v1alpha1.NetworkSpec{Mode: "none", Source: "skill"},
			crds:       allCRDs,
			wantPhase:  v1alpha1.PhaseRunning,
			wantReason: network.ReasonNotEnforced,
			wantSpec:   v1alpha1.NetworkSpec{Mode: "none", Source: "skill"},
			wantStatus: &v1alpha1.NetworkStatus{Mode: "none", Hosts: []string{"api.anthropic.com"}},
		},
		{
			name:      "a profile the run names must exist",
			profile:   "big",
			wantPhase: v1alpha1.PhaseFailed,
			wantMsg:   `sandbox profile "big" not found`,
			wantSpec:  v1alpha1.NetworkSpec{Mode: "full", Source: "default"},
		},
		{
			name:       "the default profile's warm pool reaches the job",
			objs:       []client.Object{readyProfile("default", "default")},
			wantPhase:  v1alpha1.PhaseRunning,
			wantReason: network.ReasonNotEnforced,
			wantSpec:   v1alpha1.NetworkSpec{Mode: "full", Source: "default"},
			wantStatus: &v1alpha1.NetworkStatus{Mode: "full"},
			wantPool:   "default", wantProf: "default",
		},
		{
			name:       "a named profile's warm pool reaches the job",
			profile:    "big",
			objs:       []client.Object{readyProfile("big", "big")},
			wantPhase:  v1alpha1.PhaseRunning,
			wantReason: network.ReasonNotEnforced,
			wantSpec:   v1alpha1.NetworkSpec{Mode: "full", Source: "default"},
			wantStatus: &v1alpha1.NetworkStatus{Mode: "full"},
			wantPool:   "big", wantProf: "big",
		},
		{
			name:       "a profile without a warm pool (no Agent Sandbox extensions) runs a bare sandbox",
			objs:       []client.Object{readyProfile("default", "")},
			wantPhase:  v1alpha1.PhaseRunning,
			wantReason: network.ReasonNotEnforced,
			wantSpec:   v1alpha1.NetworkSpec{Mode: "full", Source: "default"},
			wantStatus: &v1alpha1.NetworkStatus{Mode: "full"},
			wantProf:   "default",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.network
			p.Bundles, p.Preinstalled, p.InstallHosts = network.DefaultBundles, network.DefaultPreinstalledAgents, network.DefaultAgentInstallHosts
			cfg := Config{MaxConcurrentJobs: 4, Thresholds: policy.DefaultThresholds(),
				Platform: platform.Static(platform.Settings{Network: p, DefaultSandboxProfile: "default"})}
			e := newEnv(t, cfg, 200, map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}, Annotations: tt.annotations}}}, tt.objs...)
			e.r.CRDs = tt.crds
			for _, o := range tt.objs { // the fake client drops status on create
				if sp, ok := o.(*v1alpha1.SandboxProfile); ok {
					if err := e.client.Status().Update(context.Background(), sp); err != nil {
						t.Fatal(err)
					}
				}
			}
			entry := haiku
			if tt.entry.Agent != "" {
				entry = tt.entry
			}
			se := e.create("acme", "v1.0.0", digestA, entry)
			if tt.profile != "" || tt.preset != nil {
				se.Spec.SandboxProfile, se.Spec.Network = tt.profile, tt.preset
				if err := e.client.Update(context.Background(), se); err != nil {
					t.Fatal(err)
				}
			}
			e.reconcile(se)
			got := e.get(se)

			if got.Status.Phase != tt.wantPhase || !strings.Contains(got.Status.Message, tt.wantMsg) {
				t.Fatalf("phase %s %q, want %s %q", got.Status.Phase, got.Status.Message, tt.wantPhase, tt.wantMsg)
			}
			if got.Spec.Network == nil || !equalSpec(*got.Spec.Network, tt.wantSpec) {
				t.Errorf("spec.network %+v, want %+v", got.Spec.Network, tt.wantSpec)
			}
			if !equalStatus(got.Status.Network, tt.wantStatus) {
				t.Errorf("status.network %+v, want %+v", got.Status.Network, tt.wantStatus)
			}
			cond := findCondition(got.Status.Conditions, ConditionNetworkPolicy)
			if (cond == nil) != (tt.wantReason == "") || (cond != nil && cond.Reason != tt.wantReason) {
				t.Errorf("NetworkPolicy condition %+v, want reason %q", cond, tt.wantReason)
			}
			if cond != nil && tt.wantPhase == v1alpha1.PhaseFailed && cond.Message != tt.wantMsg {
				t.Errorf("condition message %q, want %q", cond.Message, tt.wantMsg)
			}

			env, hasJob := jobEnv(t, e.client, se.Name)
			if hasJob != (tt.wantPhase == v1alpha1.PhaseRunning) {
				t.Fatalf("job exists %t in phase %s", hasJob, got.Status.Phase)
			}
			if hasJob {
				if env["SKILLEVAL_NAME"] != se.Name || env["SANDBOX_WARM_POOL"] != tt.wantPool {
					t.Errorf("job env SKILLEVAL_NAME=%q SANDBOX_WARM_POOL=%q, want %q", env["SKILLEVAL_NAME"], env["SANDBOX_WARM_POOL"], tt.wantPool)
				}
			}
			if got.Status.WarmPool != tt.wantPool || got.Status.SandboxProfile != tt.wantProf {
				t.Errorf("status profile/pool %q/%q, want %q/%q", got.Status.SandboxProfile, got.Status.WarmPool, tt.wantProf, tt.wantPool)
			}

			pol := getPolicy(t, e.client, network.PolicyName(se.Name))
			if (pol != nil) != (tt.wantPolicy != "") {
				t.Fatalf("policy exists %t, want %q", pol != nil, tt.wantPolicy)
			}
			if pol != nil {
				if refs := pol.GetOwnerReferences(); len(refs) != 1 || refs[0].Kind != "SkillEval" || refs[0].Name != se.Name {
					t.Errorf("policy owners %+v", refs)
				}
				spec, err := toJSON(pol.Object["spec"])
				if err != nil || !strings.Contains(spec, `"`+tt.wantPolicy+`"`) {
					t.Errorf("policy spec %s, want it to contain %q", spec, tt.wantPolicy)
				}
				if !strings.Contains(spec, `"k8s-app":"kube-dns"`) || !strings.Contains(spec, `"port":"53","protocol":"ANY"`) {
					t.Errorf("policy spec %s lacks the DNS rule", spec)
				}
			}
		})
	}
}

func toJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func equalSpec(a, b v1alpha1.NetworkSpec) bool {
	return a.Mode == b.Mode && a.Source == b.Source && slices.Equal(a.Allow, b.Allow)
}

func equalStatus(a, b *v1alpha1.NetworkStatus) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Mode == b.Mode && a.Policy == b.Policy && a.Enforced == b.Enforced && slices.Equal(a.Hosts, b.Hosts)
}

func TestPolicyIsCreatedBeforeTheJob(t *testing.T) {
	var order []string
	e := newEnv(t, Config{MaxConcurrentJobs: 4}, 200, nil)
	e.r.CRDs = allCRDs
	e.r.Client = interceptor.NewClient(e.client.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			order = append(order, obj.GetObjectKind().GroupVersionKind().Kind+"/"+typeName(obj))
			return c.Create(ctx, obj, opts...)
		},
	})
	se := e.create("acme", "v1.0.0", digestA, haiku)
	e.reconcile(se)
	if len(order) != 2 || !strings.HasPrefix(order[0], "CiliumNetworkPolicy/") || order[1] != "/Job" {
		t.Fatalf("creates %v, want the policy before the job", order)
	}
}

func typeName(obj client.Object) string {
	if _, ok := obj.(*batchv1.Job); ok {
		return "Job"
	}
	return obj.GetName()
}

func TestHarborErrorRequeuesWithoutAJob(t *testing.T) {
	e := newEnv(t, Config{MaxConcurrentJobs: 4}, 200, nil)
	se := e.create("acme", "v1.0.0", digestA, haiku)
	e.harbor.Err = errors.New("harbor down")
	if _, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(se)}); err == nil {
		t.Fatal("want an error while Harbor is down")
	}
	if _, ok := jobEnv(t, e.client, se.Name); ok {
		t.Fatal("no job may start before the network is known")
	}
	if got := e.get(se); got.Spec.Network != nil || got.Status.Phase != v1alpha1.PhasePending {
		t.Fatalf("spec.network %+v phase %s", got.Spec.Network, got.Status.Phase)
	}
}

func TestDryRunRecordsTheNetwork(t *testing.T) {
	e := newEnv(t, Config{DryRun: true}, 200, nil)
	e.r.CRDs = allCRDs
	se := e.create("acme", "v1.0.0", digestA, haiku)
	e.reconcile(se)
	got := e.get(se)
	if got.Status.Phase != v1alpha1.PhasePlanned || got.Status.Network == nil || !got.Status.Network.Enforced {
		t.Fatalf("status %+v", got.Status)
	}
	if getPolicy(t, e.client, network.PolicyName(se.Name)) != nil {
		t.Fatal("a dry run creates no policy")
	}
}

func TestBaselinePolicyRunnable(t *testing.T) {
	tests := []struct {
		name    string
		crds    crds.Checker
		network network.Policy
		want    bool
	}{
		{name: "cilium present", crds: allCRDs, want: true},
		{name: "no cilium", crds: crds.Static{}, want: false},
		{name: "enforcement off", crds: allCRDs, network: network.Policy{Enforcement: network.EnforcementOff}, want: false},
		{name: "no checker", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Config{}, 0, nil)
			b := &BaselinePolicy{Client: e.client, CRDs: tt.crds, Namespace: ns, Platform: platform.Static(platform.Settings{Network: tt.network})}
			if !b.NeedLeaderElection() {
				t.Fatal("only the leader writes the baseline")
			}
			if err := b.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			pol := getPolicy(t, e.client, network.BaselinePolicyName)
			if (pol != nil) != tt.want {
				t.Fatalf("baseline exists %t, want %t", pol != nil, tt.want)
			}
		})
	}
}
