package platform

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/network"
)

const ns = "skill-ci"

var pins = []string{"claude-code@2.1.292"}

func hosts(h ...string) []v1alpha1.Host {
	out := make([]v1alpha1.Host, len(h))
	for i, s := range h {
		out[i] = v1alpha1.Host(s)
	}
	return out
}

func TestFromSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    v1alpha1.SkillCIConfigSpec
		wantErr string
		check   func(t *testing.T, s Settings)
	}{
		{
			name: "empty spec is the built-in defaults",
			check: func(t *testing.T, s Settings) {
				want := Defaults(pins)
				if s.DefaultSandboxProfile != "default" || s.Network.DefaultMode() != "full" ||
					!slices.Equal(s.Network.AllowedModes(), network.Modes) || s.Network.EnforcementMode() != network.EnforcementAuto ||
					!slices.Equal(BundleNames(s.Network.Bundles), []string{"apt", "github", "node", "python"}) ||
					!slices.Equal(s.Network.InstallHosts, want.Network.InstallHosts) || !slices.Equal(s.Network.Preinstalled, pins) {
					t.Errorf("settings %+v", s)
				}
			},
		},
		{
			name: "every field set",
			spec: v1alpha1.SkillCIConfigSpec{DefaultSandboxProfile: "big.trials", Network: v1alpha1.PlatformNetwork{
				Default: "none", Modes: []string{"none", "allowlist"}, Enforcement: "cilium",
				AlwaysAllow: hosts("proxy.corp.example"), Bundles: map[string]v1alpha1.BundleHosts{"corp": hosts("*.corp.example")},
				AgentInstallHosts: hosts("npm.corp.example"),
			}},
			check: func(t *testing.T, s Settings) {
				p := s.Network
				if s.DefaultSandboxProfile != "big.trials" || p.DefaultMode() != "none" || !slices.Equal(p.Allowed, []string{"none", "allowlist"}) ||
					p.EnforcementMode() != network.EnforcementCilium || !slices.Equal(p.AlwaysAllow, []string{"proxy.corp.example"}) ||
					!slices.Equal(BundleNames(p.Bundles), []string{"corp"}) || !slices.Equal(p.InstallHosts, []string{"npm.corp.example"}) {
					t.Errorf("settings %+v", s)
				}
			},
		},
		{
			name: "explicitly empty bundles and install hosts mean none",
			spec: v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{
				Bundles: map[string]v1alpha1.BundleHosts{}, AgentInstallHosts: []v1alpha1.Host{},
			}},
			check: func(t *testing.T, s Settings) {
				if len(s.Network.Bundles) != 0 || len(s.Network.InstallHosts) != 0 {
					t.Errorf("bundles %v install hosts %v, want none", s.Network.Bundles, s.Network.InstallHosts)
				}
			},
		},
		{
			name:    "default outside the allowed modes",
			spec:    v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{Default: "full", Modes: []string{"none"}}},
			wantErr: `network default "full" is not one of the allowed modes (none)`,
		},
		{
			name:    "unknown mode",
			spec:    v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{Modes: []string{"full", "open"}}},
			wantErr: `network mode "open" is not a mode (none, allowlist, full)`,
		},
		{
			name:    "mode listed twice",
			spec:    v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{Modes: []string{"full", "full"}}},
			wantErr: `network.modes lists "full" twice`,
		},
		{
			name:    "unknown enforcement",
			spec:    v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{Enforcement: "calico"}},
			wantErr: `network enforcement "calico" must be auto, cilium or off`,
		},
		{
			name:    "invalid always-allow host",
			spec:    v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{AlwaysAllow: hosts("Proxy.Example")}},
			wantErr: `invalid network host "Proxy.Example"`,
		},
		{
			name:    "invalid install host",
			spec:    v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{AgentInstallHosts: hosts("localhost")}},
			wantErr: `invalid network host "localhost"`,
		},
		{
			name:    "invalid bundle name",
			spec:    v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{Bundles: map[string]v1alpha1.BundleHosts{"Py": hosts("pypi.org")}}},
			wantErr: `network bundle name "Py" must be lowercase letters, digits and dashes`,
		},
		{
			name:    "invalid bundle host",
			spec:    v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{Bundles: map[string]v1alpha1.BundleHosts{"py": hosts("https://pypi.org")}}},
			wantErr: `network bundle @py: invalid network host "https://pypi.org"`,
		},
		{
			name:    "empty bundle",
			spec:    v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{Bundles: map[string]v1alpha1.BundleHosts{"py": {}}}},
			wantErr: `network bundle @py has no hosts`,
		},
		{
			name: "too many always-allow hosts",
			spec: v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{AlwaysAllow: func() []v1alpha1.Host {
				var h []v1alpha1.Host
				for i := range 21 {
					h = append(h, v1alpha1.Host("h"+strings.Repeat("a", i)+".example"))
				}
				return h
			}()}},
			wantErr: "network host lists hold at most 20 hosts, got 21",
		},
		{
			name: "empty observability and judge are the defaults",
			check: func(t *testing.T, s Settings) {
				if s.HubbleRelay != "" || s.Judge != network.DefaultJudge() {
					t.Errorf("hubble relay %q judge %+v, want none and the default judge", s.HubbleRelay, s.Judge)
				}
			},
		},
		{
			name: "observability and judge set",
			spec: v1alpha1.SkillCIConfigSpec{
				Observability: v1alpha1.Observability{HubbleRelay: "hubble-relay.kube-system.svc.cluster.local:80"},
				Judge:         v1alpha1.Judge{Provider: "openai-compatible", Model: "gpt-oss-120b", BaseURL: "https://llm.example.com/v1", SecretKey: "gateway"},
			},
			check: func(t *testing.T, s Settings) {
				want := network.Judge{Provider: "openai-compatible", Model: "gpt-oss-120b", BaseURL: "https://llm.example.com/v1", SecretKey: "gateway"}
				if s.HubbleRelay != "hubble-relay.kube-system.svc.cluster.local:80" || s.Judge != want {
					t.Errorf("hubble relay %q judge %+v", s.HubbleRelay, s.Judge)
				}
			},
		},
		{
			name: "a judge with only a model keeps the default provider and key",
			spec: v1alpha1.SkillCIConfigSpec{Judge: v1alpha1.Judge{Model: "claude-opus-5-5"}},
			check: func(t *testing.T, s Settings) {
				if s.Judge != (network.Judge{Provider: "anthropic", Model: "claude-opus-5-5", SecretKey: "anthropic"}) {
					t.Errorf("judge %+v", s.Judge)
				}
			},
		},
		{
			name:    "hubble relay with a scheme",
			spec:    v1alpha1.SkillCIConfigSpec{Observability: v1alpha1.Observability{HubbleRelay: "http://hubble-relay:80"}},
			wantErr: `observability.hubbleRelay "http://hubble-relay:80" must be host:port`,
		},
		{
			name:    "judge openai-compatible without base URL",
			spec:    v1alpha1.SkillCIConfigSpec{Judge: v1alpha1.Judge{Provider: "openai-compatible"}},
			wantErr: "invalid judge: judge.baseURL is required for provider openai-compatible",
		},
		{
			name:    "unknown judge provider",
			spec:    v1alpha1.SkillCIConfigSpec{Judge: v1alpha1.Judge{Provider: "gemini"}},
			wantErr: `invalid judge: judge.provider "gemini" must be one of`,
		},
		{
			name:    "invalid default profile",
			spec:    v1alpha1.SkillCIConfigSpec{DefaultSandboxProfile: "Big_Trials"},
			wantErr: `defaultSandboxProfile "Big_Trials" is not a valid object name`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := FromSpec(tt.spec, pins)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, s)
		})
	}
}

