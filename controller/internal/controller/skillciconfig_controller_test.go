package controller

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/crds"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/models"
	"github.com/dirien/skill-ci/controller/internal/network"
	"github.com/dirien/skill-ci/controller/internal/platform"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/profiles"
	"github.com/dirien/skill-ci/controller/internal/runner"
)

func skillCIConfig(name string, spec v1alpha1.SkillCIConfigSpec) *v1alpha1.SkillCIConfig {
	return &v1alpha1.SkillCIConfig{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1}, Spec: spec}
}

func netSpec(def string, modes []string, enforcement string) v1alpha1.SkillCIConfigSpec {
	return v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{Default: def, Modes: modes, Enforcement: enforcement}}
}

func getConfig(t *testing.T, c client.Client, name string) *v1alpha1.SkillCIConfig {
	t.Helper()
	var got v1alpha1.SkillCIConfig
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	return &got
}

func drain(r *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-r.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestSkillCIConfigReconciler(t *testing.T) {
	tests := []struct {
		name       string
		obj        *v1alpha1.SkillCIConfig
		crds       crds.Checker
		wantStatus metav1.ConditionStatus
		wantReason string
		wantMsg    string
		wantSource string // "" means no status.effective
		wantBy     string
		wantEvent  string
	}{
		{
			name: "valid with Cilium", obj: skillCIConfig("default", netSpec("none", []string{"none", "allowlist"}, "")), crds: allCRDs,
			wantStatus: metav1.ConditionTrue, wantReason: ReasonConfigApplied,
			wantMsg:    "applied: default network none, allowed modes none, allowlist, enforcement auto (cilium), bundles @apt, @github, @node, @python, default sandbox profile default",
			wantSource: v1alpha1.ConfigSourceSkillCIConfig, wantBy: v1alpha1.EnforcedByCilium, wantEvent: "Normal Applied",
		},
		{
			name: "valid without Cilium", obj: skillCIConfig("default", v1alpha1.SkillCIConfigSpec{}), crds: crds.Static{},
			wantStatus: metav1.ConditionTrue, wantReason: ReasonConfigApplied, wantMsg: "enforcement auto (none)",
			wantSource: v1alpha1.ConfigSourceSkillCIConfig, wantBy: v1alpha1.EnforcedByNone, wantEvent: "Normal Applied",
		},
		{
			name: "cilium required but missing", obj: skillCIConfig("default", netSpec("", nil, "cilium")), crds: crds.Static{},
			wantStatus: metav1.ConditionTrue, wantReason: ReasonConfigApplied, wantMsg: "every run fails until Cilium is installed",
			wantSource: v1alpha1.ConfigSourceSkillCIConfig, wantBy: v1alpha1.EnforcedByCilium, wantEvent: "Normal Applied",
		},
		{
			name: "invalid and never valid", obj: skillCIConfig("default", netSpec("full", []string{"none"}, "")), crds: allCRDs,
			wantStatus: metav1.ConditionFalse, wantReason: ReasonConfigInvalid,
			wantMsg:    `invalid spec: invalid network settings: network default "full" is not one of the allowed modes (none); the controller uses the built-in defaults`,
			wantSource: v1alpha1.ConfigSourceBuiltIn, wantBy: v1alpha1.EnforcedByCilium, wantEvent: "Warning Invalid",
		},
		{
			name: "another name is ignored", obj: skillCIConfig("staging", netSpec("none", nil, "")), crds: allCRDs,
			wantStatus: metav1.ConditionFalse, wantReason: ReasonConfigIgnored,
			wantMsg:   "ignored: the controller reads only the SkillCIConfig named default in namespace skill-ci",
			wantEvent: "Warning Ignored",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Config{}, 0, nil, tt.obj)
			rec := events.NewFakeRecorder(10)
			r := &SkillCIConfigReconciler{Client: e.client, Recorder: rec, CRDs: tt.crds, Platform: &platform.Source{Reader: e.client, Namespace: ns}}
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tt.obj)}
			if _, err := r.Reconcile(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			got := getConfig(t, e.client, tt.obj.Name)
			c := findCondition(got.Status.Conditions, ConditionReady)
			if c == nil || c.Status != tt.wantStatus || c.Reason != tt.wantReason || !strings.Contains(c.Message, tt.wantMsg) || c.ObservedGeneration != 1 {
				t.Fatalf("Ready %+v, want %s %s %q", c, tt.wantStatus, tt.wantReason, tt.wantMsg)
			}
			if got.Status.ObservedGeneration != 1 {
				t.Errorf("observedGeneration %d", got.Status.ObservedGeneration)
			}
			switch eff := got.Status.Effective; {
			case tt.wantSource == "" && eff != nil:
				t.Errorf("an ignored config has status.effective %+v", eff)
			case tt.wantSource != "" && (eff == nil || eff.Source != tt.wantSource || eff.Network.EnforcedBy != tt.wantBy):
				t.Errorf("status.effective %+v, want source %s enforcedBy %s", eff, tt.wantSource, tt.wantBy)
			}
			if evs := drain(rec); len(evs) != 1 || !strings.HasPrefix(evs[0], tt.wantEvent) {
				t.Errorf("events %q, want one %q", evs, tt.wantEvent)
			}

			// A second pass changes nothing and stays quiet.
			before := got.ResourceVersion
			if _, err := r.Reconcile(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if got := getConfig(t, e.client, tt.obj.Name); got.ResourceVersion != before {
				t.Error("a second reconcile rewrote the status")
			}
			if evs := drain(rec); len(evs) != 0 {
				t.Errorf("second reconcile emitted %q", evs)
			}
		})
	}
}

