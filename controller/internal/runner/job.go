// Package runner renders the runner Job for a SkillEval and reads its result.
package runner

import (
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
)

// Names from the runner Job contract in CONTRACT.md.
const (
	// ContainerName is the runner container; its termination message carries the result.
	ContainerName = "skill-eval"
	// ServiceAccount is the account the runner Job uses to create Sandboxes.
	ServiceAccount = "skill-eval-runner"
	authMount      = "/auth"
)

// Config says how Jobs are rendered.
type Config struct {
	// Image is the runner image.
	Image string
	// Registry is the host[:port] the skill is pulled from, without scheme.
	Registry string
	// Project is the registry project that holds skills.
	Project string
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
	secretEnv := func(name, key string, optional bool) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			Name:     "llm-keys",
			Key:      key,
			Optional: new(optional),
		}}}
	}
	labels := map[string]string{
		v1alpha1.LabelSkill:  se.Labels[v1alpha1.LabelSkill],
		v1alpha1.LabelDigest: se.Labels[v1alpha1.LabelDigest],
		v1alpha1.LabelModel:  se.Labels[v1alpha1.LabelModel],
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
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName: ServiceAccount,
					RestartPolicy:      corev1.RestartPolicyNever,
					ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "docr"}},
					Containers: []corev1.Container{{
						Name:  ContainerName,
						Image: cfg.Image,
						Env: []corev1.EnvVar{
							{Name: "SKILL_REF", Value: cfg.Registry + "/" + cfg.Project + "/" + se.Spec.Skill + "@" + se.Spec.Digest},
							{Name: "SKILL_NAME", Value: se.Spec.Skill},
							{Name: "AGENT", Value: se.Spec.Agent},
							{Name: "MODEL", Value: se.Spec.Model},
							{Name: "ATTEMPTS", Value: strconv.Itoa(se.Spec.Attempts)},
							secretEnv("ANTHROPIC_API_KEY", "anthropic", false),
							secretEnv("OPENAI_API_KEY", "openai", true),
						},
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
