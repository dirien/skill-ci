package network

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPolicyName(t *testing.T) {
	tests := []struct {
		name, eval, want string
	}{
		{name: "short", eval: "acme-v1-0-0-claude-code-haiku", want: "skillci-net-acme-v1-0-0-claude-code-haiku"},
		{name: "exactly 63", eval: strings.Repeat("a", 51), want: "skillci-net-" + strings.Repeat("a", 51)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PolicyName(tt.eval); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
	long := strings.Repeat("b", 63)
	got := PolicyName(long)
	if len(got) != 63 || !strings.HasPrefix(got, ("skillci-net-" + long)[:52]+"-") {
		t.Fatalf("long name %q (%d chars)", got, len(got))
	}
	if PolicyName(long) == PolicyName(strings.Repeat("b", 62)+"c") {
		t.Fatal("two long names must stay distinct")
	}
}

func specJSON(t *testing.T, obj map[string]any) string {
	t.Helper()
	b, err := json.Marshal(obj["spec"])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const dnsJSON = `{"toEndpoints":[{"matchLabels":{"k8s-app":"kube-dns","k8s:io.kubernetes.pod.namespace":"kube-system"}}],` +
	`"toPorts":[{"ports":[{"port":"53","protocol":"ANY"}],"rules":{"dns":[{"matchPattern":"*"}]}}]}`

func TestEvalPolicy(t *testing.T) {
	selector := `"endpointSelector":{"matchLabels":{"sandbox.users.io/skillci-eval":"e1","sandbox.users.io/skillci-role":"trial"}}`
	tests := []struct {
		name string
		eff  Effective
		want string
	}{
		{
			name: "full adds the world",
			eff:  Effective{Mode: "full"},
			want: `{"egress":[` + dnsJSON + `,{"toEntities":["world"]}],` + selector + `}`,
		},
		{
			name: "none allows the hosts on TCP 443, wildcards as patterns",
			eff:  Effective{Mode: "none", Hosts: []string{"*.example.com", "api.anthropic.com"}},
			want: `{"egress":[` + dnsJSON + `,{"toFQDNs":[{"matchPattern":"*.example.com"},{"matchName":"api.anthropic.com"}],` +
				`"toPorts":[{"ports":[{"port":"443","protocol":"TCP"}]}]}],` + selector + `}`,
		},
		{
			name: "no hosts leaves DNS only",
			eff:  Effective{Mode: "none"},
			want: `{"egress":[` + dnsJSON + `],` + selector + `}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := EvalPolicy("skill-ci", "e1", tt.eff)
			if u.GetAPIVersion() != "cilium.io/v2" || u.GetKind() != "CiliumNetworkPolicy" {
				t.Fatalf("kind %s %s", u.GetAPIVersion(), u.GetKind())
			}
			if u.GetName() != "skillci-net-e1" || u.GetNamespace() != "skill-ci" {
				t.Fatalf("name %s/%s", u.GetNamespace(), u.GetName())
			}
			if l := u.GetLabels(); l[LabelManagedBy] != "skill-ci" || l[LabelEval] != "e1" {
				t.Fatalf("labels %v", l)
			}
			if got := specJSON(t, u.Object); got != tt.want {
				t.Fatalf("spec\n got %s\nwant %s", got, tt.want)
			}
			_ = u.DeepCopy() // panics on a value unstructured cannot copy
		})
	}
}

func TestBaselinePolicy(t *testing.T) {
	u := BaselinePolicy("skill-ci")
	if u.GetName() != BaselinePolicyName || u.GetNamespace() != "skill-ci" || u.GetLabels()[LabelManagedBy] != "skill-ci" {
		t.Fatalf("metadata %s/%s %v", u.GetNamespace(), u.GetName(), u.GetLabels())
	}
	want := `{"egress":[` + dnsJSON + `],"endpointSelector":{"matchLabels":{"skillci.dev/component":"trial-sandbox"}}}`
	if got := specJSON(t, u.Object); got != want {
		t.Fatalf("spec\n got %s\nwant %s", got, want)
	}
	_ = u.DeepCopy()
}
