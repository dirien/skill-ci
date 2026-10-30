package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/evals"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/models"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/semver"
)

// AnnotationLastApplied holds, on the model list ConfigMap, the list the
// controller last acted on. A model is "added" when it is in the ConfigMap data
// but not in this annotation.
const AnnotationLastApplied = "skillci.dev/last-applied-models"

// ModelsReconciler watches the model list and evaluates the skills already in
// the registry against every model that was added.
type ModelsReconciler struct {
	client.Client
	// Harbor lists the skill versions tagged beta or stable.
	Harbor harbor.Client
	// ConfigMap is the name of the model list ConfigMap.
	ConfigMap string
	// Config holds the shared settings.
	Config Config
}

// SetupWithManager registers the reconciler for the model list ConfigMap only.
func (r *ModelsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	only := predicate.NewPredicateFuncs(func(o client.Object) bool { return o.GetName() == r.ConfigMap })
	err := ctrl.NewControllerManagedBy(mgr).
		Named("models").
		For(&corev1.ConfigMap{}, builder.WithPredicates(only)).
		Complete(r)
	if err != nil {
		return fmt.Errorf("setting up models controller: %w", err)
	}
	return nil
}

// Reconcile creates SkillEvals for added models, then records the list as applied.
func (r *ModelsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	var cm corev1.ConfigMap
	if err := r.Get(ctx, req.NamespacedName, &cm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	current, err := models.FromConfigMap(&cm)
	if err != nil {
		log.Error(err, "ignoring invalid model list until it changes")
		return ctrl.Result{}, nil
	}
	var previous []models.Model
	if raw := cm.Annotations[AnnotationLastApplied]; raw != "" {
		if previous, err = models.Parse([]byte(raw)); err != nil {
			log.Error(err, "ignoring unreadable last-applied annotation", "annotation", AnnotationLastApplied)
		}
	}

	if added := models.Added(previous, current); len(added) > 0 {
		n, err := r.evaluateTagged(ctx, added)
		if err != nil {
			return ctrl.Result{}, err
		}
		log.Info("model list changed", "added", len(added), "skillevals", n)
	}

	applied, err := models.Marshal(current)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("rendering applied model list: %w", err)
	}
	if cm.Annotations[AnnotationLastApplied] == string(applied) {
		return ctrl.Result{}, nil
	}
	patch := client.MergeFrom(cm.DeepCopy())
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Annotations[AnnotationLastApplied] = string(applied)
	if err := r.Patch(ctx, &cm, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("recording applied model list: %w", err)
	}
	return ctrl.Result{}, nil
}

// evaluateTagged creates a SkillEval for every added model and every digest
// tagged beta or stable. It returns how many it created.
func (r *ModelsReconciler) evaluateTagged(ctx context.Context, added []models.Model) (int, error) {
	log := logf.FromContext(ctx)
	skills, err := r.Harbor.ListRepositories(ctx)
	if err != nil {
		return 0, fmt.Errorf("listing skills: %w", err)
	}
	created := 0
	for _, skill := range skills {
		arts, err := r.Harbor.ListArtifacts(ctx, skill)
		if err != nil {
			return created, fmt.Errorf("listing artifacts of %s: %w", skill, err)
		}
		for _, art := range arts {
			if !art.HasTag(policy.TagBeta) && !art.HasTag(policy.TagStable) {
				continue
			}
			version := semver.Newest(art.Tags)
			if version == "" {
				log.Info("skipping tagged artifact without a version tag", "skill", skill, "digest", art.Digest)
				continue
			}
			for _, m := range added {
				ok, err := evals.Ensure(ctx, r.Client, r.Config.Namespace, evals.Request{
					Skill: skill, Version: version, Digest: art.Digest, Model: m,
					Attempts: r.Config.Attempts, Trigger: v1alpha1.TriggerModelChange,
				})
				if err != nil {
					return created, fmt.Errorf("evaluating %s %s: %w", skill, version, err)
				}
				if ok {
					created++
				}
			}
		}
	}
	return created, nil
}
