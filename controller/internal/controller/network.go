package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/crds"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/network"
	"github.com/dirien/skill-ci/controller/internal/platform"
)

// ConditionNetworkPolicy records how the run's network mode is enforced. Its
// reasons are the network.Reason constants.
const ConditionNetworkPolicy = "NetworkPolicy"

// llmKeysSecret is the Secret whose openai-base-url decides the OpenAI host.
const (
	llmKeysSecret    = "llm-keys"
	openAIBaseURLKey = "openai-base-url"
)

// runPlan is what a pending run starts with.
type runPlan struct {
	// network is the effective network of the trials.
	network network.Effective
	// hubbleRelay is the HUBBLE_SERVER of the Job; empty collects no flows.
	hubbleRelay string
	// judge is the LLM judge of the Job.
	judge network.Judge
}

// prepareRun resolves the network and the sandbox profile of a pending run
// and records them in the status (in memory; the next status write stores
// them). A non-empty failure is the message the run fails with, without a Job.
//
// The platform settings are looked up once per call, so a change to the
// SkillCIConfig applies to every run resolved after it. A run whose
// spec.network was recorded earlier keeps its mode; the allowed modes,
// bundles and enforcement are those in force now.
func (r *SkillEvalReconciler) prepareRun(ctx context.Context, se *v1alpha1.SkillEval) (plan runPlan, failure string, err error) {
	settings, err := r.Config.Platform.Current(ctx)
	if err != nil {
		return runPlan{}, "", fmt.Errorf("reading the platform settings for %s: %w", se.Name, err)
	}
	if err := r.declareNetwork(ctx, se, settings.Network); err != nil {
		return runPlan{}, "", err
	}
	if err := settings.Judge.CheckAgent(se.Spec.Agent); err != nil {
		return runPlan{}, err.Error(), nil // the runner would refuse it after starting
	}
	if failure, err := r.resolveProfile(ctx, se, settings.DefaultSandboxProfile); err != nil || failure != "" {
		return runPlan{}, failure, err
	}
	eff, err := r.resolveNetwork(ctx, se, settings.Network, settings.Judge)
	var nerr *network.Error
	if errors.As(err, &nerr) {
		se.Status.Network = &v1alpha1.NetworkStatus{Mode: se.Spec.Network.Mode}
		meta.SetStatusCondition(&se.Status.Conditions, metav1.Condition{
			Type: ConditionNetworkPolicy, Status: metav1.ConditionFalse, Reason: nerr.Reason, Message: nerr.Message,
		})
		return runPlan{}, nerr.Message, nil
	}
	if err != nil {
		return runPlan{}, "", err
	}
	st := &v1alpha1.NetworkStatus{Mode: eff.Mode, Enforced: eff.Cilium, Hosts: eff.Hosts}
	cond := metav1.Condition{Type: ConditionNetworkPolicy, Status: metav1.ConditionFalse, Reason: network.ReasonNotEnforced}
	if eff.Cilium {
		st.Policy = network.PolicyName(se.Name)
		cond.Status, cond.Reason = metav1.ConditionTrue, network.ReasonEnforced
	}
	cond.Message = networkMessage(eff, st.Policy, settings.Network)
	if eff.Note != "" {
		cond.Message += "; " + eff.Note
	}
	se.Status.Network = st
	meta.SetStatusCondition(&se.Status.Conditions, cond)
	return runPlan{network: eff, hubbleRelay: settings.HubbleRelay, judge: settings.Judge}, "", nil
}

// networkMessage says in a sentence what the trials may reach and what enforces it.
func networkMessage(eff network.Effective, policy string, p network.Policy) string {
	reach := "the internet, not the cluster"
	if eff.Mode != v1alpha1.NetworkFull {
		reach = fmt.Sprintf("%d host(s) on TCP 443: %s", len(eff.Hosts), strings.Join(eff.Hosts, ", "))
	}
	switch {
	case eff.Cilium:
		return fmt.Sprintf("mode %s (%s): CiliumNetworkPolicy %s allows %s", eff.Mode, eff.Source, policy, reach)
	case p.EnforcementMode() == network.EnforcementOff:
		return fmt.Sprintf("mode %s (%s) is not enforced: SkillCIConfig network.enforcement is off", eff.Mode, eff.Source)
	default:
		return fmt.Sprintf("mode %s (%s) is not enforced by skill-ci: the CiliumNetworkPolicy CRD was not found", eff.Mode, eff.Source)
	}
}