func TestSkillCIConfigInvalidEditKeepsLastValid(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Config{}, 0, nil, skillCIConfig("default", netSpec("none", []string{"none"}, "")))
	src := &platform.Source{Reader: e.client, Namespace: ns}
	r := &SkillCIConfigReconciler{Client: e.client, Recorder: events.NewFakeRecorder(10), CRDs: allCRDs, Platform: src}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "default"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}

	cfg := getConfig(t, e.client, "default")
	cfg.Spec.Network.Default, cfg.Generation = "full", 2 // full is not in modes
	if err := e.client.Update(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	got := getConfig(t, e.client, "default")
	c := findCondition(got.Status.Conditions, ConditionReady)
	if c == nil || c.Status != metav1.ConditionFalse || !strings.HasSuffix(c.Message, "the controller keeps the last valid spec (generation 1)") {
		t.Fatalf("Ready %+v", c)
	}
	if eff := got.Status.Effective; eff == nil || eff.Source != v1alpha1.ConfigSourceLastValid || eff.Generation != 1 || eff.Network.Default != "none" {
		t.Errorf("status.effective %+v, want the last valid spec", eff)
	}
	if s, err := src.Current(ctx); err != nil || s.Network.DefaultMode() != "none" {
		t.Errorf("runs resolve with default %q (%v), want the last valid none", s.Network.DefaultMode(), err)
	}

	// Deleting the config brings back the built-in defaults.
	if err := e.client.Delete(ctx, got); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if s, err := src.Current(ctx); err != nil || s.Network.DefaultMode() != "full" {
		t.Errorf("after delete default %q (%v), want full", s.Network.DefaultMode(), err)
	}
}

