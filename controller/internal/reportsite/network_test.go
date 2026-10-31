package reportsite

import (
	"html"
	"net/http"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
)

const netSkill = "net-skill"

// networkSpecs are runs of one skill with every network outcome.
func networkSpecs() []spec {
	base := func(version, model string, phase v1alpha1.Phase) spec {
		s := ok(netSkill, version, model, true, v1alpha1.TriggerPush, at(14, 0), "0.90", "0.40", "+0.50", "Helps")
		s.phase = phase
		return s
	}
	enforced := base("v1.0.0", haiku, v1alpha1.PhaseSucceeded)
	enforced.network = &v1alpha1.NetworkSpec{Mode: "allowlist", Allow: []string{"@python", "dl.k8s.io"}, Source: "skill"}
	enforced.netStatus = &v1alpha1.NetworkStatus{Mode: "allowlist", Enforced: true, Policy: "skillci-net-x",
		Hosts: []string{"api.anthropic.com", "dl.k8s.io", "files.pythonhosted.org", "pypi.org"}}
	enforced.netCond = &metav1.Condition{Type: "NetworkPolicy", Status: metav1.ConditionTrue, Reason: "Enforced",
		Message: "mode allowlist (skill): CiliumNetworkPolicy skillci-net-x allows 4 host(s) on TCP 443"}
	enforced.profile, enforced.warmPool = "default", "default"

	unenforced := base("v1.0.0", sonnet, v1alpha1.PhaseSucceeded)
	unenforced.network = &v1alpha1.NetworkSpec{Mode: "full", Source: "default"}
	unenforced.netStatus = &v1alpha1.NetworkStatus{Mode: "full"}
	unenforced.netCond = &metav1.Condition{Type: "NetworkPolicy", Status: metav1.ConditionFalse, Reason: "NotEnforced",
		Message: "mode full (default) is not enforced by skill-ci: the CiliumNetworkPolicy CRD was not found"}
	unenforced.profile = "default"

	refused := spec{skill: netSkill, version: "v1.1.0", model: haiku, required: true, trigger: v1alpha1.TriggerPush,
		started: at(14, 10), took: time.Second, phase: v1alpha1.PhaseFailed,
		message: `network mode "none" needs Cilium (CiliumNetworkPolicy CRD not found); this cluster can only run full`}
	refused.network = &v1alpha1.NetworkSpec{Mode: "none", Source: "skill"}
	refused.netStatus = &v1alpha1.NetworkStatus{Mode: "none"}
	refused.netCond = &metav1.Condition{Type: "NetworkPolicy", Status: metav1.ConditionFalse, Reason: "NoCilium", Message: refused.message}

	gate := spec{skill: netSkill, version: "v1.1.0", model: sonnet, required: true, trigger: v1alpha1.TriggerPush,
		started: at(14, 10), took: time.Minute, phase: v1alpha1.PhaseFailed, message: "no eval dataset: add evals/evals.json",
		tiers: [3]string{"passed", "passed", "skipped"}, findings: []string{"tier1/quality: description is short"}}

	legacy := base("v0.9.0", haiku, v1alpha1.PhaseSucceeded)
	pending := spec{skill: netSkill, version: "v1.2.0", model: haiku, required: true, trigger: v1alpha1.TriggerPush,
		started: at(14, 20), phase: v1alpha1.PhasePending}
	return []spec{enforced, unenforced, refused, gate, legacy, pending}
}

func TestRunPageNetwork(t *testing.T) {
	h := newHarnessWith(t, networkSpecs(), harbor.NewFake(map[string][]harbor.Artifact{}))
	tests := []struct {
		name   string
		path   string
		want   []string
		absent []string
	}{
		{name: "enforced allowlist", path: runPath(netSkill, "v1.0.0", haiku), want: []string{
			"<h2 id=\"network-h\">Network</h2>", "<code>allowlist</code>", "(declared by the skill)",
			"The model API hosts, the judge and the hosts the skill declares.", "@python, dl.k8s.io",
			"api.anthropic.com, dl.k8s.io, files.pythonhosted.org, pypi.org", "(TCP 443)",
			"CiliumNetworkPolicy <code>skillci-net-x</code>", "SandboxProfile default, trial pods claimed from warm pool default",
		}},
		{name: "full without cilium", path: runPath(netSkill, "v1.0.0", sonnet), want: []string{
			"<code>full</code>", "(platform default, the skill declares none)", "The internet, not the cluster.",
			"not enforced by skill-ci", "the CiliumNetworkPolicy CRD was not found",
			"SandboxProfile default, a bare Sandbox per trial (the Agent Sandbox extensions are not installed)",
		}, absent: []string{"Allowed hosts"}},
		{name: "refused for lack of cilium", path: runPath(netSkill, "v1.1.0", haiku), want: []string{
			"<code>none</code>", "the run did not start (NoCilium)", `network mode "none" needs Cilium`, "a bare Sandbox per trial",
		}},
		{name: "a failed run shows the gate it passed", path: runPath(netSkill, "v1.1.0", sonnet), want: []string{
			"no eval dataset", "tier1/quality: description is short", ">Passed<", ">Skipped<",
		}, absent: []string{"This run has no tier results"}},
		{name: "a run before network modes", path: runPath(netSkill, "v0.9.0", haiku), want: []string{
			"full: this run predates network modes.",
		}},
		{name: "a pending run", path: runPath(netSkill, "v1.2.0", haiku), want: []string{
			"The network is decided when the run starts.", "the default SandboxProfile",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.get(tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			body := html.UnescapeString(rec.Body.String())
			for _, w := range tt.want {
				if !strings.Contains(body, w) {
					t.Errorf("body lacks %q", w)
				}
			}
			for _, a := range tt.absent {
				if strings.Contains(body, a) {
					t.Errorf("body contains %q", a)
				}
			}
		})
	}
}