// declareNetwork fills in spec.network of a run that has none from the
// manifest annotations of its digest. A digest Harbor does not have counts as
// declaring nothing. The status in memory survives the spec write.
func (r *SkillEvalReconciler) declareNetwork(ctx context.Context, se *v1alpha1.SkillEval, p network.Policy) error {
	if se.Spec.Network != nil {
		return nil
	}
	art, err := r.Harbor.GetArtifact(ctx, se.Spec.Skill, se.Spec.Digest)
	if err != nil && !errors.Is(err, harbor.ErrNotFound) {
		return fmt.Errorf("reading the network annotations of %s@%s: %w", se.Spec.Skill, se.Spec.Digest, err)
	}
	se.Spec.Network = network.Declared(art.Annotations, p)
	status := se.Status.DeepCopy()
	if err := r.Update(ctx, se); err != nil {
		return fmt.Errorf("recording the network of %s: %w", se.Name, err)
	}
	se.Status = *status
	return nil
}

// resolveProfile records the SandboxProfile and warm pool of the run. A run
// without a profile name uses the default profile of the SkillCIConfig. When
// the default profile does not exist the run starts a bare Sandbox per trial;
// a profile the run names explicitly must exist.
func (r *SkillEvalReconciler) resolveProfile(ctx context.Context, se *v1alpha1.SkillEval, defaultProfile string) (failure string, err error) {
	se.Status.SandboxProfile, se.Status.WarmPool = "", ""
	name := se.Spec.SandboxProfile
	if name == "" {
		name = defaultProfile
	}
	if name == "" {
		return "", nil
	}
	var p v1alpha1.SandboxProfile
	err = r.Reader.Get(ctx, types.NamespacedName{Namespace: se.Namespace, Name: name}, &p)
	switch {
	case apierrors.IsNotFound(err) || meta.IsNoMatchError(err):
		if name == defaultProfile {
			logf.FromContext(ctx).Info("default sandbox profile not found, the run starts a bare Sandbox per trial", "profile", name)
			return "", nil
		}
		return fmt.Sprintf("sandbox profile %q not found", name), nil
	case err != nil:
		return "", fmt.Errorf("reading sandbox profile %s: %w", name, err)
	}
	se.Status.SandboxProfile, se.Status.WarmPool = name, p.Status.WarmPoolName
	return "", nil
}

// resolveNetwork computes the run's effective network, the judge's host included.
func (r *SkillEvalReconciler) resolveNetwork(ctx context.Context, se *v1alpha1.SkillEval, p network.Policy, judge network.Judge) (network.Effective, error) {
	cilium, err := crdPresent(ctx, r.CRDs, network.CiliumPolicyCRD)
	if err != nil {
		return network.Effective{}, err
	}
	entry := network.Entry{Agent: se.Spec.Agent, AgentVersion: se.Spec.AgentVersion, Model: se.Spec.Model}
	in := network.Input{Spec: se.Spec.Network, Entry: entry, Judge: judge, CiliumCRD: cilium}
	if se.Spec.Network.Mode != v1alpha1.NetworkFull && (network.NeedsOpenAIBaseURL(entry) || judge.NeedsOpenAIBaseURL()) {
		if in.OpenAIBaseURL, err = r.openAIBaseURL(ctx, se.Namespace); err != nil {
			return network.Effective{}, err
		}
	}
	eff, err := network.Resolve(p, in)
	if err != nil {
		// A *network.Error stays reachable with errors.As; its Message is the run's failure.
		return network.Effective{}, fmt.Errorf("resolving the network of %s: %w", se.Name, err)
	}
	return eff, nil
}

