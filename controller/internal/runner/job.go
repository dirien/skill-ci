// Package runner renders the runner Job for a SkillEval and reads its result.
package runner

import (
	"fmt"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
)

// SafeToEvictAnnotation tells the cluster autoscaler not to evict a pod during scale-down.
const SafeToEvictAnnotation = "cluster-autoscaler.kubernetes.io/safe-to-evict"

// Names from the runner Job contract in CONTRACT.md.
const (
	// ContainerName is the runner container; its termination message carries the result.
	ContainerName = "skill-eval"
	// ServiceAccount is the account the runner Job uses to create Sandboxes.
	ServiceAccount = "skill-eval-runner"
	authMount      = "/auth"
)

// llmKey maps an entry of the llm-keys Secret to an environment variable.
type llmKey struct {
	// Env is the variable the runner reads.
	Env string
	// Key is the key in the llm-keys Secret.
	Key string
	// Optional keys may be missing from the Secret.
	Optional bool
}

// LLMKeys are the llm-keys entries every runner Job receives. anthropic is
// required: it is the judge whatever agent runs. The others are optional and
// only matter for the agent under test: openai (and openai-base-url) for
// Codex and for Tier 2 embeddings; ollama, openrouter and nvidia for OpenCode
// models that live there (ollama-cloud/..., openrouter/..., nvidia/...).
var LLMKeys = []llmKey{
	{"ANTHROPIC_API_KEY", "anthropic", false},
	{"OPENAI_API_KEY", "openai", true},
	{"OPENAI_BASE_URL", "openai-base-url", true},
	{"NVIDIA_API_KEY", "nvidia", true},
	{"OLLAMA_API_KEY", "ollama", true},
	{"OPENROUTER_API_KEY", "openrouter", true},
}

// Config says how Jobs are rendered.
type Config struct {
	// Image is the runner image.
	Image string
	// Registry is the host[:port] the skill is pulled from, without scheme.
	Registry string
	// Project is the registry project that holds skills.
	Project string
	// SandboxImage is the generic sandbox image every trial of the run starts from
	// (SANDBOX_IMAGE). Empty leaves the runner's own default: the sandbox image of its release.
	SandboxImage string
	// ImagePullSecrets name the Secrets that pull the runner image and, as SANDBOX_PULL_SECRETS,
	// the sandbox image. None by default: the images are public.
	ImagePullSecrets []string
}

// ParsePullSecrets splits a comma separated list of Secret names, as the --image-pull-secrets
// flag takes it, and rejects anything that is not a Kubernetes name.
func ParsePullSecrets(list string) ([]string, error) {
	var names []string
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
			return nil, fmt.Errorf("image pull secret %q: %s", name, strings.Join(errs, "; "))
		}
		names = append(names, name)
	}
	return names, nil
}

// pullSecretRefs returns the pod's imagePullSecrets, nil when there are none.
func pullSecretRefs(names []string) []corev1.LocalObjectReference {
	var refs []corev1.LocalObjectReference
	for _, name := range names {
		refs = append(refs, corev1.LocalObjectReference{Name: name})
	}
	return refs
}

// maxJobName is the longest Job name that still fits the job-name label of its pods.
const maxJobName = 63

// JobName returns the name of the Job for the SkillEval's current attempt: the
// SkillEval's name, then <name>-r1, <name>-r2 and so on for each retry. A name
// too long for the suffix is cut short before it, so the result fits the
// 63 characters a label value allows.
func JobName(se *v1alpha1.SkillEval) string {
	if se.Status.Retries <= 0 {
		return se.Name
	}
	suffix := "-r" + strconv.Itoa(se.Status.Retries)
	base := se.Name
	if len(base)+len(suffix) > maxJobName {
		base = strings.TrimRight(base[:maxJobName-len(suffix)], "-")
	}
	return base + suffix
}

// NewJob renders the Job that evaluates one SkillEval. The Job is named by
// JobName and is owned by the SkillEval.
func NewJob(se *v1alpha1.SkillEval, cfg Config) *batchv1.Job {
	env := []corev1.EnvVar{
		{Name: "SKILL_REF", Value: cfg.Registry + "/" + cfg.Project + "/" + se.Spec.Skill + "@" + se.Spec.Digest},
		{Name: "SKILL_NAME", Value: se.Spec.Skill},
		{Name: "AGENT", Value: se.Spec.Agent},
		{Name: "MODEL", Value: se.Spec.Model},
		{Name: "ATTEMPTS", Value: strconv.Itoa(se.Spec.Attempts)},
	}
	if se.Spec.AgentVersion != "" { // without it the runner uses its default pin for the agent
		env = append(env, corev1.EnvVar{Name: "AGENT_VERSION", Value: se.Spec.AgentVersion})
	}
	if cfg.SandboxImage != "" {
		env = append(env, corev1.EnvVar{Name: "SANDBOX_IMAGE", Value: cfg.SandboxImage})
	}
	if len(cfg.ImagePullSecrets) > 0 {
		env = append(env, corev1.EnvVar{Name: "SANDBOX_PULL_SECRETS", Value: strings.Join(cfg.ImagePullSecrets, ",")})
	}
	for _, k := range LLMKeys {
		env = append(env, corev1.EnvVar{Name: k.Env, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			Name:     "llm-keys",
			Key:      k.Key,
			Optional: new(k.Optional),
		}}})
	}
	labels := map[string]string{
		v1alpha1.LabelSkill:  se.Labels[v1alpha1.LabelSkill],
		v1alpha1.LabelDigest: se.Labels[v1alpha1.LabelDigest],
		v1alpha1.LabelModel:  se.Labels[v1alpha1.LabelModel],
	}
	for _, key := range []string{v1alpha1.LabelAgent, v1alpha1.LabelAgentVersion} {
		if v := se.Labels[key]; v != "" {
			labels[key] = v
		}
	}
	return &batchv1.Job{
		Name:      JobName(se),
		Namespace: se.Namespace,
		Labels:    labels,
		OwnerReferences: []metav1.OwnerReference{
			*metav1.NewControllerRef(se, v1alpha1.GroupVersion.WithKind("SkillEval")),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            new(int32(0)),
			TTLSecondsAfterFinished: new(int32(3600)),
			// A stuck run (image pull backoff, hung agent) must free its slot.
			ActiveDeadlineSeconds: new(int64(2 * 60 * 60)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					// A scale-down must not kill an eval that runs for half an hour.
					Annotations: map[string]string{SafeToEvictAnnotation: "false"},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: ServiceAccount,
					RestartPolicy:      corev1.RestartPolicyNever,
					ImagePullSecrets:   pullSecretRefs(cfg.ImagePullSecrets),
					Containers: []corev1.Container{{
						Name:         ContainerName,
						Image:        cfg.Image,
						Env:          env,
						VolumeMounts: []corev1.VolumeMount{{Name: "harbor-pull", MountPath: authMount, ReadOnly: true}},
						// If the runner dies before writing a result, the log tail becomes the message.
						TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
					}},
					Volumes: []corev1.Volume{{
						Name: "harbor-pull",
						Secret: &corev1.SecretVolumeSource{
							SecretName: "harbor-pull",
							Items:      []corev1.KeyToPath{{Key: ".dockerconfigjson", Path: "config.json"}},
						},
					}},
				},
			},
		},
	}
}
