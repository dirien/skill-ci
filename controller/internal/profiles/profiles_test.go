package profiles

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
)

func profile(mut func(*v1alpha1.SandboxProfile)) *v1alpha1.SandboxProfile {
	p := &v1alpha1.SandboxProfile{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "skill-ci"}}
	if mut != nil {
		mut(p)
	}
	return p
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTemplateSpec(t *testing.T) {
	q := func(s string) *resource.Quantity { v := resource.MustParse(s); return &v }
	sec := int64(30)
	tests := []struct {
		name    string
		profile *v1alpha1.SandboxProfile
		opts    Options
		want    string
	}{
		{
			name:    "defaults, no cilium",
			profile: profile(nil),
			opts:    Options{DefaultImage: "ghcr.io/dirien/skill-ci-sandbox:0.9.0"},
			want: `{"networkPolicyManagement":"Managed","podTemplate":{"metadata":{` +
				`"annotations":{"cluster-autoscaler.kubernetes.io/safe-to-evict":"false"},` +
				`"labels":{"skillci.dev/component":"trial-sandbox","skillci.dev/profile":"default"}},` +
				`"spec":{"automountServiceAccountToken":false,"containers":[{"command":["sleep","infinity"],` +
				`"image":"ghcr.io/dirien/skill-ci-sandbox:0.9.0","imagePullPolicy":"IfNotPresent","name":"main",` +
				`"resources":{"limits":{"memory":"2Gi"},"requests":{"cpu":"1","memory":"2Gi"}},` +
				`"securityContext":{"allowPrivilegeEscalation":false,"runAsUser":0}}],"terminationGracePeriodSeconds":1}}}`,
		},
		{
			name: "everything set, cilium",
			profile: profile(func(p *v1alpha1.SandboxProfile) {
				p.Spec.Image = "registry.example.com/sandbox:latest"
				p.Spec.Resources = v1alpha1.SandboxResources{CPU: q("500m"), Memory: q("4Gi")}
				p.Spec.RuntimeClassName = "gvisor"
				p.Spec.NodeSelector = map[string]string{"pool": "trials"}
				p.Spec.Tolerations = []corev1.Toleration{{Key: "trials", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule, TolerationSeconds: &sec}}
			}),
			opts: Options{DefaultImage: "ignored:1", ImagePullSecrets: []string{"ghcr"}, Cilium: true},
			want: `{"networkPolicyManagement":"Unmanaged","podTemplate":{"metadata":{` +
				`"annotations":{"cluster-autoscaler.kubernetes.io/safe-to-evict":"false"},` +
				`"labels":{"skillci.dev/component":"trial-sandbox","skillci.dev/profile":"default"}},` +
				`"spec":{"automountServiceAccountToken":false,"containers":[{"command":["sleep","infinity"],` +
				`"image":"registry.example.com/sandbox:latest","imagePullPolicy":"Always","name":"main",` +
				`"resources":{"limits":{"memory":"4Gi"},"requests":{"cpu":"500m","memory":"4Gi"}},` +
				`"securityContext":{"allowPrivilegeEscalation":false,"runAsUser":0}}],"imagePullSecrets":[{"name":"ghcr"}],` +
				`"nodeSelector":{"pool":"trials"},"runtimeClassName":"gvisor","terminationGracePeriodSeconds":1,` +
				`"tolerations":[{"effect":"NoSchedule","key":"trials","operator":"Exists","tolerationSeconds":30}]}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := TemplateSpec(tt.profile, tt.opts)
			if got := mustJSON(t, spec); got != tt.want {
				t.Fatalf("\n got %s\nwant %s", got, tt.want)
			}
			u := New(TemplateGVK, tt.profile)
			u.Object["spec"] = spec
			_ = u.DeepCopy() // panics on a value unstructured cannot copy
		})
	}
}

func TestPullPolicy(t *testing.T) {
	tests := map[string]string{
		"ghcr.io/dirien/skill-ci-sandbox:0.9.0":  "IfNotPresent",
		"ghcr.io/dirien/skill-ci-sandbox:latest": "Always",
		"ghcr.io/dirien/skill-ci-sandbox":        "Always",
		"localhost:5000/sandbox":                 "Always",
		"localhost:5000/sandbox:1":               "IfNotPresent",
		"ghcr.io/x/y@sha256:abc":                 "IfNotPresent",
	}
	for image, want := range tests {
		if got := pullPolicy(image); got != want {
			t.Errorf("%s: got %s, want %s", image, got, want)
		}
	}
}

func TestWarmPoolSpec(t *testing.T) {
	zero := int32(0)
	tests := []struct {
		name    string
		profile *v1alpha1.SandboxProfile
		want    string
	}{
		{name: "default replicas", profile: profile(nil),
			want: `{"replicas":2,"sandboxTemplateRef":{"name":"default"},"updateStrategy":{"type":"Recreate"}}`},
		{name: "zero replicas", profile: profile(func(p *v1alpha1.SandboxProfile) { p.Spec.WarmPool.Replicas = &zero }),
			want: `{"replicas":0,"sandboxTemplateRef":{"name":"default"},"updateStrategy":{"type":"Recreate"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mustJSON(t, WarmPoolSpec(tt.profile)); got != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestWarmPoolStatus(t *testing.T) {
	tests := []struct {
		name          string
		status        map[string]any
		replicas, rdy int32
	}{
		{name: "no status"},
		{name: "two of two", status: map[string]any{"replicas": int64(2), "readyReplicas": int64(2)}, replicas: 2, rdy: 2},
		{name: "one starting", status: map[string]any{"replicas": int64(2), "readyReplicas": int64(1)}, replicas: 2, rdy: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{}}
			if tt.status != nil {
				u.Object["status"] = tt.status
			}
			r, rr := WarmPoolStatus(u)
			if r != tt.replicas || rr != tt.rdy {
				t.Fatalf("got %d/%d, want %d/%d", rr, r, tt.rdy, tt.replicas)
			}
		})
	}
}

func TestImage(t *testing.T) {
	if got := Image(profile(nil), Options{DefaultImage: "a:1"}); got != "a:1" {
		t.Fatalf("got %s", got)
	}
	if got := Image(profile(func(p *v1alpha1.SandboxProfile) { p.Spec.Image = "b:2" }), Options{DefaultImage: "a:1"}); got != "b:2" {
		t.Fatalf("got %s", got)
	}
}
