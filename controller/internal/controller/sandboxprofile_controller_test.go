package controller

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/crds"
	"github.com/dirien/skill-ci/controller/internal/network"
	"github.com/dirien/skill-ci/controller/internal/profiles"
	"github.com/dirien/skill-ci/controller/internal/runner"
)

func getObject(t *testing.T, c client.Client, gvk schema.GroupVersionKind, name string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, u); err != nil {
		return nil
	}
	return u
}

func TestSandboxProfileReconciler(t *testing.T) {
	two := int32(2)
	tests := []struct {
		name        string
		crds        crds.Checker
		network     network.Policy
		poolStatus  map[string]any
		watching    bool
		wantPool    string
		wantReady   int32
		wantReason  string
		wantMgmt    string
		wantBase    bool
		wantRequeue time.Duration
	}{
		{name: "no Agent Sandbox extensions", crds: crds.Static{}, wantReason: ReasonExtensionsMissing, wantRequeue: profileRetry},
		{name: "extensions, no cilium: managed template, pool starting", crds: crds.Static{profiles.TemplateCRD: true, profiles.WarmPoolCRD: true},
			wantPool: "default", wantReason: ReasonWarmPoolStarting, wantMgmt: profiles.NetworkManaged, wantRequeue: profileRetry},
		{name: "cilium: unmanaged template and the baseline", crds: allCRDs, watching: true,
			wantPool: "default", wantReason: ReasonWarmPoolStarting, wantMgmt: profiles.NetworkUnmanaged, wantBase: true, wantRequeue: profileRefresh},
		{name: "cilium but enforcement off: managed, no baseline", crds: allCRDs, network: network.Policy{Enforcement: network.EnforcementOff},
			wantPool: "default", wantReason: ReasonWarmPoolStarting, wantMgmt: profiles.NetworkManaged, wantRequeue: profileRetry},
		{name: "warm pool ready", crds: allCRDs, poolStatus: map[string]any{"replicas": int64(2), "readyReplicas": int64(2)},
			wantPool: "default", wantReady: 2, wantReason: ReasonWarmPoolReady, wantMgmt: profiles.NetworkUnmanaged, wantBase: true, wantRequeue: profileRetry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &v1alpha1.SandboxProfile{}
			p.Name, p.Namespace, p.Generation = "default", ns, 1
			p.Spec.WarmPool.Replicas = &two
			var objs []client.Object
			objs = append(objs, p)
			if tt.poolStatus != nil {
				pool := profiles.New(profiles.WarmPoolGVK, p)
				pool.Object["spec"] = map[string]any{"sandboxTemplateRef": map[string]any{"name": "default"}}
				pool.Object["status"] = tt.poolStatus
				objs = append(objs, pool)
			}
			e := newEnv(t, Config{}, 0, nil, objs...)
			cfg := Config{Namespace: ns, Network: tt.network,
				Runner: runner.Config{SandboxImage: "ghcr.io/dirien/skill-ci-sandbox:0.9.0", ImagePullSecrets: []string{"ghcr"}}}
			r := &SandboxProfileReconciler{Client: e.client, CRDs: tt.crds, Config: cfg, watching: tt.watching}

			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "default"}})
			if err != nil {
				t.Fatal(err)
			}
			if res.RequeueAfter != tt.wantRequeue {
				t.Errorf("requeue %s, want %s", res.RequeueAfter, tt.wantRequeue)
			}
			var got v1alpha1.SandboxProfile
			if err := e.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "default"}, &got); err != nil {
				t.Fatal(err)
			}
			if got.Status.WarmPoolName != tt.wantPool || got.Status.TemplateName != tt.wantPool || got.Status.ReadyReplicas != tt.wantReady {
				t.Errorf("status %+v", got.Status)
			}
			if c := findCondition(got.Status.Conditions, ConditionReady); c == nil || c.Reason != tt.wantReason {
				t.Errorf("Ready condition %+v, want reason %s", c, tt.wantReason)
			}

			tmpl := getObject(t, e.client, profiles.TemplateGVK, "default")
			pool := getObject(t, e.client, profiles.WarmPoolGVK, "default")
			if tt.wantMgmt == "" {
				if tmpl != nil || pool != nil {
					t.Fatal("no Agent Sandbox objects without the extensions")
				}
			} else {
				if tmpl == nil || pool == nil {
					t.Fatal("template and warm pool must exist")
				}
				for _, o := range []*unstructured.Unstructured{tmpl, pool} {
					refs := o.GetOwnerReferences()
					if len(refs) != 1 || refs[0].Kind != "SandboxProfile" || refs[0].Name != "default" || refs[0].Controller == nil || !*refs[0].Controller {
						t.Errorf("%s owners %+v", o.GetKind(), refs)
					}
				}
				if m, _, _ := unstructured.NestedString(tmpl.Object, "spec", "networkPolicyManagement"); m != tt.wantMgmt {
					t.Errorf("networkPolicyManagement %q, want %q", m, tt.wantMgmt)
				}
				if img, _, _ := unstructured.NestedSlice(tmpl.Object, "spec", "podTemplate", "spec", "containers"); len(img) != 1 ||
					img[0].(map[string]any)["image"] != "ghcr.io/dirien/skill-ci-sandbox:0.9.0" {
					t.Errorf("containers %v", img)
				}
				if n, _, _ := unstructured.NestedInt64(pool.Object, "spec", "replicas"); n != 2 {
					t.Errorf("pool replicas %d", n)
				}
				if s, _, _ := unstructured.NestedString(pool.Object, "spec", "updateStrategy", "type"); s != "Recreate" {
					t.Errorf("pool update strategy %q", s)
				}
			}
			if base := getPolicy(t, e.client, network.BaselinePolicyName); (base != nil) != tt.wantBase {
				t.Errorf("baseline exists %t, want %t", base != nil, tt.wantBase)
			}

			// A second pass changes nothing.
			before := got.ResourceVersion
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "default"}}); err != nil {
				t.Fatal(err)
			}
			if err := e.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "default"}, &got); err != nil {
				t.Fatal(err)
			}
			if got.ResourceVersion != before {
				t.Error("a second reconcile rewrote the profile status")
			}
		})
	}
}

func TestSandboxProfileGone(t *testing.T) {
	e := newEnv(t, Config{}, 0, nil)
	r := &SandboxProfileReconciler{Client: e.client, CRDs: allCRDs}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "nope"}}); err != nil {
		t.Fatal(err)
	}
}

func TestReadyCondition(t *testing.T) {
	zero, two := int32(0), int32(2)
	tests := []struct {
		name     string
		replicas *int32
		ready    int32
		want     string
		message  string
	}{
		{name: "no warm pods", replicas: &zero, want: ReasonWarmPoolReady, message: "no warm pods: every trial cold-starts a pod from the template"},
		{name: "starting", replicas: &two, ready: 1, want: ReasonWarmPoolStarting, message: "1 of 2 warm trial pods ready"},
		{name: "ready", replicas: &two, ready: 2, want: ReasonWarmPoolReady, message: "2 of 2 warm trial pods ready"},
		{name: "default size", ready: 2, want: ReasonWarmPoolReady, message: "2 of 2 warm trial pods ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &v1alpha1.SandboxProfile{}
			p.Spec.WarmPool.Replicas = tt.replicas
			c := readyCondition(p, tt.ready)
			if c.Reason != tt.want || c.Message != tt.message {
				t.Fatalf("got %s %q", c.Reason, c.Message)
			}
		})
	}
}
