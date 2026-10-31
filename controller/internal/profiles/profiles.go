// Package profiles renders the Agent Sandbox objects of a SandboxProfile: a
// SandboxTemplate with the trial pod and a SandboxWarmPool of pre-started pods.
package profiles

import (
	"math"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/network"
)

// Agent Sandbox extensions (extensions.agents.x-k8s.io/v1beta1).
var (
	// TemplateGVK is the kind of a SandboxTemplate.
	TemplateGVK = schema.GroupVersionKind{Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Kind: "SandboxTemplate"}
	// WarmPoolGVK is the kind of a SandboxWarmPool.
	WarmPoolGVK = schema.GroupVersionKind{Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Kind: "SandboxWarmPool"}
)

// CRDs a SandboxProfile needs. Without them runs start a bare Sandbox per trial.
const (
	// TemplateCRD is the SandboxTemplate CRD.
	TemplateCRD = "sandboxtemplates.extensions.agents.x-k8s.io"
	// WarmPoolCRD is the SandboxWarmPool CRD.
	WarmPoolCRD = "sandboxwarmpools.extensions.agents.x-k8s.io"
)

// ExtensionCRDs are the CRDs the profile reconciler needs.
var ExtensionCRDs = []string{TemplateCRD, WarmPoolCRD}

// Network policy management of a SandboxTemplate.
const (
	// NetworkManaged lets Agent Sandbox apply its secure default: the
	// internet, not the cluster.
	NetworkManaged = "Managed"
	// NetworkUnmanaged leaves egress to skill-ci's CiliumNetworkPolicies.
	NetworkUnmanaged = "Unmanaged"
)

// SafeToEvictAnnotation tells the cluster autoscaler not to evict a pod during scale-down.
const SafeToEvictAnnotation = "cluster-autoscaler.kubernetes.io/safe-to-evict"

// ContainerName is the trial container, the same as the bare Sandbox path.
const ContainerName = "main"

// Options are the controller settings a template depends on.
type Options struct {
	// DefaultImage is the controller's --sandbox-image, used when the profile names none.
	DefaultImage string
	// ImagePullSecrets name the pull Secrets of the trial pods.
	ImagePullSecrets []string
	// Cilium reports whether skill-ci's CiliumNetworkPolicies own egress.
	Cilium bool
}

// Image returns the sandbox image of the profile.
func Image(p *v1alpha1.SandboxProfile, opts Options) string {
	if p.Spec.Image != "" {
		return p.Spec.Image
	}
	return opts.DefaultImage
}

// pullPolicy is Always for a mutable tag (latest or none), IfNotPresent
// otherwise, like the bare Sandbox path.
func pullPolicy(image string) string {
	if strings.Contains(image, "@") {
		return "IfNotPresent"
	}
	last := image[strings.LastIndex(image, "/")+1:]
	_, tag, hasTag := strings.Cut(last, ":")
	if !hasTag || tag == "latest" {
		return "Always"
	}
	return "IfNotPresent"
}

// TemplateSpec returns the spec of the profile's SandboxTemplate. The pod
// matches the bare Sandbox of harbor_agent_sandbox (container main, sleep
// infinity, root without privilege escalation) plus the profile's size and
// scheduling, labels for the baseline policy and a 1 s grace period so a
// released trial pod goes away at once.
func TemplateSpec(p *v1alpha1.SandboxProfile, opts Options) map[string]any {
	image := Image(p, opts)
	memory := p.Memory()
	cpu := p.CPU()
	container := map[string]any{
		"name":            ContainerName,
		"image":           image,
		"imagePullPolicy": pullPolicy(image),
		"command":         []any{"sleep", "infinity"},
		"securityContext": map[string]any{"runAsUser": int64(0), "allowPrivilegeEscalation": false},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": cpu.String(), "memory": memory.String()},
			"limits":   map[string]any{"memory": memory.String()},
		},
	}
	podSpec := map[string]any{
		"automountServiceAccountToken":  false,
		"terminationGracePeriodSeconds": int64(1),
		"containers":                    []any{container},
	}
	if len(opts.ImagePullSecrets) > 0 {
		refs := make([]any, 0, len(opts.ImagePullSecrets))
		for _, s := range opts.ImagePullSecrets {
			refs = append(refs, map[string]any{"name": s})
		}
		podSpec["imagePullSecrets"] = refs
	}
	if p.Spec.RuntimeClassName != "" {
		podSpec["runtimeClassName"] = p.Spec.RuntimeClassName
	}
	if len(p.Spec.NodeSelector) > 0 {
		sel := map[string]any{}
		for k, v := range p.Spec.NodeSelector {
			sel[k] = v
		}
		podSpec["nodeSelector"] = sel
	}
	if len(p.Spec.Tolerations) > 0 {
		tols := make([]any, 0, len(p.Spec.Tolerations))
		for _, t := range p.Spec.Tolerations {
			tols = append(tols, toleration(t))
		}
		podSpec["tolerations"] = tols
	}
	management := NetworkManaged
	if opts.Cilium {
		management = NetworkUnmanaged
	}
	return map[string]any{
		"networkPolicyManagement": management,
		"podTemplate": map[string]any{
			"metadata": map[string]any{
				"labels": map[string]any{
					network.LabelComponent: network.ComponentTrialSandbox,
					network.LabelProfile:   p.Name,
				},
				"annotations": map[string]any{SafeToEvictAnnotation: "false"},
			},
			"spec": podSpec,
		},
	}
}

// toleration renders a toleration with the fields it sets.
func toleration(t corev1.Toleration) map[string]any {
	m := map[string]any{}
	for k, v := range map[string]string{"key": t.Key, "operator": string(t.Operator), "value": t.Value, "effect": string(t.Effect)} {
		if v != "" {
			m[k] = v
		}
	}
	if t.TolerationSeconds != nil {
		m["tolerationSeconds"] = *t.TolerationSeconds
	}
	return m
}

// WarmPoolSpec returns the spec of the profile's SandboxWarmPool. Recreate
// replaces warm pods when the template changes, so a new image (a release
// upgrade) never leaves stale pods in the pool.
func WarmPoolSpec(p *v1alpha1.SandboxProfile) map[string]any {
	return map[string]any{
		"replicas":           int64(p.WarmReplicas()),
		"sandboxTemplateRef": map[string]any{"name": p.Name},
		"updateStrategy":     map[string]any{"type": "Recreate"},
	}
}

// New returns an empty object of the kind, named and namespaced like the profile.
func New(gvk schema.GroupVersionKind, p *v1alpha1.SandboxProfile) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(p.Namespace)
	u.SetName(p.Name)
	return u
}

// WarmPoolStatus reads replicas and readyReplicas from a SandboxWarmPool.
func WarmPoolStatus(u *unstructured.Unstructured) (replicas, ready int32) {
	r, _, _ := unstructured.NestedInt64(u.Object, "status", "replicas")
	rr, _, _ := unstructured.NestedInt64(u.Object, "status", "readyReplicas")
	return int32(min(max(r, 0), math.MaxInt32)), int32(min(max(rr, 0), math.MaxInt32))
}
