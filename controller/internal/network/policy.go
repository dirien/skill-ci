package network

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
)

// CiliumPolicyGVK is the kind of the policies skill-ci creates.
var CiliumPolicyGVK = schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}

// CiliumPolicyCRD is the name of the CRD whose presence means Cilium enforces policies.
const CiliumPolicyCRD = "ciliumnetworkpolicies.cilium.io"

// Labels of trial pods and of the policies. The sandbox.users.io domain is the
// one Agent Sandbox lets a SandboxClaim put on a claimed pod.
const (
	// LabelTrialEval holds the SkillEval name on every trial pod.
	LabelTrialEval = "sandbox.users.io/skillci-eval"
	// LabelTrialRole is "trial" on every trial pod.
	LabelTrialRole = "sandbox.users.io/skillci-role"
	// RoleTrial is the value of LabelTrialRole.
	RoleTrial = "trial"
	// LabelComponent is "trial-sandbox" on every pod of a SandboxProfile's template.
	LabelComponent = "skillci.dev/component"
	// ComponentTrialSandbox is the value of LabelComponent.
	ComponentTrialSandbox = "trial-sandbox"
	// LabelProfile holds the SandboxProfile name on the pods of its template.
	LabelProfile = "skillci.dev/profile"
	// LabelEval holds the SkillEval name on its policy.
	LabelEval = "skillci.dev/eval"
	// LabelManagedBy marks the objects skill-ci manages.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// ManagedBy is the value of LabelManagedBy.
	ManagedBy = "skill-ci"
)

// BaselinePolicyName is the per-namespace policy that leaves every trial pod
// of a profile only DNS until an eval policy selects it.
const BaselinePolicyName = "skillci-trial-baseline"

const (
	policyPrefix = "skillci-net-"
	maxName      = 63
	keepChars    = 52
	hashChars    = 10
)

// PolicyName returns the name of a SkillEval's policy, skillci-net-<name>. A
// name over 63 characters keeps its first 52 and ends in "-" and 10 hex
// characters of the SHA-256 of the full name.
func PolicyName(eval string) string {
	full := policyPrefix + eval
	if len(full) <= maxName {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	return strings.TrimRight(full[:keepChars], "-") + "-" + hex.EncodeToString(sum[:])[:hashChars]
}

// newPolicy returns an empty CiliumNetworkPolicy.
func newPolicy(namespace, name string, labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(CiliumPolicyGVK)
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetLabels(labels)
	return u
}

// dnsRule allows DNS to kube-dns, through Cilium's DNS proxy so toFQDNs can learn the answers.
func dnsRule() map[string]any {
	return map[string]any{
		"toEndpoints": []any{map[string]any{"matchLabels": map[string]any{
			"k8s:io.kubernetes.pod.namespace": "kube-system",
			"k8s-app":                         "kube-dns",
		}}},
		"toPorts": []any{map[string]any{
			"ports": []any{map[string]any{"port": "53", "protocol": "ANY"}},
			"rules": map[string]any{"dns": []any{map[string]any{"matchPattern": "*"}}},
		}},
	}
}

// fqdnRule allows TCP 443 to the hosts; "*.x" becomes a matchPattern.
func fqdnRule(hosts []string) map[string]any {
	fqdns := make([]any, 0, len(hosts))
	for _, h := range hosts {
		if strings.HasPrefix(h, "*.") {
			fqdns = append(fqdns, map[string]any{"matchPattern": h})
		} else {
			fqdns = append(fqdns, map[string]any{"matchName": h})
		}
	}
	return map[string]any{
		"toFQDNs": fqdns,
		"toPorts": []any{map[string]any{"ports": []any{map[string]any{"port": "443", "protocol": "TCP"}}}},
	}
}

// BaselinePolicy selects every pod of a SandboxProfile template (warm or
// claimed) and allows DNS only, so a pod without an eval policy has no egress.
func BaselinePolicy(namespace string) *unstructured.Unstructured {
	u := newPolicy(namespace, BaselinePolicyName, map[string]string{LabelManagedBy: ManagedBy})
	u.Object["spec"] = map[string]any{
		"endpointSelector": map[string]any{"matchLabels": map[string]any{LabelComponent: ComponentTrialSandbox}},
		"egress":           []any{dnsRule()},
	}
	return u
}

// EvalPolicy selects the trial pods of one SkillEval and allows what its mode
// allows: the world for full, the hosts on TCP 443 for none and allowlist,
// DNS always.
func EvalPolicy(namespace, eval string, eff Effective) *unstructured.Unstructured {
	u := newPolicy(namespace, PolicyName(eval), map[string]string{LabelManagedBy: ManagedBy, LabelEval: eval})
	egress := []any{dnsRule()}
	switch {
	case eff.Mode == v1alpha1.NetworkFull:
		egress = append(egress, map[string]any{"toEntities": []any{"world"}})
	case len(eff.Hosts) > 0:
		egress = append(egress, fqdnRule(eff.Hosts))
	}
	u.Object["spec"] = map[string]any{
		"endpointSelector": map[string]any{"matchLabels": map[string]any{
			LabelTrialEval: eval,
			LabelTrialRole: RoleTrial,
		}},
		"egress": egress,
	}
	return u
}