func TestDefaultsDoNotShareState(t *testing.T) {
	d := Defaults(pins)
	d.Network.Bundles["python"][0] = "changed.example"
	d.Network.Allowed[0] = "changed"
	if network.DefaultBundles["python"][0] != "pypi.org" || network.Modes[0] != "none" {
		t.Fatal("Defaults returned the package-level slices")
	}
}

func newClient(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) client.WithWatch {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...)
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	return b.Build()
}

func config(name string, spec v1alpha1.SkillCIConfigSpec) *v1alpha1.SkillCIConfig {
	return &v1alpha1.SkillCIConfig{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: spec}
}

var (
	noneOnly = v1alpha1.SkillCIConfigSpec{DefaultSandboxProfile: "small", Network: v1alpha1.PlatformNetwork{Default: "none", Modes: []string{"none"}}}
	invalid  = v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{Default: "full", Modes: []string{"none"}}}
)

// TestPrecedence walks one SkillCIConfig through its life: absent, valid,
// invalid, fixed, deleted. Each step is a fresh lookup through the reader.
func TestPrecedence(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, nil)
	src := &Source{Reader: c, Namespace: ns, Preinstalled: pins}
	key := types.NamespacedName{Namespace: ns, Name: v1alpha1.SkillCIConfigName}
	update := func(spec v1alpha1.SkillCIConfigSpec) {
		t.Helper()
		var cfg v1alpha1.SkillCIConfig
		if err := c.Get(ctx, key, &cfg); err != nil {
			t.Fatal(err)
		}
		cfg.Spec = spec
		cfg.Generation++ // the fake client does not count generations
		if err := c.Update(ctx, &cfg); err != nil {
			t.Fatal(err)
		}
	}

	steps := []struct {
		name        string
		do          func()
		wantSource  string
		wantDefault string
		wantProfile string
		wantInvalid bool
		wantFound   bool
	}{
		{name: "no config: built-in defaults", do: func() {},
			wantSource: v1alpha1.ConfigSourceBuiltIn, wantDefault: "full", wantProfile: "default"},
		{name: "valid config: its values", do: func() {
			cfg := config(v1alpha1.SkillCIConfigName, noneOnly)
			cfg.Generation = 1
			if err := c.Create(ctx, cfg); err != nil {
				t.Fatal(err)
			}
		}, wantSource: v1alpha1.ConfigSourceSkillCIConfig, wantDefault: "none", wantProfile: "small", wantFound: true},
		{name: "invalid edit: the last valid spec stays", do: func() { update(invalid) },
			wantSource: v1alpha1.ConfigSourceLastValid, wantDefault: "none", wantProfile: "small", wantInvalid: true, wantFound: true},
		{name: "fixed: the new spec", do: func() {
			update(v1alpha1.SkillCIConfigSpec{Network: v1alpha1.PlatformNetwork{Default: "allowlist"}})
		}, wantSource: v1alpha1.ConfigSourceSkillCIConfig, wantDefault: "allowlist", wantProfile: "default", wantFound: true},
		{name: "deleted: built-in defaults", do: func() {
			if err := c.Delete(ctx, config(v1alpha1.SkillCIConfigName, v1alpha1.SkillCIConfigSpec{})); err != nil {
				t.Fatal(err)
			}
		}, wantSource: v1alpha1.ConfigSourceBuiltIn, wantDefault: "full", wantProfile: "default"},
		{name: "recreated invalid: built-in defaults, never the deleted object's spec", do: func() {
			cfg := config(v1alpha1.SkillCIConfigName, invalid)
			cfg.Generation = 1
			if err := c.Create(ctx, cfg); err != nil {
				t.Fatal(err)
			}
		}, wantSource: v1alpha1.ConfigSourceBuiltIn, wantDefault: "full", wantProfile: "default", wantInvalid: true, wantFound: true},
	}
	for _, step := range steps {
		step.do()
		st, err := src.Lookup(ctx)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if st.Source != step.wantSource || st.Settings.Network.DefaultMode() != step.wantDefault ||
			st.Settings.DefaultSandboxProfile != step.wantProfile || (st.Invalid != nil) != step.wantInvalid || st.Found != step.wantFound {
			t.Errorf("%s: got source %s default %s profile %s invalid %v found %t", step.name,
				st.Source, st.Settings.Network.DefaultMode(), st.Settings.DefaultSandboxProfile, st.Invalid, st.Found)
		}
	}
}

