package v1alpha1

import (
	"regexp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// agentVersionPattern is what an agent version may look like. It ends up in a
// label value and in `npm install -g pkg@<version>` inside the trial sandbox,
// so it is letters, digits, dot, dash and underscore, ending in a letter or digit.
var agentVersionPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,61}[A-Za-z0-9])?$`)

// ValidAgentVersion reports whether v is an acceptable agent version, such as 2.1.292.
func ValidAgentVersion(v string) bool { return agentVersionPattern.MatchString(v) }

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
	// LabelAgentVersion holds the agent version of the spec; absent when the
	// entry pins none.
	LabelAgentVersion = "skillci.dev/agent-version"
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
	// VerdictRejected means Tier 1 or Tier 2 failed, so no live trials ran.
	VerdictRejected Verdict = "Rejected"
	// VerdictInconclusive means the score dropped below the stable version by
	// more than the tolerance, but the trials do not show it beyond noise.
	VerdictInconclusive Verdict = "Inconclusive"
)

// Tier and publication statuses reported by the runner.
const (
	// TierPassed means every check of the tier passed.
	TierPassed = "passed"
	// TierFailed means a check of the tier failed.
	TierFailed = "failed"
	// TierIncomplete means a check could not produce trustworthy evidence.
	TierIncomplete = "incomplete"
	// TierSkipped means the tier was not run.
	TierSkipped = "skipped"
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
	// AgentVersion is the release of the agent CLI to evaluate with, for
	// example 2.1.292. Empty means the runner's default pin for the agent.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`
	AgentVersion string `json:"agentVersion,omitempty"`
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
	// SandboxProfile names the SandboxProfile whose warm pool the trials run
	// in, copied from the model list entry (default: the SkillCIConfig
	// spec.defaultSandboxProfile). Empty means the default profile, and a bare
	// Sandbox per trial when that profile does not exist.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	SandboxProfile string `json:"sandboxProfile,omitempty"`
	// Network is the network the skill declared for its trials, or the
	// platform default. The controller fills it in from the OCI manifest
	// annotations of the digest before the run starts.
	// +optional
	Network *NetworkSpec `json:"network,omitempty"`
}

// Network modes of a trial sandbox.
const (
	// NetworkNone allows only the model API hosts.
	NetworkNone = "none"
	// NetworkAllowlist allows the model API hosts and the hosts the skill declares.
	NetworkAllowlist = "allowlist"
	// NetworkFull allows the internet, but not the cluster.
	NetworkFull = "full"
)

// Sources of a SkillEval's network mode.
const (
	// NetworkSourceSkill means the skill declared the mode (skillci.dev/network).
	NetworkSourceSkill = "skill"
	// NetworkSourceDefault means the skill declared none and the platform default applies.
	NetworkSourceDefault = "default"
)

// NetworkSpec is the network a skill's trials may use.
type NetworkSpec struct {
	// Mode is none, allowlist or full. A mode the platform does not allow
	// fails the run before any Job starts.
	// +kubebuilder:validation:MaxLength=32
	Mode string `json:"mode"`
	// Allow holds the hosts and @bundles the skill declared
	// (skillci.dev/network-allow), as declared. Used only with allowlist.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=64
	Allow []string `json:"allow,omitempty"`
	// Source says where the mode comes from: skill or default.
	// +kubebuilder:validation:Enum=skill;default
	Source string `json:"source"`
}

// NetworkStatus is the network the trials got.
type NetworkStatus struct {
	// Mode is the effective mode: none, allowlist or full.
	// +optional
	Mode string `json:"mode,omitempty"`
	// Policy is the CiliumNetworkPolicy that enforces the mode; empty when
	// nothing enforces it.
	// +optional
	Policy string `json:"policy,omitempty"`
	// Enforced reports whether a CiliumNetworkPolicy enforces the mode.
	Enforced bool `json:"enforced"`
	// Hosts are the hosts the trials may reach on TCP 443 with none and
	// allowlist, bundles expanded, sorted. Empty for full.
	// +optional
	// +listType=atomic
	Hosts []string `json:"hosts,omitempty"`
}

// TierStatuses holds the outcome of the three SkillEvaluator tiers. An empty
// value means the runner did not report the tier.
type TierStatuses struct {
	// Tier1 is the static, security and quality tier.
	// +optional
	Tier1 string `json:"tier1,omitempty"`
	// Tier2 is the deduplication tier.
	// +optional
	Tier2 string `json:"tier2,omitempty"`
	// Tier3 is the live with and without skill evaluation.
	// +optional
	Tier3 string `json:"tier3,omitempty"`
}

// Samples are the per-trial overall scores of a run, one entry per trial and
// two decimals, as strings so the CRD needs no floats. The statistics in the
// status are computed from them.
type Samples struct {
	// WithSkill holds the with-skill trial scores.
	// +optional
	// +listType=atomic
	WithSkill []string `json:"withSkill,omitempty"`
	// WithoutSkill holds the baseline trial scores.
	// +optional
	// +listType=atomic
	WithoutSkill []string `json:"withoutSkill,omitempty"`
}

