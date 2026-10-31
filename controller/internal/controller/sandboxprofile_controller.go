package controller

import (
	"context"
	"fmt"
	"maps"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/crds"
	"github.com/dirien/skill-ci/controller/internal/network"
	"github.com/dirien/skill-ci/controller/internal/profiles"
)

// ConditionReady is the condition of a SandboxProfile.
const ConditionReady = "Ready"

// Reasons of a SandboxProfile's Ready condition.
const (
	// ReasonWarmPoolReady means every warm trial pod is ready.
	ReasonWarmPoolReady = "WarmPoolReady"
	// ReasonWarmPoolStarting means warm trial pods are still starting.
	ReasonWarmPoolStarting = "WarmPoolStarting"
	// ReasonExtensionsMissing means the Agent Sandbox extensions CRDs are not installed.
	ReasonExtensionsMissing = "AgentSandboxExtensionsMissing"
)

const (
	// profileRefresh re-checks a profile now and then: the enforcement mode
	// (Cilium installed later) and the CRDs may change without an event.
	profileRefresh = 5 * time.Minute
	// profileRetry re-checks a profile whose objects are not watched.
	profileRetry = 30 * time.Second
)

// SandboxProfileReconciler keeps a SandboxTemplate and a SandboxWarmPool for
// every SandboxProfile, owned by the profile, and reports the warm pool's
// readiness in the profile status.
type SandboxProfileReconciler struct {
	client.Client
	// CRDs detects the Agent Sandbox extensions and Cilium.
	CRDs crds.Checker
	// Config holds the shared settings: namespace, sandbox image, pull
	// secrets and the network policy.
	Config Config

	// watching is set when the manager watches the owned Agent Sandbox objects.
	watching bool
}

// SetupWithManager registers the reconciler. The owned SandboxTemplates and
// SandboxWarmPools are watched only when their CRDs exist at start; without
// them the profile is re-checked periodically.
func (r *SandboxProfileReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.SandboxProfile{}, builder.WithPredicates(predicate.GenerationChangedPredicate{}))
	present, err := crdPresent(ctx, r.CRDs, profiles.ExtensionCRDs...)
	if err != nil {
		// Without the watches the reconciler polls (profileRetry).
		logf.FromContext(ctx).Error(err, "detecting the Agent Sandbox extensions, not watching templates and warm pools")
	}
	if present {
		for _, gvk := range []schema.GroupVersionKind{profiles.TemplateGVK, profiles.WarmPoolGVK} {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(gvk)
			b = b.Owns(u)
		}
		r.watching = true
	}
	if err := b.Complete(r); err != nil {
		return fmt.Errorf("setting up sandboxprofile controller: %w", err)
	}
	return nil
}

// Reconcile applies the profile's SandboxTemplate and SandboxWarmPool and the
// namespace's baseline network policy, then writes the status.
func (r *SandboxProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var p v1alpha1.SandboxProfile
	if err := r.Get(ctx, req.NamespacedName, &p); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	cilium, err := crdPresent(ctx, r.CRDs, network.CiliumPolicyCRD)
	if err != nil {
		return ctrl.Result{}, err
	}
	cilium = r.Config.Network.Cilium(cilium)
	if _, err := ensureBaseline(ctx, r.Client, r.CRDs, p.Namespace, r.Config.Network); err != nil {
		return ctrl.Result{}, err
	}
	before := p.Status.DeepCopy()
	present, err := crdPresent(ctx, r.CRDs, profiles.ExtensionCRDs...)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !present {
		p.Status.TemplateName, p.Status.WarmPoolName, p.Status.ReadyReplicas = "", "", 0
		meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
			Type: ConditionReady, Status: metav1.ConditionFalse, Reason: ReasonExtensionsMissing, ObservedGeneration: p.Generation,
			Message: "the Agent Sandbox extensions CRDs (" + profiles.TemplateCRD + ", " + profiles.WarmPoolCRD +
				") are not installed: runs start a bare Sandbox per trial",
		})
		return ctrl.Result{RequeueAfter: profileRetry}, r.writeStatus(ctx, &p, before)
	}

	opts := profiles.Options{DefaultImage: r.Config.Runner.SandboxImage, ImagePullSecrets: r.Config.Runner.ImagePullSecrets, Cilium: cilium}
	if err := r.apply(ctx, &p, profiles.TemplateGVK, profiles.TemplateSpec(&p, opts)); err != nil {
		return ctrl.Result{}, err
	}
	pool := profiles.New(profiles.WarmPoolGVK, &p)
	if err := r.applyInto(ctx, &p, pool, profiles.WarmPoolSpec(&p)); err != nil {
		return ctrl.Result{}, err
	}
	_, ready := profiles.WarmPoolStatus(pool)
	p.Status.TemplateName, p.Status.WarmPoolName, p.Status.ReadyReplicas = p.Name, p.Name, ready
	meta.SetStatusCondition(&p.Status.Conditions, readyCondition(&p, ready))
	requeue := profileRefresh
	if !r.watching {
		requeue = profileRetry
	}
	return ctrl.Result{RequeueAfter: requeue}, r.writeStatus(ctx, &p, before)
}

