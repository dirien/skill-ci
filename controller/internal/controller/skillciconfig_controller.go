package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/crds"
	"github.com/dirien/skill-ci/controller/internal/network"
	"github.com/dirien/skill-ci/controller/internal/platform"
)

// Reasons of a SkillCIConfig's Ready condition, also the reasons of its events.
const (
	// ReasonConfigApplied means the spec is valid and in force.
	ReasonConfigApplied = "Applied"
	// ReasonConfigInvalid means the spec is invalid; the last valid spec or
	// the built-in defaults stay in force.
	ReasonConfigInvalid = "Invalid"
	// ReasonConfigIgnored means the object is not the SkillCIConfig the controller reads.
	ReasonConfigIgnored = "Ignored"
)

// configRefresh re-checks the config now and then: whether Cilium is
// installed (status.effective.network.enforcedBy) changes without an event.
const configRefresh = 5 * time.Minute

// SkillCIConfigReconciler reports in the status of every SkillCIConfig of
// the namespace whether it is in force, and what is in force. The settings
// themselves are read by platform.Source at the moment a run is resolved.
type SkillCIConfigReconciler struct {
	client.Client
	// Recorder emits an event when a config becomes valid, invalid or ignored.
	Recorder events.EventRecorder
	// CRDs detects Cilium; nil means no optional CRD is installed.
	CRDs crds.Checker
	// Platform evaluates the spec and remembers the last valid one. Its
	// Namespace is the only namespace whose SkillCIConfig default is read.
	Platform *platform.Source
}

// SetupWithManager registers the reconciler for SkillCIConfigs.
func (r *SkillCIConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	err := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.SkillCIConfig{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
	if err != nil {
		return fmt.Errorf("setting up skillciconfig controller: %w", err)
	}
	return nil
}

// Reconcile writes the Ready condition and status.effective of a SkillCIConfig.
func (r *SkillCIConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cfg v1alpha1.SkillCIConfig
	if err := r.Get(ctx, req.NamespacedName, &cfg); err != nil {
		if apierrors.IsNotFound(err) && req.Name == v1alpha1.SkillCIConfigName {
			r.Platform.Evaluate(nil) // deleted: the built-in defaults apply again
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := cfg.Status.DeepCopy()
	if cfg.Name != v1alpha1.SkillCIConfigName || cfg.Namespace != r.Platform.Namespace {
		cfg.Status.Effective = nil
		r.setReady(&cfg, metav1.ConditionFalse, ReasonConfigIgnored, fmt.Sprintf(
			"ignored: the controller reads only the SkillCIConfig named %s in namespace %s",
			v1alpha1.SkillCIConfigName, r.Platform.Namespace))
		return ctrl.Result{}, r.writeConfigStatus(ctx, &cfg, before)
	}

	st := r.Platform.Evaluate(&cfg)
	ciliumCRD, err := crdPresent(ctx, r.CRDs, network.CiliumPolicyCRD)
	if err != nil {
		return ctrl.Result{}, err
	}
	cfg.Status.Effective = platform.Effective(st, platform.EnforcedBy(st.Settings.Network, ciliumCRD))
	status, reason, msg := configCondition(st, cfg.Status.Effective, ciliumCRD)
	r.setReady(&cfg, status, reason, msg)
	return ctrl.Result{RequeueAfter: configRefresh}, r.writeConfigStatus(ctx, &cfg, before)
}

// configCondition renders the Ready condition of the SkillCIConfig default.
func configCondition(st platform.State, eff *v1alpha1.EffectiveConfig, ciliumCRD bool) (metav1.ConditionStatus, string, string) {
	if st.Invalid != nil {
		kept := "the controller uses the built-in defaults (it has seen no valid spec of this object since it started)"
		if st.Source == v1alpha1.ConfigSourceLastValid {
			kept = fmt.Sprintf("the controller keeps the last valid spec (generation %d)", st.Generation)
		}
		return metav1.ConditionFalse, ReasonConfigInvalid, fmt.Sprintf("invalid spec: %s; %s", st.Invalid, kept)
	}
	n := eff.Network
	msg := fmt.Sprintf("applied: default network %s, allowed modes %s, enforcement %s (%s), bundles %s, default sandbox profile %s",
		n.Default, strings.Join(n.Modes, ", "), n.Enforcement, n.EnforcedBy, bundleList(n.Bundles), eff.DefaultSandboxProfile)
	if n.Enforcement == string(network.EnforcementCilium) && !ciliumCRD {
		msg += "; the CiliumNetworkPolicy CRD was not found, so every run fails until Cilium is installed"
	}
	return metav1.ConditionTrue, ReasonConfigApplied, msg
}

func bundleList(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return "@" + strings.Join(names, ", @")
}

// setReady sets the Ready condition and emits an event when its reason or
// message changes.
func (r *SkillCIConfigReconciler) setReady(cfg *v1alpha1.SkillCIConfig, status metav1.ConditionStatus, reason, msg string) {
	prev := meta.FindStatusCondition(cfg.Status.Conditions, ConditionReady)
	changed := prev == nil || prev.Reason != reason || prev.Message != msg
	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: ConditionReady, Status: status, Reason: reason, Message: msg, ObservedGeneration: cfg.Generation,
	})
	if !changed || r.Recorder == nil {
		return
	}
	eventType := corev1.EventTypeNormal
	if status != metav1.ConditionTrue {
		eventType = corev1.EventTypeWarning
	}
	r.Recorder.Eventf(cfg, nil, eventType, reason, "Configure", "%s", msg)
}

// writeConfigStatus writes the status when it changed.
func (r *SkillCIConfigReconciler) writeConfigStatus(ctx context.Context, cfg *v1alpha1.SkillCIConfig, before *v1alpha1.SkillCIConfigStatus) error {
	cfg.Status.ObservedGeneration = cfg.Generation
	if equality.Semantic.DeepEqual(&cfg.Status, before) {
		return nil
	}
	if err := r.Status().Update(ctx, cfg); err != nil {
		return fmt.Errorf("updating status of SkillCIConfig %s: %w", cfg.Name, err)
	}
	return nil
}
