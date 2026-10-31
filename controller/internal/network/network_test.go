package network

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
)

func defaultPolicy() Policy {
	return Policy{
		Bundles:      DefaultBundles,
		Preinstalled: DefaultPreinstalledAgents,
		InstallHosts: DefaultAgentInstallHosts,
	}
}

var claude = Entry{Agent: "claude-code", AgentVersion: "2.1.292", Model: "claude-opus-5-5"}

func spec(mode string, allow ...string) *v1alpha1.NetworkSpec {
	return &v1alpha1.NetworkSpec{Mode: mode, Allow: allow, Source: v1alpha1.NetworkSourceSkill}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		policy  func(*Policy)
		in      Input
		want    Effective
		reason  string
		message string
	}{
		{
			name: "no declaration gets the platform default full, unenforced without cilium",
			in:   Input{Entry: claude},
			want: Effective{Mode: "full", Source: "default"},
		},
		{
			name: "full with cilium is enforced and has no hosts",
			in:   Input{Spec: spec("full"), Entry: claude, CiliumCRD: true},
			want: Effective{Mode: "full", Source: "skill", Cilium: true},
		},
		{
			name:   "platform default none applies to a run without a declaration",
			policy: func(p *Policy) { p.Default = "none" },
			in:     Input{Entry: claude, CiliumCRD: true},
			want:   Effective{Mode: "none", Source: "default", Hosts: []string{"api.anthropic.com"}, Cilium: true},
		},
		{
			name: "none allows the model host only",
			in:   Input{Spec: spec("none", "pypi.org"), Entry: claude, CiliumCRD: true},
			want: Effective{Mode: "none", Source: "skill", Hosts: []string{"api.anthropic.com"}, Cilium: true,
				Note: "skillci.dev/network-allow ignored: it is used only with mode allowlist"},
		},
		{
			name: "allow next to full is ignored with a note",
			in:   Input{Spec: spec("full", "pypi.org"), Entry: claude, CiliumCRD: true},
			want: Effective{Mode: "full", Source: "skill", Cilium: true,
				Note: "skillci.dev/network-allow ignored: it is used only with mode allowlist"},
		},
		{
			name: "allowlist expands bundles, adds the model host, sorts and dedupes",
			in:   Input{Spec: spec("allowlist", "@python", "@github", "dl.k8s.io", "pypi.org"), Entry: claude, CiliumCRD: true},
			want: Effective{Mode: "allowlist", Source: "skill", Cilium: true, Hosts: []string{
				"api.anthropic.com", "api.github.com", "codeload.github.com", "dl.k8s.io", "files.pythonhosted.org",
				"github.com", "objects.githubusercontent.com", "pypi.org",
			}},
		},
		{
			name: "allowlist keeps wildcard hosts",
			in:   Input{Spec: spec("allowlist", "*.example.com"), Entry: claude, CiliumCRD: true},
			want: Effective{Mode: "allowlist", Source: "skill", Cilium: true, Hosts: []string{"*.example.com", "api.anthropic.com"}},
		},
		{
			name:   "always-allow hosts are added to none",
			policy: func(p *Policy) { p.AlwaysAllow = []string{"proxy.corp.example"} },
			in:     Input{Spec: spec("none"), Entry: claude, CiliumCRD: true},
			want:   Effective{Mode: "none", Source: "skill", Cilium: true, Hosts: []string{"api.anthropic.com", "proxy.corp.example"}},
		},
		{
			name: "an agent version that is not preinstalled adds the install hosts",
			in:   Input{Spec: spec("none"), Entry: Entry{Agent: "claude-code", AgentVersion: "2.1.308", Model: "claude-opus-5-5"}, CiliumCRD: true},
			want: Effective{Mode: "none", Source: "skill", Cilium: true, Hosts: []string{"api.anthropic.com", "registry.npmjs.org"}},
		},
		{
			name: "an unpinned entry runs the preinstalled default",
			in:   Input{Spec: spec("none"), Entry: Entry{Agent: "codex", Model: "openai/gpt-6.1-sol"}, CiliumCRD: true},
			want: Effective{Mode: "none", Source: "skill", Cilium: true, Hosts: []string{"api.openai.com"}},
		},
		{
			name: "codex uses the host of openai-base-url",
			in: Input{Spec: spec("none"), Entry: Entry{Agent: "codex", AgentVersion: "0.161.0", Model: "openai/gpt-6.1-sol"},
				OpenAIBaseURL: "https://Gateway.Example.com/v1", CiliumCRD: true},
			want: Effective{Mode: "none", Source: "skill", Cilium: true, Hosts: []string{"gateway.example.com"}},
		},
		{
			name:   "enforcement off runs none unenforced and still reports the hosts",
			policy: func(p *Policy) { p.Enforcement = EnforcementOff },
			in:     Input{Spec: spec("none"), Entry: claude, CiliumCRD: true},
			want:   Effective{Mode: "none", Source: "skill", Hosts: []string{"api.anthropic.com"}},
		},
		{
			name:   "enforcement cilium enforces even before the CRD check passes elsewhere",
			policy: func(p *Policy) { p.Enforcement = EnforcementCilium },
			in:     Input{Spec: spec("full"), Entry: claude, CiliumCRD: true},
			want:   Effective{Mode: "full", Source: "skill", Cilium: true},
		},
		{
			name:    "a mode outside the allowed set fails",
			policy:  func(p *Policy) { p.Allowed = []string{"none", "allowlist"} },
			in:      Input{Spec: spec("full"), Entry: claude, CiliumCRD: true},
			reason:  ReasonNotAllowed,
			message: `network mode "full" is not allowed on this platform (allowed: none, allowlist)`,
		},
		{
			name:    "an unknown mode is not allowed",
			in:      Input{Spec: spec("open"), Entry: claude, CiliumCRD: true},
			reason:  ReasonNotAllowed,
			message: `network mode "open" is not allowed on this platform (allowed: none, allowlist, full)`,
		},
		{
			name:    "none without cilium fails under auto",
			in:      Input{Spec: spec("none"), Entry: claude},
			reason:  ReasonNoCilium,
			message: `network mode "none" needs Cilium (CiliumNetworkPolicy CRD not found); this cluster can only run full`,
		},
		{
			name:    "allowlist without cilium fails under auto",
			in:      Input{Spec: spec("allowlist", "@python"), Entry: claude},
			reason:  ReasonNoCilium,
			message: `network mode "allowlist" needs Cilium (CiliumNetworkPolicy CRD not found); this cluster can only run full`,
		},
		{
			name:    "enforcement cilium without the CRD fails every mode",
			policy:  func(p *Policy) { p.Enforcement = EnforcementCilium },
			in:      Input{Spec: spec("full"), Entry: claude},
			reason:  ReasonNoCilium,
			message: "network enforcement is cilium, but the CiliumNetworkPolicy CRD was not found",
		},
		{
			name:    "an unknown bundle fails and lists the defined ones",
			in:      Input{Spec: spec("allowlist", "@rust"), Entry: claude, CiliumCRD: true},
			reason:  ReasonUnknownBundle,
			message: `unknown network bundle "@rust" (defined: @apt, @github, @node, @python)`,
		},
		{
			name:    "a malformed host fails",
			in:      Input{Spec: spec("allowlist", "PyPI.org"), Entry: claude, CiliumCRD: true},
			reason:  ReasonInvalidAllowlist,
			message: `invalid network host "PyPI.org": must be a lowercase RFC 1123 hostname, optionally starting with "*."`,
		},
		{
			name: "more than 20 hosts after expansion fail",
			in: Input{Spec: spec("allowlist", "@python", "@github", "@apt", "@node",
				"a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com", "f.example.com",
				"g.example.com", "h.example.com", "i.example.com", "j.example.com", "k.example.com",
				"l.example.com", "m.example.com", "n.example.com", "o.example.com"), Entry: claude, CiliumCRD: true},
			reason:  ReasonInvalidAllowlist,
			message: "network allowlist has 24 hosts after bundle expansion, at most 20 are allowed",
		},
		{
			name:    "an unknown model provider fails with none",
			in:      Input{Spec: spec("none"), Entry: Entry{Agent: "opencode", Model: "mistral/large"}, CiliumCRD: true},
			reason:  ReasonUnknownModelHost,
			message: `no model API host is known for agent "opencode" with model "mistral/large", so it cannot run with network none or allowlist`,
		},
		{
			name: "an unknown model provider runs with full",
			in:   Input{Spec: spec("full"), Entry: Entry{Agent: "opencode", Model: "mistral/large"}, CiliumCRD: true},
			want: Effective{Mode: "full", Source: "skill", Cilium: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := defaultPolicy()
			if tt.policy != nil {
				tt.policy(&p)
			}
			got, err := Resolve(p, tt.in)
			if tt.reason != "" {
				var nerr *Error
				if !errors.As(err, &nerr) {
					t.Fatalf("want a network error, got %v (effective %+v)", err, got)
				}
				if nerr.Reason != tt.reason || nerr.Message != tt.message {
					t.Fatalf("got %s %q, want %s %q", nerr.Reason, nerr.Message, tt.reason, tt.message)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestModelHosts(t *testing.T) {
	tests := []struct {
		entry   Entry
		baseURL string
		want    string
		wantErr string
	}{
		{entry: Entry{Agent: "claude-code", Model: "claude-haiku-5-5"}, want: "api.anthropic.com"},
		{entry: Entry{Agent: "codex", Model: "openai/gpt-6.1-sol"}, want: "api.openai.com"},
		{entry: Entry{Agent: "codex", Model: "openai/gpt-6.1-sol"}, baseURL: "http://litellm.ai.svc:4000/v1", want: "litellm.ai.svc"},
		{entry: Entry{Agent: "codex", Model: "openai/x"}, baseURL: "not a url", wantErr: `llm-keys openai-base-url "not a url" has no host`},
		{entry: Entry{Agent: "opencode", Model: "anthropic/claude-haiku-5-5"}, want: "api.anthropic.com"},
		{entry: Entry{Agent: "opencode", Model: "openai/gpt-6.1-sol"}, want: "api.openai.com"},
		{entry: Entry{Agent: "opencode", Model: "openrouter/qwen/qwen3"}, want: "openrouter.ai"},
		{entry: Entry{Agent: "opencode", Model: "ollama-cloud/gpt-oss:120b"}, want: "ollama.com"},
		{entry: Entry{Agent: "opencode", Model: "nvidia/nemotron"}, want: "integrate.api.nvidia.com"},
		{entry: Entry{Agent: "aider", Model: "x"}, wantErr: `no model API host is known for agent "aider"`},
	}
	for _, tt := range tests {
		t.Run(tt.entry.Agent+"/"+tt.entry.Model, func(t *testing.T) {
			got, err := ModelHosts(tt.entry, tt.baseURL)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("got %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || len(got) != 1 || got[0] != tt.want {
				t.Fatalf("got %v, %v; want %s", got, err, tt.want)
			}
		})
	}
}

func TestNeedsOpenAIBaseURL(t *testing.T) {
	tests := []struct {
		entry Entry
		want  bool
	}{
		{Entry{Agent: "codex", Model: "openai/x"}, true},
		{Entry{Agent: "opencode", Model: "openai/x"}, true},
		{Entry{Agent: "opencode", Model: "anthropic/x"}, false},
		{Entry{Agent: "claude-code", Model: "x"}, false},
	}
	for _, tt := range tests {
		if got := NeedsOpenAIBaseURL(tt.entry); got != tt.want {
			t.Errorf("%+v: got %t, want %t", tt.entry, got, tt.want)
		}
	}
}

func TestDeclared(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		def         string
		want        *v1alpha1.NetworkSpec
	}{
		{name: "nothing declared", want: &v1alpha1.NetworkSpec{Mode: "full", Source: "default"}},
		{name: "nothing declared, default none", def: "none", want: &v1alpha1.NetworkSpec{Mode: "none", Source: "default"}},
		{
			name:        "mode and allow",
			annotations: map[string]string{AnnotationMode: " Allowlist ", AnnotationAllow: "@python, ,dl.k8s.io"},
			want:        &v1alpha1.NetworkSpec{Mode: "allowlist", Allow: []string{"@python", "dl.k8s.io"}, Source: "skill"},
		},
		{
			name:        "allow without a mode keeps the default mode",
			annotations: map[string]string{AnnotationAllow: "pypi.org"},
			want:        &v1alpha1.NetworkSpec{Mode: "full", Allow: []string{"pypi.org"}, Source: "default"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Declared(tt.annotations, Policy{Default: tt.def})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidateHost(t *testing.T) {
	tests := []struct {
		host string
		ok   bool
	}{
		{"pypi.org", true},
		{"*.githubusercontent.com", true},
		{"a-b.c1.example", true},
		{"localhost", false},
		{"PyPI.org", false},
		{"*.", false},
		{"**.example.com", false},
		{"foo.*.example.com", false},
		{"-bad.example.com", false},
		{"bad-.example.com", false},
		{"https://pypi.org", false},
		{"pypi.org:443", false},
		{"", false},
		{strings.Repeat("a", 64) + ".com", false},
	}
	for _, tt := range tests {
		if err := ValidateHost(tt.host); (err == nil) != tt.ok {
			t.Errorf("%q: got %v, want ok=%t", tt.host, err, tt.ok)
		}
	}
}

func TestPolicyValidate(t *testing.T) {
	tests := []struct {
		name    string
		policy  Policy
		wantErr string
	}{
		{name: "defaults", policy: defaultPolicy()},
		{name: "unknown default", policy: Policy{Default: "open"}, wantErr: `network default "open" is not a mode`},
		{name: "unknown allowed mode", policy: Policy{Allowed: []string{"none", "open"}}, wantErr: `network mode "open" is not a mode`},
		{name: "default not allowed", policy: Policy{Default: "full", Allowed: []string{"none"}}, wantErr: `network default "full" is not one of the allowed modes (none)`},
		{name: "bad enforcement", policy: Policy{Enforcement: "calico"}, wantErr: `network enforcement "calico" must be auto, cilium or off`},
		{name: "bad always-allow host", policy: Policy{AlwaysAllow: []string{"Proxy"}}, wantErr: `invalid network host "Proxy"`},
		{name: "bad bundle name", policy: Policy{Bundles: map[string][]string{"Py": {"pypi.org"}}}, wantErr: `network bundle name "Py"`},
		{name: "bad bundle host", policy: Policy{Bundles: map[string][]string{"py": {"pypi"}}}, wantErr: `network bundle @py: invalid network host "pypi"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.policy.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestPolicyCilium(t *testing.T) {
	tests := []struct {
		enforcement Enforcement
		crd, want   bool
	}{
		{"", true, true}, {"", false, false},
		{EnforcementAuto, true, true}, {EnforcementAuto, false, false},
		{EnforcementCilium, false, true}, {EnforcementCilium, true, true},
		{EnforcementOff, true, false}, {EnforcementOff, false, false},
	}
	for _, tt := range tests {
		if got := (Policy{Enforcement: tt.enforcement}).Cilium(tt.crd); got != tt.want {
			t.Errorf("%q crd=%t: got %t, want %t", tt.enforcement, tt.crd, got, tt.want)
		}
	}
}
