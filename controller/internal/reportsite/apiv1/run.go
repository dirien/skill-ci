package apiv1

import "time"

// RunDetail is the response of GET /api/v1/runs/{name}[?job=<job name>].
type RunDetail struct {
	// Run is the summary row of the run.
	Run RunSummary `json:"run"`
	// SpecAgentVersion is the release spec.agentVersion pins, left out for an unpinned entry.
	SpecAgentVersion string `json:"specAgentVersion,omitempty"`
	// Channels are the tags the digest carries.
	Channels []string `json:"channels"`
	// WithSkillStdErr and WithoutSkillStdErr are the standard errors of the trial scores.
	WithSkillStdErr    *float64 `json:"withSkillStdErr,omitempty"`
	WithoutSkillStdErr *float64 `json:"withoutSkillStdErr,omitempty"`
	// Samples are the per-trial scores behind the statistics.
	Samples Samples `json:"samples"`
	// Findings are the short strings naming a failing tier and check.
	Findings []string `json:"findings"`
	// Conditions are the status conditions.
	Conditions []Condition `json:"conditions"`
	// Regressed, Inconclusive and Retried are the messages of those conditions when true.
	Regressed    string `json:"regressed,omitempty"`
	Inconclusive string `json:"inconclusive,omitempty"`
	Retried      string `json:"retried,omitempty"`
	// Network is what the trials could reach.
	Network RunNetwork `json:"network"`
	// Sandbox is where the trials ran.
	Sandbox Sandbox `json:"sandbox"`
	// Report is the stored SkillEvaluator report of the selected attempt.
	Report Report `json:"report"`
	// Trials is the trial grid of the selected attempt.
	Trials TrialGrid `json:"trials"`
	// Attempts are the Jobs of the run, oldest first (status.attempts plus the current Job).
	Attempts []RunAttempt `json:"attempts"`
	// RefreshSeconds is how often to poll, 0 when the run is finished.
	RefreshSeconds int `json:"refreshSeconds"`
}

// Samples are the per-trial overall scores of each arm.
type Samples struct {
	// WithSkill and WithoutSkill hold up to 50 scores each.
	WithSkill    []float64 `json:"withSkill"`
	WithoutSkill []float64 `json:"withoutSkill"`
}

// Condition is one status condition of a SkillEval.
type Condition struct {
	// Type, Status, Reason and Message are copied from the SkillEval.
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	// ChangedAt is the last transition time.
	ChangedAt time.Time `json:"changedAt"`
}

// RunNetwork is the network of one run.
type RunNetwork struct {
	// Mode is none, allowlist or full; left out for runs before network modes (Legacy).
	Mode string `json:"mode,omitempty"`
	// Source is skill or default.
	Source string `json:"source,omitempty"`
	// Declared are the hosts and @bundles the skill declared.
	Declared []string `json:"declared"`
	// Hosts are the hosts the trials could reach on TCP 443, bundles expanded.
	Hosts []string `json:"hosts"`
	// Policy is the enforcing CiliumNetworkPolicy, left out when none enforced the mode.
	Policy string `json:"policy,omitempty"`
	// Enforced reports whether a policy enforced the mode.
	Enforced bool `json:"enforced"`
	// Reason and Message are those of the NetworkPolicy condition.
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	// Legacy marks a run made before network modes: it ran with full and no policy.
	Legacy bool `json:"legacy"`
}

// Sandbox is where the trials of a run ran.
type Sandbox struct {
	// Profile is the SandboxProfile the run used; left out for a bare Sandbox per trial.
	Profile string `json:"profile,omitempty"`
	// SpecProfile is the profile the spec names; left out for the controller default.
	SpecProfile string `json:"specProfile,omitempty"`
	// WarmPool is the SandboxWarmPool the trials were claimed from.
	WarmPool string `json:"warmPool,omitempty"`
}

// Report states.
const (
	// ReportNone means no report digest is stored.
	ReportNone = "none"
	// ReportMissing means the digest is stored but the registry no longer has it.
	ReportMissing = "missing"
	// ReportError means the registry or the file could not be read.
	ReportError = "error"
	// ReportFiles means Tier 3 did not run, so only the stored files are offered.
	ReportFiles = "files"
	// ReportOK means the dimension scores were read.
	ReportOK = "ok"
)