func TestEvaluateIgnoresStaleCopies(t *testing.T) {
	src := &Source{Namespace: ns}
	newer := config(v1alpha1.SkillCIConfigName, noneOnly)
	newer.UID, newer.Generation = "u1", 3
	older := config(v1alpha1.SkillCIConfigName, v1alpha1.SkillCIConfigSpec{})
	older.UID, older.Generation = "u1", 2
	src.Evaluate(newer)
	src.Evaluate(older) // a cache that lags behind
	bad := config(v1alpha1.SkillCIConfigName, invalid)
	bad.UID, bad.Generation = "u1", 4
	st := src.Evaluate(bad)
	if st.Source != v1alpha1.ConfigSourceLastValid || st.Generation != 3 || st.Settings.Network.DefaultMode() != "none" {
		t.Errorf("state %+v, want the last valid generation 3", st)
	}
}

func TestLookupWithoutReader(t *testing.T) {
	var nilSource *Source
	for name, src := range map[string]*Source{
		"nil source":    nilSource,
		"no reader":     {Namespace: ns, Preinstalled: pins},
		"static source": Static(Settings{DefaultSandboxProfile: "fixed"}),
	} {
		st, err := src.Lookup(context.Background())
		if err != nil || st.Source != v1alpha1.ConfigSourceBuiltIn {
			t.Errorf("%s: %+v %v", name, st, err)
		}
		if name == "static source" && st.Settings.DefaultSandboxProfile != "fixed" {
			t.Errorf("static settings not returned: %+v", st.Settings)
		}
		if name == "nil source" && !slices.Equal(st.Settings.Network.Preinstalled, network.DefaultPreinstalledAgents) {
			t.Errorf("nil source preinstalled %v", st.Settings.Network.Preinstalled)
		}
	}
}