// TestConfigChangeAppliesToNextRun changes the SkillCIConfig between two
// runs without restarting anything: the run resolved before keeps its
// network, the next one gets the new settings, and a Pending run whose mode
// was recorded earlier is checked against the modes allowed now.
func TestConfigChangeAppliesToNextRun(t *testing.T) {
	ctx := context.Background()
	sonnet := models.Model{Agent: "claude-code", Model: "claude-sonnet-5-5", Required: true}
	opus := models.Model{Agent: "claude-code", Model: "claude-opus-5-5", Required: true}
	cfg := Config{MaxConcurrentJobs: 4, Thresholds: policy.DefaultThresholds()}
	e := newEnv(t, cfg, 200, map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}})
	e.r.CRDs = allCRDs
	e.r.Config.Platform = &platform.Source{Reader: e.client, Namespace: ns}

	first := e.create("acme", "v1.0.0", digestA, haiku)
	e.reconcile(first)
	if got := e.get(first); got.Spec.Network == nil || got.Spec.Network.Mode != "full" || got.Status.Phase != v1alpha1.PhaseRunning {
		t.Fatalf("without a config: %+v phase %s, want full and Running", got.Spec.Network, got.Status.Phase)
	}

	if err := e.client.Create(ctx, skillCIConfig("default", netSpec("none", []string{"none"}, ""))); err != nil {
		t.Fatal(err)
	}
	second := e.create("acme", "v1.0.0", digestA, sonnet)
	e.reconcile(second)
	got := e.get(second)
	if got.Spec.Network == nil || got.Spec.Network.Mode != "none" || got.Spec.Network.Source != v1alpha1.NetworkSourceDefault || got.Status.Phase != v1alpha1.PhaseRunning {
		t.Fatalf("after the change: %+v phase %s %q, want none from the default and Running", got.Spec.Network, got.Status.Phase, got.Status.Message)
	}
	if pol := getPolicy(t, e.client, network.PolicyName(second.Name)); pol == nil {
		t.Error("the none run has no CiliumNetworkPolicy")
	}
	if got := e.get(first); got.Spec.Network.Mode != "full" {
		t.Errorf("the earlier run changed to %s", got.Spec.Network.Mode)
	}

	// A run whose spec.network says full, resolved now, is not allowed any more.
	third := e.create("acme", "v1.0.0", digestA, opus)
	third.Spec.Network = &v1alpha1.NetworkSpec{Mode: "full", Source: v1alpha1.NetworkSourceSkill}
	if err := e.client.Update(ctx, third); err != nil {
		t.Fatal(err)
	}
	e.reconcile(third)
	if got := e.get(third); got.Status.Phase != v1alpha1.PhaseFailed ||
		got.Status.Message != `network mode "full" is not allowed on this platform (allowed: none)` {
		t.Errorf("phase %s %q, want Failed NotAllowed", got.Status.Phase, got.Status.Message)
	}
}

// TestProfilesFollowConfig shows a SkillCIConfig change re-reconciling every
// profile, which flips the template's networkPolicyManagement.
func TestProfilesFollowConfig(t *testing.T) {
	ctx := context.Background()
	objs := []client.Object{
		&v1alpha1.SandboxProfile{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns}},
		&v1alpha1.SandboxProfile{ObjectMeta: metav1.ObjectMeta{Name: "big", Namespace: ns}},
	}
	e := newEnv(t, Config{}, 0, nil, objs...)
	cfg := Config{Namespace: ns, Platform: &platform.Source{Reader: e.client, Namespace: ns}, Runner: runner.Config{SandboxImage: "sandbox:1"}}
	r := &SandboxProfileReconciler{Client: e.client, CRDs: allCRDs, Config: cfg, WatchConfig: true}
	mgmt := func() string {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "big"}}); err != nil {
			t.Fatal(err)
		}
		m, _, _ := unstructured.NestedString(getObject(t, e.client, profiles.TemplateGVK, "big").Object, "spec", "networkPolicyManagement")
		return m
	}
	if m := mgmt(); m != profiles.NetworkUnmanaged {
		t.Fatalf("with Cilium and no config: %q", m)
	}

	off := skillCIConfig("default", netSpec("", nil, "off"))
	if err := e.client.Create(ctx, off); err != nil {
		t.Fatal(err)
	}
	reqs := r.allProfiles(ctx, off)
	if len(reqs) != 2 {
		t.Fatalf("a config change enqueues %v, want both profiles", reqs)
	}
	if got := r.allProfiles(ctx, skillCIConfig("staging", v1alpha1.SkillCIConfigSpec{})); len(got) != 0 {
		t.Errorf("an ignored config enqueues %v", got)
	}
	if m := mgmt(); m != profiles.NetworkManaged {
		t.Errorf("with enforcement off: %q, want Managed", m)
	}
}
