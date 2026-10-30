package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Labels set on every SkillEval.
const (
	// LabelSkill holds the skill (repository) name.
	LabelSkill = "skillci.dev/skill"
	// LabelDigest holds the first 32 hex characters of the manifest digest.
	LabelDigest = "skillci.dev/digest"
	// LabelModel holds the model name.
	LabelModel = "skillci.dev/model"
	// LabelAgent holds the agent name.
	LabelAgent = "skillci.dev/agent"
)

// Phase is the lifecycle phase of a SkillEval.
type Phase string

// SkillEval phases.
const (
	// PhasePending means the eval waits for a Job slot.
	PhasePending Phase = "Pending"
	// PhaseRunning means the runner Job exists and has not finished.
	PhaseRunning Phase = "Running"
	// PhaseSucceeded means the runner produced scores.
	PhaseSucceeded Phase = "Succeeded"
	// PhaseFailed means the runner failed or produced no scores.
	PhaseFailed Phase = "Failed"
	// PhasePlanned means a dry run recorded the eval without creating a Job.
	PhasePlanned Phase = "Planned"
)

// Trigger says why a SkillEval was created.
type Trigger string

// SkillEval triggers.
const (
	// TriggerPush is a skill version pushed to the registry.
	TriggerPush Trigger = "push"
	// TriggerModelChange is a model added to the model list.
	TriggerModelChange Trigger = "model-change"
	// TriggerManual is a SkillEval created by hand.
	TriggerManual Trigger = "manual"
)

// Verdict is the outcome of comparing the with-skill and without-skill scores.
type Verdict string

// SkillEval verdicts.
const (
	// VerdictHelps means the skill raised the score.
	VerdictHelps Verdict = "Helps"
	// VerdictNeutral means the skill made no meaningful difference.
	VerdictNeutral Verdict = "Neutral"
	// VerdictHurts means the skill lowered the score.
	VerdictHurts Verdict = "Hurts"
	// VerdictObsolete means the model already does well without the skill.
	VerdictObsolete Verdict = "Obsolete"
	// VerdictRegressed means the version scores worse than the stable version.
	VerdictRegressed Verdict = "Regressed"
)

// SkillEvalSpec identifies one skill digest and one model to evaluate it with.
type SkillEvalSpec struct {
	// Skill is the repository name in the skills project.
	// +kubebuilder:validation:MinLength=1
	Skill string `json:"skill"`
	// Version is the semver tag that was pushed.
	// +kubebuilder:validation:Pattern=`^v\d+\.\d+\.\d+$`
	Version string `json:"version"`
	// Digest is the manifest digest, the identity of the version.
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	Digest string `json:"digest"`
	// Agent is the agent harness, for example claude-code.
	// +kubebuilder:validation:MinLength=1
	Agent string `json:"agent"`
	// Model is the pinned model name.
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`
	// Required marks the model as one that gates promotion.
	// +optional
	Required bool `json:"required,omitempty"`
	// Attempts is the number of trials per arm.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Attempts int `json:"attempts,omitempty"`
	// Trigger records why the eval exists.
	// +kubebuilder:validation:Enum=push;model-change;manual
	// +kubebuilder:default=manual
	Trigger Trigger `json:"trigger,omitempty"`
}

// SkillEvalStatus is the observed state of a SkillEval.
type SkillEvalStatus struct {
	// Phase is the lifecycle phase.
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Planned
	Phase Phase `json:"phase,omitempty"`
	// JobName is the runner Job.
	JobName string `json:"jobName,omitempty"`
	// WithSkill is the score with the skill, two decimals.
	WithSkill string `json:"withSkill,omitempty"`
	// WithoutSkill is the score without the skill, two decimals.
	WithoutSkill string `json:"withoutSkill,omitempty"`
	// Lift is WithSkill minus WithoutSkill, signed, two decimals.
	Lift string `json:"lift,omitempty"`
	// Verdict classifies the lift.
	// +kubebuilder:validation:Enum=Helps;Neutral;Hurts;Obsolete;Regressed
	Verdict Verdict `json:"verdict,omitempty"`
	// ReportDigest is the digest of the eval report attached to the skill in the registry.
	ReportDigest string `json:"reportDigest,omitempty"`
	// Message is a human readable detail, for example a failure reason.
	Message string `json:"message,omitempty"`
	// CompletedAt is when the eval reached Succeeded or Failed.
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// Conditions record what the tag policy did with this result.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// SkillEval is one evaluation of a skill digest with one model.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=se
// +kubebuilder:printcolumn:name="Skill",type=string,JSONPath=`.spec.skill`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="With",type=string,JSONPath=`.status.withSkill`
// +kubebuilder:printcolumn:name="Without",type=string,JSONPath=`.status.withoutSkill`
// +kubebuilder:printcolumn:name="Lift",type=string,JSONPath=`.status.lift`
// +kubebuilder:printcolumn:name="Verdict",type=string,JSONPath=`.status.verdict`
// +kubebuilder:printcolumn:name="Report",type=string,priority=1,JSONPath=`.status.reportDigest`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type SkillEval struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SkillEvalSpec   `json:"spec"`
	Status SkillEvalStatus `json:"status,omitempty"`
}

// SkillEvalList is a list of SkillEvals.
// +kubebuilder:object:root=true
type SkillEvalList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SkillEval `json:"items"`
}