func TestLookupReadError(t *testing.T) {
	boom := errors.New("boom")
	c := newClient(t, &interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		return boom
	}})
	st, err := (&Source{Reader: c, Namespace: ns}).Lookup(context.Background())
	if !errors.Is(err, boom) || st.Source != v1alpha1.ConfigSourceBuiltIn {
		t.Errorf("state %+v error %v, want the read error and the built-in defaults", st, err)
	}
}

func TestEffective(t *testing.T) {
	s, err := FromSpec(noneOnly, pins)
	if err != nil {
		t.Fatal(err)
	}
	eff := Effective(State{Settings: s, Source: v1alpha1.ConfigSourceSkillCIConfig, Generation: 2}, EnforcedBy(s.Network, true))
	n := eff.Network
	if eff.Source != v1alpha1.ConfigSourceSkillCIConfig || eff.Generation != 2 || eff.DefaultSandboxProfile != "small" ||
		n.Default != "none" || !slices.Equal(n.Modes, []string{"none"}) || n.Enforcement != "auto" || n.EnforcedBy != "cilium" ||
		!slices.Equal(n.Bundles, []string{"apt", "github", "node", "python"}) || !slices.Equal(n.AgentInstallHosts, []string{"registry.npmjs.org"}) {
		t.Errorf("effective %+v", eff)
	}
	if eff.Observability.HubbleRelay != "" || eff.Judge != (v1alpha1.EffectiveJudge{Provider: "anthropic", SecretKey: "anthropic", Host: "api.anthropic.com"}) {
		t.Errorf("effective observability %+v judge %+v, want the defaults", eff.Observability, eff.Judge)
	}
	if got := EnforcedBy(network.Policy{Enforcement: network.EnforcementOff}, true); got != v1alpha1.EnforcedByNone {
		t.Errorf("enforcement off resolves to %s", got)
	}
	if got := EnforcedBy(network.Policy{}, false); got != v1alpha1.EnforcedByNone {
		t.Errorf("auto without Cilium resolves to %s", got)
	}
}

func TestEffectiveJudge(t *testing.T) {
	tests := []struct {
		name  string
		judge network.Judge
		want  v1alpha1.EffectiveJudge
	}{
		{name: "zero value", want: v1alpha1.EffectiveJudge{Provider: "anthropic", SecretKey: "anthropic", Host: "api.anthropic.com"}},
		{name: "gateway", judge: network.Judge{Provider: "openai-compatible", BaseURL: "https://llm.example.com/v1", SecretKey: "gw"},
			want: v1alpha1.EffectiveJudge{Provider: "openai-compatible", BaseURL: "https://llm.example.com/v1", SecretKey: "gw", Host: "llm.example.com"}},
		{name: "nv_build", judge: network.Judge{Provider: "nv_build", Model: "nvidia/judge", SecretKey: "nvidia"},
			want: v1alpha1.EffectiveJudge{Provider: "nv_build", Model: "nvidia/judge", SecretKey: "nvidia", Host: "integrate.api.nvidia.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveJudge(tt.judge); got != tt.want {
				t.Errorf("EffectiveJudge() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidateHubbleRelay(t *testing.T) {
	tests := []struct {
		addr string
		ok   bool
	}{
		{"", true},
		{"hubble-relay.kube-system.svc.cluster.local:80", true},
		{"hubble-relay:4245", true},
		{"10.0.0.7:4245", true},
		{"[fd00::7]:4245", true},
		{"hubble-relay.kube-system.svc.cluster.local", false},
		{"http://hubble-relay:80", false},
		{"hubble-relay:0", false},
		{"hubble-relay:65536", false},
		{"hubble-relay:080", false},
		{"Hubble-Relay:80", false},
		{"fd00::7:4245", false},
		{"[10.0.0.7]:4245", false},
		{"[hubble-relay]:80", false},
		{strings.Repeat("a", 250) + ".io:80", false},
	}
	for _, tt := range tests {
		if err := ValidateHubbleRelay(tt.addr); (err == nil) != tt.ok {
			t.Errorf("ValidateHubbleRelay(%q) = %v, want ok=%v", tt.addr, err, tt.ok)
		}
	}
}