// Report is the stored SkillEvaluator report of one run attempt.
type Report struct {
	// State is none, missing, error, files or ok.
	State string `json:"state"`
	// Error is the error text for the error state.
	Error string `json:"error,omitempty"`
	// Digest is the report referrer digest.
	Digest string `json:"digest,omitempty"`
	// Dimensions are the six SkillEvaluator dimensions, then the overall row (state ok).
	Dimensions []Dimension `json:"dimensions"`
	// Activation is SkillEvaluator's skill activation observation, when reported.
	Activation *Activation `json:"activation,omitempty"`
	// Files are links to the stored files (report.html, result.json).
	Files []ReportFile `json:"files"`
}

// Dimension compares one SkillEvaluator dimension with and without the skill.
type Dimension struct {
	// Key is the SkillEvaluator metric key, for example behavior_check.
	Key string `json:"key"`
	// Label is the display name.
	Label string `json:"label"`
	// WithSkill and WithoutSkill are scores from 0 to 1; Delta is their difference.
	WithSkill    float64 `json:"withSkill"`
	WithoutSkill float64 `json:"withoutSkill"`
	Delta        float64 `json:"delta"`
	// Overall marks the summary row.
	Overall bool `json:"overall"`
}

// Activation is whether the agent used the skill, over the with-skill trials.
type Activation struct {
	// Status is the reported status, for example activated.
	Status string `json:"status"`
	// Activated and Positive count trials where the skill was used and where it could be.
	Activated int `json:"activated"`
	Positive  int `json:"positive"`
}

// ReportFile is a stored file of the report, served outside the API.
type ReportFile struct {
	// Name is the file name, for example report.html.
	Name string `json:"name"`
	// Href is the site path, for example /runs/{name}/report.
	Href string `json:"href"`
}

// RunAttempt is one Job of a run. Earlier attempts come from status.attempts.
type RunAttempt struct {
	// Number is 1 for the first Job, 2 for the first retry.
	Number int `json:"number"`
	// JobName is the Job, the value of the job query parameter.
	JobName string `json:"jobName"`
	// ReportDigest is the report the attempt attached, when it attached one.
	ReportDigest string `json:"reportDigest,omitempty"`
	// Message is why the attempt ended (the failure of a retried Job).
	Message string `json:"message,omitempty"`
	// Current marks the attempt that status.reportDigest belongs to.
	Current bool `json:"current"`
	// Trials is stored, withheld, none or unknown for this attempt.
	Trials string `json:"trials"`
}

// Trial grid states.
const (
	// GridStored means the grid comes from the trial bundle index and links to sessions.
	GridStored = "stored"
	// GridWithheld means the runner withheld the bundle; Reason says why.
	GridWithheld = "withheld"
	// GridFallback means there is no bundle and the grid comes from result.json: scores, no links.
	GridFallback = "fallback"
	// GridNone means there is nothing to show: no report, or Tier 3 did not run.
	GridNone = "none"
	// GridMissing means the bundle is referenced but gone from the registry.
	GridMissing = "missing"
	// GridError means the bundle or result.json could not be read; Reason has the error.
	GridError = "error"
)

// TrialGrid is the cases x attempts x arm matrix of one run attempt.
type TrialGrid struct {
	// State is stored, withheld, fallback, none, missing or error.
	State string `json:"state"`
	// Reason explains withheld, missing, error and none.
	Reason string `json:"reason,omitempty"`
	// Job is the Job name of the attempt the grid belongs to.
	Job string `json:"job,omitempty"`
	// BundleDigest is the trial bundle referrer digest (state stored).
	BundleDigest string `json:"bundleDigest,omitempty"`
	// AttemptsPerCase is the number of attempt columns per arm.
	AttemptsPerCase int `json:"attemptsPerCase"`
	// Cases are the eval cases in dataset order.
	Cases []GridCase `json:"cases"`
	// Totals sum the trials of the grid.
	Totals TrialTotals `json:"totals"`
	// Credentials name the provider routes the trials used, names only.
	Credentials []Credential `json:"credentials"`
	// Network sums the flows of all trials, when flows were collected.
	Network *NetworkSummary `json:"network,omitempty"`
	// Withheld lists trials the runner left out of the bundle.
	Withheld []WithheldTrial `json:"withheld"`
}

// GridCase is one eval case row of the trial grid.
type GridCase struct {
	// ID is the eval case id.
	ID string `json:"id"`
	// Question is the case prompt, at most 300 characters.
	Question string `json:"question"`
	// ExpectedBehaviors are the behaviors the judge checks.
	ExpectedBehaviors []string `json:"expectedBehaviors"`
	// WithSkill and WithoutSkill hold one cell per attempt (index attempt-1), null when missing.
	WithSkill    []*TrialCell `json:"withSkill"`
	WithoutSkill []*TrialCell `json:"withoutSkill"`
}