// SkillEvalStatus is the observed state of a SkillEval.
type SkillEvalStatus struct {
	// Phase is the lifecycle phase.
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Planned
	Phase Phase `json:"phase,omitempty"`
	// JobName is the runner Job.
	JobName string `json:"jobName,omitempty"`
	// AgentVersion is the agent CLI release the trials ran, as the runner
	// reported it; until then the release the spec asks for.
	// +optional
	AgentVersion string `json:"agentVersion,omitempty"`
	// WithSkill is the score with the skill, two decimals.
	WithSkill string `json:"withSkill,omitempty"`
	// WithoutSkill is the score without the skill, two decimals.
	WithoutSkill string `json:"withoutSkill,omitempty"`
	// Lift is WithSkill minus WithoutSkill, signed, two decimals.
	Lift string `json:"lift,omitempty"`
	// WithSkillStdErr is the standard error of the with-skill trial scores, two decimals.
	// +optional
	WithSkillStdErr string `json:"withSkillStdErr,omitempty"`
	// WithoutSkillStdErr is the standard error of the baseline trial scores, two decimals.
	// +optional
	WithoutSkillStdErr string `json:"withoutSkillStdErr,omitempty"`
	// N is the number of trials behind the scores, with-skill/without-skill, for example 9/9.
	// +optional
	N string `json:"n,omitempty"`
	// LiftCI is the confidence interval of the lift (Welch's t), for example [+0.41, +0.73].
	// The level is the controller's --confidence.
	// +optional
	LiftCI string `json:"liftCI,omitempty"`
	// Samples are the per-trial scores the statistics come from; absent for
	// runs of a runner that does not report them.
	// +optional
	Samples *Samples `json:"samples,omitempty"`
	// Verdict classifies the lift.
	// +kubebuilder:validation:Enum=Helps;Neutral;Hurts;Obsolete;Regressed;Inconclusive;Rejected
	Verdict Verdict `json:"verdict,omitempty"`
	// Tiers is the outcome of the three SkillEvaluator tiers; absent when the
	// runner is older than the tiers.
	// +optional
	Tiers *TierStatuses `json:"tiers,omitempty"`
	// Publication is SkillEvaluator's publication assessment: passed, failed or incomplete.
	// +optional
	Publication string `json:"publication,omitempty"`
	// Findings names the failing tier and check, at most five short strings.
	// +optional
	// +listType=atomic
	Findings []string `json:"findings,omitempty"`
	// ReportDigest is the digest of the eval report attached to the skill in the registry.
	ReportDigest string `json:"reportDigest,omitempty"`
	// Retries counts the times the controller started a fresh Job after an
	// infrastructure failure. The first Job is not a retry; the Job of retry n
	// is named <skilleval>-rn. See the Retried condition for the failure.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Retries int `json:"retries,omitempty"`
	// Network is the network the trials got; absent for runs made before
	// network modes existed (they ran with full).
	// +optional
	Network *NetworkStatus `json:"network,omitempty"`
	// SandboxProfile is the SandboxProfile the run used; empty when it ran a
	// bare Sandbox per trial.
	// +optional
	SandboxProfile string `json:"sandboxProfile,omitempty"`
	// WarmPool is the SandboxWarmPool the runner claimed trial pods from
	// (SANDBOX_WARM_POOL); empty for a bare Sandbox per trial.
	// +optional
	WarmPool string `json:"warmPool,omitempty"`
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
// +kubebuilder:printcolumn:name="Agent",type=string,JSONPath=`.spec.agent`
// +kubebuilder:printcolumn:name="AgentVer",type=string,JSONPath=`.status.agentVersion`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="With",type=string,JSONPath=`.status.withSkill`
// +kubebuilder:printcolumn:name="Without",type=string,JSONPath=`.status.withoutSkill`
// +kubebuilder:printcolumn:name="Lift",type=string,JSONPath=`.status.lift`
// +kubebuilder:printcolumn:name="Verdict",type=string,JSONPath=`.status.verdict`
// +kubebuilder:printcolumn:name="Tier1",type=string,JSONPath=`.status.tiers.tier1`
// +kubebuilder:printcolumn:name="Network",type=string,JSONPath=`.status.network.mode`
// +kubebuilder:printcolumn:name="Lift CI",type=string,priority=1,JSONPath=`.status.liftCI`
// +kubebuilder:printcolumn:name="N",type=string,priority=1,JSONPath=`.status.n`
// +kubebuilder:printcolumn:name="Publication",type=string,priority=1,JSONPath=`.status.publication`
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

// EffectiveAgentVersion returns the agent version that identifies the run: the
// one the spec pins, else the one the runner reported. It is empty for a run
// that pinned none and has no result yet.
func (se *SkillEval) EffectiveAgentVersion() string {
	if se.Spec.AgentVersion != "" {
		return se.Spec.AgentVersion
	}
	return se.Status.AgentVersion
}

// Harness returns agent@version, or the agent alone while the version is unknown.
func (se *SkillEval) Harness() string {
	if v := se.EffectiveAgentVersion(); v != "" {
		return se.Spec.Agent + "@" + v
	}
	return se.Spec.Agent
}

// EntryKey identifies the model list entry of the run, agent@version/model.
// The tag policy compares and counts runs per entry.
func (se *SkillEval) EntryKey() string {
	return se.Harness() + "/" + se.Spec.Model
}