// readyCondition says whether the warm pool has all its pods ready.
func readyCondition(p *v1alpha1.SandboxProfile, ready int32) metav1.Condition {
	want := p.WarmReplicas()
	c := metav1.Condition{Type: ConditionReady, ObservedGeneration: p.Generation}
	switch {
	case want == 0:
		c.Status, c.Reason, c.Message = metav1.ConditionTrue, ReasonWarmPoolReady, "no warm pods: every trial cold-starts a pod from the template"
	case ready >= want:
		c.Status, c.Reason, c.Message = metav1.ConditionTrue, ReasonWarmPoolReady, fmt.Sprintf("%d of %d warm trial pods ready", ready, want)
	default:
		c.Status, c.Reason, c.Message = metav1.ConditionFalse, ReasonWarmPoolStarting, fmt.Sprintf("%d of %d warm trial pods ready", ready, want)
	}
	return c
}

// apply creates or updates one Agent Sandbox object of the profile.
func (r *SandboxProfileReconciler) apply(ctx context.Context, p *v1alpha1.SandboxProfile, gvk schema.GroupVersionKind, spec map[string]any) error {
	return r.applyInto(ctx, p, profiles.New(gvk, p), spec)
}

// applyInto creates or updates obj: the keys of spec are set, keys the API
// server defaulted are kept, and the profile becomes the controller owner.
// obj holds the object as stored afterwards, status included.
func (r *SandboxProfileReconciler) applyInto(ctx context.Context, p *v1alpha1.SandboxProfile, obj *unstructured.Unstructured, spec map[string]any) error {
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		cur, _, err := unstructured.NestedMap(obj.Object, "spec")
		if err != nil || cur == nil {
			cur = map[string]any{}
		}
		maps.Copy(cur, spec)
		obj.Object["spec"] = cur
		labels := obj.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[network.LabelManagedBy] = network.ManagedBy
		labels[network.LabelProfile] = p.Name
		obj.SetLabels(labels)
		if err := controllerutil.SetControllerReference(p, obj, r.Scheme()); err != nil {
			return fmt.Errorf("owning %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("applying %s %s: %w", obj.GetKind(), obj.GetName(), err)
	}
	logf.FromContext(ctx).V(1).Info("applied", "kind", obj.GetKind(), "name", obj.GetName())
	return nil
}

// writeStatus writes the profile status when it changed.
func (r *SandboxProfileReconciler) writeStatus(ctx context.Context, p *v1alpha1.SandboxProfile, before *v1alpha1.SandboxProfileStatus) error {
	p.Status.ObservedGeneration = p.Generation
	if equality.Semantic.DeepEqual(&p.Status, before) {
		return nil
	}
	if err := r.Status().Update(ctx, p); err != nil {
		return fmt.Errorf("updating status of sandbox profile %s: %w", p.Name, err)
	}
	return nil
}