// Trial statuses.
const (
	// StatusScored means the verifier wrote a reward.
	StatusScored = "scored"
	// StatusUnscored means there is no reward: the trial failed before or during scoring.
	StatusUnscored = "unscored"
	// StatusError means the runner could not read the trial; only its metadata is stored.
	StatusError = "error"
	// StatusWithheld means the runner left the trial out of the bundle.
	StatusWithheld = "withheld"
)

// TrialCell is one trial in the grid, from the bundle index (or result.json for a fallback grid).
type TrialCell struct {
	// ID is the Harbor trial name, for example rn-basic__sryqofs.
	ID string `json:"id"`
	// Arm is with-skill or without-skill.
	Arm string `json:"arm"`
	// Case is the eval case id and Attempt the 1-based attempt.
	Case    string `json:"case"`
	Attempt int    `json:"attempt"`
	// Status is scored, unscored, error or withheld.
	Status string `json:"status"`
	// Score is the overall score, and Passed whether it reached the pass threshold.
	Score  *float64 `json:"score,omitempty"`
	Passed *bool    `json:"passed,omitempty"`
	// ErrorType is the exception type of an unscored trial.
	ErrorType string `json:"errorType,omitempty"`
	// DurationSec is the whole trial; AgentSec the agent execution phase.
	DurationSec *float64 `json:"durationSec,omitempty"`
	AgentSec    *float64 `json:"agentSec,omitempty"`
	// Steps and ToolCalls count trajectory steps and tool calls.
	Steps     int `json:"steps"`
	ToolCalls int `json:"toolCalls"`
	// Tokens are the trajectory's final totals, when known.
	TokensIn     *int64 `json:"tokensIn,omitempty"`
	TokensOut    *int64 `json:"tokensOut,omitempty"`
	TokensCached *int64 `json:"tokensCached,omitempty"`
	// CostUSD is the total cost; CostEstimated is true when it is a LiteLLM estimate.
	CostUSD       *float64 `json:"costUsd,omitempty"`
	CostEstimated bool     `json:"costEstimated"`
	// Criteria counts the expected behaviors that passed.
	Criteria *Ratio `json:"criteria,omitempty"`
	// SkillInvoked is SkillEvaluator's skill_invoked, when reported.
	SkillInvoked *bool `json:"skillInvoked,omitempty"`
	// HasSession reports whether a trial document exists, so the cell links to the session.
	HasSession bool `json:"hasSession"`
	// Dropped counts DROPPED flow rows of the trial.
	Dropped int `json:"dropped"`
}

// TrialTotals sum the trials of a grid.
type TrialTotals struct {
	// Trials counts all cells; Scored, Unscored and Withheld count them by status.
	Trials   int `json:"trials"`
	Scored   int `json:"scored"`
	Unscored int `json:"unscored"`
	Withheld int `json:"withheld"`
	// Tokens sum the known token totals.
	TokensIn     int64 `json:"tokensIn"`
	TokensOut    int64 `json:"tokensOut"`
	TokensCached int64 `json:"tokensCached"`
	// CostUSD sums the known costs; CostEstimated is true when any part is an estimate.
	CostUSD       float64 `json:"costUsd"`
	CostEstimated bool    `json:"costEstimated"`
	// AgentSec and DurationSec sum the known times.
	AgentSec    float64 `json:"agentSec"`
	DurationSec float64 `json:"durationSec"`
}

// Credential is one provider route of a run, names only, never values.
type Credential struct {
	// Role is agent (the route of the agent under test) or judge.
	Role string `json:"role"`
	// Env is the variable the route reads, for example OPENAI_API_KEY.
	Env string `json:"env"`
	// Host is the endpoint host, for example ollama.com.
	Host string `json:"host"`
}

// NetworkSummary sums the flows of a set of trials.
type NetworkSummary struct {
	// State is collected, partial, unavailable or off.
	State string `json:"state"`
	// Reason says why flows are partial, unavailable or off.
	Reason string `json:"reason,omitempty"`
	// Forwarded and Dropped count flow rows by verdict.
	Forwarded int `json:"forwarded"`
	Dropped   int `json:"dropped"`
}

// WithheldTrial is a trial the runner left out of the bundle.
type WithheldTrial struct {
	// ID, Arm, Case and Attempt identify the trial.
	ID      string `json:"id"`
	Arm     string `json:"arm"`
	Case    string `json:"case"`
	Attempt int    `json:"attempt"`
	// Reason is secret-scan, size, limit or unreadable.
	Reason string `json:"reason"`
}