// openAIBaseURL reads llm-keys openai-base-url; empty when the Secret or the key is missing.
func (r *SkillEvalReconciler) openAIBaseURL(ctx context.Context, namespace string) (string, error) {
	var s corev1.Secret
	err := r.Reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: llmKeysSecret}, &s)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s for %s: %w", llmKeysSecret, openAIBaseURLKey, err)
	}
	return string(s.Data[openAIBaseURLKey]), nil
}

// ensureEvalPolicy creates or updates the run's CiliumNetworkPolicy, owned by the SkillEval.
func (r *SkillEvalReconciler) ensureEvalPolicy(ctx context.Context, se *v1alpha1.SkillEval, eff network.Effective) error {
	want := network.EvalPolicy(se.Namespace, se.Name, eff)
	return applyPolicy(ctx, r.Client, want, se)
}

// applyPolicy creates or updates a CiliumNetworkPolicy: its labels and spec
// as rendered, and the owner when there is one.
func applyPolicy(ctx context.Context, c client.Client, want *unstructured.Unstructured, owner client.Object) error {
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(want.GroupVersionKind())
	got.SetNamespace(want.GetNamespace())
	got.SetName(want.GetName())
	_, err := controllerutil.CreateOrUpdate(ctx, c, got, func() error {
		labels := got.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		maps.Copy(labels, want.GetLabels())
		got.SetLabels(labels)
		got.Object["spec"] = want.Object["spec"]
		if owner == nil {
			return nil
		}
		if err := controllerutil.SetControllerReference(owner, got, c.Scheme()); err != nil {
			return fmt.Errorf("owning %s: %w", got.GetName(), err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("applying CiliumNetworkPolicy %s: %w", want.GetName(), err)
	}
	return nil
}

// ensureBaseline creates or updates the namespace's baseline policy when
// Cilium enforces network modes. It reports whether it applied it.
func ensureBaseline(ctx context.Context, c client.Client, checker crds.Checker, namespace string, p network.Policy) (bool, error) {
	present, err := crdPresent(ctx, checker, network.CiliumPolicyCRD)
	if err != nil || !present || !p.Cilium(present) {
		return false, err
	}
	if err := applyPolicy(ctx, c, network.BaselinePolicy(namespace), nil); err != nil {
		return false, err
	}
	return true, nil
}

// crdPresent asks the checker; a nil checker knows no CRDs.
func crdPresent(ctx context.Context, c crds.Checker, names ...string) (bool, error) {
	if c == nil {
		return false, nil
	}
	ok, err := crds.HasAll(ctx, c, names...)
	if err != nil {
		return false, fmt.Errorf("detecting optional CRDs: %w", err)
	}
	return ok, nil
}

// BaselinePolicy ensures the baseline CiliumNetworkPolicy once the manager
// starts, so warm trial pods have no egress before the first run. It is a
// manager Runnable that needs the leader.
type BaselinePolicy struct {
	// Client writes the policy.
	Client client.Client
	// CRDs detects Cilium.
	CRDs crds.Checker
	// Namespace is where trial pods run.
	Namespace string
	// Platform supplies the network policy in force.
	Platform *platform.Source
}

// Start implements manager.Runnable. A failure is logged, not fatal: the
// profile reconciler applies the policy again.
func (b *BaselinePolicy) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("baseline-policy")
	settings, err := b.Platform.Current(ctx)
	if err != nil {
		log.Error(err, "reading the platform settings, using the built-in defaults")
	}
	applied, err := ensureBaseline(ctx, b.Client, b.CRDs, b.Namespace, settings.Network)
	switch {
	case err != nil:
		log.Error(err, "ensuring the baseline network policy", "policy", network.BaselinePolicyName)
	case applied:
		log.Info("baseline network policy applied", "policy", network.BaselinePolicyName, "namespace", b.Namespace)
	}
	return nil
}

// NeedLeaderElection reports true: only the leader writes the policy.
func (b *BaselinePolicy) NeedLeaderElection() bool { return true }
