package apiv1

import (
	"encoding/json"
	"time"
)

// Prefix is the path prefix of every API route.
const Prefix = "/api/v1"

// Arms of a trial: the skill installed, or the baseline without it.
const (
	// ArmWithSkill is the arm that runs with the skill installed.
	ArmWithSkill = "with-skill"
	// ArmWithoutSkill is the baseline arm.
	ArmWithoutSkill = "without-skill"
)

// Trials states of a run, a run attempt or a trial grid.
const (
	// TrialsStored means the report references a trial bundle.
	TrialsStored = "stored"
	// TrialsWithheld means the runner withheld the whole bundle; the reason is given.
	TrialsWithheld = "withheld"
	// TrialsNone means the run has no bundle: no report, or a report from a runner before trial bundles.
	TrialsNone = "none"
	// TrialsUnknown means the report manifest has not been read yet or could not be read.
	TrialsUnknown = "unknown"
)

// Log entry categories (the filter chips of the session log).
const (
	// CategoryMsg is agent message text.
	CategoryMsg = "msg"
	// CategoryUser is a user turn: the prompt and later user steps.
	CategoryUser = "user"
	// CategoryThink is agent reasoning content.
	CategoryThink = "think"
	// CategoryTools is a tool call with its observation.
	CategoryTools = "tools"
	// CategoryWeb is a web fetch, web search or a shell command that calls the network.
	CategoryWeb = "web"
	// CategoryPkg is a package install command (pip, uv, npm, pnpm, yarn, apt-get, apk, cargo).
	CategoryPkg = "pkg"
	// CategorySkill is a skill activation, a read of a SKILL.md or the skill base directory turn.
	CategorySkill = "skill"
	// CategoryNet is a network flow from Hubble placed by time.
	CategoryNet = "net"
	// CategoryJudge is a judge entry after the last step: one per metric.
	CategoryJudge = "judge"
	// CategoryUsage is the token and cost usage of a step.
	CategoryUsage = "usage"
	// CategoryMeta is system steps, copied context and step extras.
	CategoryMeta = "meta"
)

// Categories lists every log entry category in display order.
var Categories = []string{
	CategoryMsg, CategoryUser, CategoryThink, CategoryTools, CategoryWeb, CategoryPkg,
	CategorySkill, CategoryNet, CategoryJudge, CategoryUsage, CategoryMeta,
}

// Interval is a confidence interval, of a lift or of a difference.
type Interval struct {
	// Lower and Upper are the bounds.
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
	// Level is the confidence level as text, for example "95%".
	Level string `json:"level"`
}

// Tiers are the SkillEvaluator tier statuses a run reported: passed, failed,
// incomplete or skipped. A tier the runner did not report is left out.
type Tiers struct {
	// Tier1 is static, security and quality checks.
	Tier1 string `json:"tier1,omitempty"`
	// Tier2 is deduplication against the other skills.
	Tier2 string `json:"tier2,omitempty"`
	// Tier3 is the live with and without skill evaluation.
	Tier3 string `json:"tier3,omitempty"`
}

// Ratio counts passes out of a total.
type Ratio struct {
	// Passed is the number that passed.
	Passed int `json:"passed"`
	// Total is the number checked.
	Total int `json:"total"`
}

// Pagination describes one page of a list.
type Pagination struct {
	// Page is the 1-based page number.
	Page int `json:"page"`
	// PageSize is the number of items per page.
	PageSize int `json:"pageSize"`
	// Total is the number of items over all pages.
	Total int `json:"total"`
	// Pages is the number of pages, at least 1.
	Pages int `json:"pages"`
}

// Thresholds are the controller policy bands (controller flags).
type Thresholds struct {
	// Helps is the lift at or above which a verdict is Helps.
	Helps float64 `json:"helps"`
	// Hurts is the lift at or below which a verdict is Hurts.
	Hurts float64 `json:"hurts"`
	// Promote is the lift every required model needs for stable.
	Promote float64 `json:"promote"`
	// Demote is the lift on any required model that removes stable.
	Demote float64 `json:"demote"`
	// Obsolete is the without-skill score at which a Neutral result is Obsolete.
	Obsolete float64 `json:"obsolete"`
	// Regression is the tolerated with-skill drop below the stable version.
	Regression float64 `json:"regression"`
	// Confidence is the level of the intervals, for example 0.95.
	Confidence float64 `json:"confidence"`
}

// Site is the response of GET /api/v1/site: what the app shell shows on every page.
type Site struct {
	// Namespace is where the SkillEvals live.
	Namespace string `json:"namespace"`
	// Project is the Harbor project that holds the skills.
	Project string `json:"project"`
	// Version is the controller release.
	Version string `json:"version"`
	// Thresholds are the policy bands in force.
	Thresholds Thresholds `json:"thresholds"`
	// GeneratedAt is when the response was made.
	GeneratedAt time.Time `json:"generatedAt"`
}

// Overview is the response of GET /api/v1/overview.
type Overview struct {
	// GeneratedAt is when the response was made.
	GeneratedAt time.Time `json:"generatedAt"`
	// RefreshSeconds is how often the page should poll, 0 when nothing is pending or running.
	RefreshSeconds int `json:"refreshSeconds"`
	// Counts are the headline numbers.
	Counts Counts `json:"counts"`
	// Attention lists what needs a look: failed runs, held back and rejected versions, running runs.
	Attention []AttentionItem `json:"attention"`
	// Recent are the 20 newest runs.
	Recent []RunSummary `json:"recent"`
	// Channels are the release channels per skill.
	Channels []ChannelRow `json:"channels"`
	// ChannelsError is set when Harbor could not be read; Channels is then empty.
	ChannelsError string `json:"channelsError,omitempty"`
	// Network is the platform network policy in force.
	Network NetworkPolicy `json:"network"`
}

// Counts are the headline numbers of the overview.
type Counts struct {
	// Runs counts all SkillEvals; Pending, Running, Succeeded, Failed and
	// Planned count them by phase and add up to Runs.
	Runs      int `json:"runs"`
	Pending   int `json:"pending"`
	Running   int `json:"running"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Planned   int `json:"planned"`
	// Skills counts skills known to the cluster or the registry.
	Skills int `json:"skills"`
	// OnStable counts skills with a stable version.
	OnStable int `json:"onStable"`
	// HeldBack counts versions the regression gate holds back.
	HeldBack int `json:"heldBack"`
	// Rejected counts versions that failed Tier 1 or Tier 2 on a required model.
	Rejected int `json:"rejected"`
}

// Attention kinds.
const (
	// AttentionFailed is a run that ended Failed.
	AttentionFailed = "failed"
	// AttentionHeldBack is a version the regression gate holds back (Regressed or Inconclusive).
	AttentionHeldBack = "held-back"
	// AttentionRejected is a version that failed Tier 1 or Tier 2 on a required model.
	AttentionRejected = "rejected"
	// AttentionRunning is a run in phase Running.
	AttentionRunning = "running"
	// AttentionPending is a run in phase Pending, waiting for its Job to start.
	AttentionPending = "pending"
)

// AttentionItem is one entry of the "needs attention" list.
type AttentionItem struct {
	// Kind is failed, held-back, rejected, running or pending.
	Kind string `json:"kind"`
	// Skill and Version name the skill version.
	Skill   string `json:"skill"`
	Version string `json:"version"`
	// Run is the SkillEval the item points at; empty for a version-level item without one run.
	Run string `json:"run,omitempty"`
	// Entry is agent@version/model of the run, when there is one.
	Entry string `json:"entry,omitempty"`
	// Message says why, in one sentence.
	Message string `json:"message"`
	// Since is when the state began (completion or creation of the run).
	Since time.Time `json:"since"`
}

// ChannelRow is one skill in the release channels table.
type ChannelRow struct {
	// Skill is the skill name.
	Skill string `json:"skill"`
	// Stable and Beta are the versions that hold the tags, left out when none does.
	Stable string `json:"stable,omitempty"`
	Beta   string `json:"beta,omitempty"`
	// HeldBack lists versions the regression gate holds back, newest first.
	HeldBack []string `json:"heldBack"`
	// Rejected lists versions that failed Tier 1 or Tier 2 on a required model, newest first.
	Rejected []string `json:"rejected"`
	// Versions counts the evaluated digests.
	Versions int `json:"versions"`
}

// NetworkPolicy is the platform network policy in force (SkillCIConfig default).
type NetworkPolicy struct {
	// Default is the mode of a skill that declares none.
	Default string `json:"default"`
	// Modes are the modes a skill may declare.
	Modes []string `json:"modes"`
	// Bundles are the bundle names without the @.
	Bundles []string `json:"bundles"`
	// Enforcement is the configured setting: auto, cilium or off.
	Enforcement string `json:"enforcement"`
	// EnforcedBy is what enforcement resolves to: cilium or none; left out when unknown.
	EnforcedBy string `json:"enforcedBy,omitempty"`
	// Source is SkillCIConfig, LastValid or BuiltIn.
	Source string `json:"source"`
	// SourceText says where the policy comes from, in words.
	SourceText string `json:"sourceText"`
	// HubbleRelay is the Hubble Relay address of spec.observability.hubbleRelay; empty when flows are off.
	HubbleRelay string `json:"hubbleRelay,omitempty"`
	// Problem is set when the SkillCIConfig is invalid or unreadable.
	Problem string `json:"problem,omitempty"`
}

// RunSummary is one SkillEval as a row of a runs table.
type RunSummary struct {
	// Name is the SkillEval name, the key of /runs/{name}.
	Name string `json:"name"`
	// Skill, Version and Digest identify the evaluated skill version.
	Skill   string `json:"skill"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	// Agent is the harness: claude-code, codex or opencode.
	Agent string `json:"agent"`
	// AgentVersion is the release the spec pins, else the one the runner reported.
	AgentVersion string `json:"agentVersion,omitempty"`
	// RanVersion is the release the runner reported for the trials.
	RanVersion string `json:"ranVersion,omitempty"`
	// Model is the model under test.
	Model string `json:"model"`
	// Entry is agent@version/model, the unit the tag policy compares.
	Entry string `json:"entry"`
	// Required marks entries that gate promotion.
	Required bool `json:"required"`
	// Attempts is the number of trials per arm and eval case.
	Attempts int `json:"attempts"`
	// Trigger is push, model-change or manual.
	Trigger string `json:"trigger"`
	// PushedBy is the Harbor user who pushed the version (annotation skillci.dev/pushed-by).
	PushedBy string `json:"pushedBy,omitempty"`
	// Phase is Pending, Running, Succeeded, Failed or Planned.
	Phase string `json:"phase"`
	// Verdict is the stored verdict, left out before a result exists.
	Verdict string `json:"verdict,omitempty"`
	// WithSkill, WithoutSkill and Lift are the scores, left out while unknown.
	WithSkill    *float64 `json:"withSkill,omitempty"`
	WithoutSkill *float64 `json:"withoutSkill,omitempty"`
	Lift         *float64 `json:"lift,omitempty"`
	// LiftCI is the lift interval, left out for runs without trial scores.
	LiftCI *Interval `json:"liftCI,omitempty"`
	// N is the trials behind the scores, with/without, for example "9/9".
	N string `json:"n,omitempty"`
	// Tiers are the tier statuses.
	Tiers Tiers `json:"tiers"`
	// Publication is SkillEvaluator's publication assessment.
	Publication string `json:"publication,omitempty"`
	// NetworkMode is the effective network mode; left out for runs before network modes.
	NetworkMode string `json:"networkMode,omitempty"`
	// Retries counts fresh Jobs after infrastructure failures.
	Retries int `json:"retries"`
	// CreatedAt is when the SkillEval was created.
	CreatedAt time.Time `json:"createdAt"`
	// CompletedAt is when the run finished.
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	// DurationSec is CompletedAt minus CreatedAt.
	DurationSec *float64 `json:"durationSec,omitempty"`
	// Message is the failure reason or other detail.
	Message string `json:"message,omitempty"`
	// HasReport reports whether a report digest is stored.
	HasReport bool `json:"hasReport"`
	// Trials is stored, withheld, none or unknown (see the Trials constants).
	Trials string `json:"trials"`
	// TrialsReason is the skillci.dev/trials-withheld value of the report: why
	// the runner withheld the bundle (secret-scan, size, build-failed,
	// attach-failed, or another reason it gave), or why a run with Trials none
	// has no bundle (no-trials, disabled). Left out otherwise: Trials none with
	// HasReport and no reason is a report from a runner before trial bundles.
	TrialsReason string `json:"trialsReason,omitempty"`
}

// RunFilter is the filter of GET /api/v1/runs, echoed back. Every field matches
// exactly, except Q, which matches a substring of the run name.
type RunFilter struct {
	// Skill, Agent (agent@version), Model, Verdict, Phase, Publication and Trigger match exactly.
	Skill       string `json:"skill,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Model       string `json:"model,omitempty"`
	Verdict     string `json:"verdict,omitempty"`
	Phase       string `json:"phase,omitempty"`
	Publication string `json:"publication,omitempty"`
	Trigger     string `json:"trigger,omitempty"`
	// Q matches a substring of the run name.
	Q string `json:"q,omitempty"`
	// Sort is newest (default), oldest, skill or lift.
	Sort string `json:"sort"`
}

// FilterOptions are the distinct values the filter controls offer. A value from
// the request filter is kept even when no run has it.
type FilterOptions struct {
	// Skills, Agents, Models, Verdicts, Phases, Publications and Triggers are sorted.
	Skills       []string `json:"skills"`
	Agents       []string `json:"agents"`
	Models       []string `json:"models"`
	Verdicts     []string `json:"verdicts"`
	Phases       []string `json:"phases"`
	Publications []string `json:"publications"`
	Triggers     []string `json:"triggers"`
}

// RunList is the response of GET /api/v1/runs.
type RunList struct {
	// Items is one page of runs in the requested order.
	Items []RunSummary `json:"items"`
	// Pagination describes the page.
	Pagination Pagination `json:"pagination"`
	// Filter is the filter that was applied.
	Filter RunFilter `json:"filter"`
	// Options are the values the filter controls offer.
	Options FilterOptions `json:"options"`
	// RefreshSeconds is how often to poll, 0 when nothing is pending or running.
	RefreshSeconds int `json:"refreshSeconds"`
}

// SkillList is the response of GET /api/v1/skills: the skill catalog.
type SkillList struct {
	// Items holds one row per skill, sorted by name.
	Items []SkillSummary `json:"items"`
	// ChannelsError is set when Harbor could not be read.
	ChannelsError string `json:"channelsError,omitempty"`
}

// SkillSummary is one row of the skill catalog.
type SkillSummary struct {
	// Skill is the skill name.
	Skill string `json:"skill"`
	// Stable and Beta are the versions holding the tags.
	Stable string `json:"stable,omitempty"`
	Beta   string `json:"beta,omitempty"`
	// Newest is the newest evaluated version.
	Newest string `json:"newest,omitempty"`
	// Versions counts evaluated digests; Runs counts SkillEvals; Active counts
	// pending or running ones, Pending and Running each phase on its own.
	Versions int `json:"versions"`
	Runs     int `json:"runs"`
	Active   int `json:"active"`
	Pending  int `json:"pending"`
	Running  int `json:"running"`
	// LastRunAt is when the newest run was created.
	LastRunAt *time.Time `json:"lastRunAt,omitempty"`
	// HeldBack and Rejected list versions, newest first.
	HeldBack []string `json:"heldBack"`
	Rejected []string `json:"rejected"`
	// Required counts the required entries whose newest run on the newest version succeeded with a non-failing verdict.
	Required Ratio `json:"required"`
}

// SkillDetail is the response of GET /api/v1/skills/{skill}.
type SkillDetail struct {
	// Skill is the skill name.
	Skill string `json:"skill"`
	// Stable and Beta are the versions holding the tags.
	Stable string `json:"stable,omitempty"`
	Beta   string `json:"beta,omitempty"`
	// Runs counts the skill's SkillEvals.
	Runs int `json:"runs"`
	// ChannelsError is set when Harbor could not be read.
	ChannelsError string `json:"channelsError,omitempty"`
	// Columns are the model list entries that ran, required first, then by entry.
	Columns []ModelColumn `json:"columns"`
	// Rows are the evaluated versions, newest first.
	Rows []VersionRow `json:"rows"`
	// RefreshSeconds is how often to poll, 0 when nothing is pending or running.
	RefreshSeconds int `json:"refreshSeconds"`
}

// ModelColumn is one column of the skill matrix: a model list entry.
type ModelColumn struct {
	// Agent, AgentVersion and Model identify the entry.
	Agent        string `json:"agent"`
	AgentVersion string `json:"agentVersion,omitempty"`
	Model        string `json:"model"`
	// Entry is agent@version/model.
	Entry string `json:"entry"`
	// Required is true when any run of the entry is required.
	Required bool `json:"required"`
}

// VersionRow is one evaluated digest of a skill.
type VersionRow struct {
	// Version and Digest identify the row.
	Version string `json:"version"`
	Digest  string `json:"digest"`
	// Channels are the tags the digest carries (stable, beta).
	Channels []string `json:"channels"`
	// Cells has one entry per column, null where the entry never ran on this digest.
	Cells []*MatrixCell `json:"cells"`
	// Decision is what the tag policy did with the version.
	Decision Decision `json:"decision"`
}

// MatrixCell is the newest run of one entry on one version.
type MatrixCell struct {
	// Run is the SkillEval name.
	Run string `json:"run"`
	// Phase and Verdict are those of the run.
	Phase   string `json:"phase"`
	Verdict string `json:"verdict,omitempty"`
	// WithSkill, WithoutSkill and Lift are the scores.
	WithSkill    *float64 `json:"withSkill,omitempty"`
	WithoutSkill *float64 `json:"withoutSkill,omitempty"`
	Lift         *float64 `json:"lift,omitempty"`
	// LiftCI and N are the evidence of the lift.
	LiftCI *Interval `json:"liftCI,omitempty"`
	N      string    `json:"n,omitempty"`
	// Tiers are the tier statuses.
	Tiers Tiers `json:"tiers"`
	// Trials is stored, withheld, none or unknown.
	Trials string `json:"trials"`
}

// Decision explains what the tag policy did with one skill version.
type Decision struct {
	// Kind picks the marker: pass, neutral, regressed or failed.
	Kind string `json:"kind"`
	// Headline is the short outcome, for example "Held back".
	Headline string `json:"headline"`
	// Detail is the reason in one sentence.
	Detail string `json:"detail"`
}

// ModelList is the response of GET /api/v1/models.
type ModelList struct {
	// Source is the ConfigMap the list comes from.
	Source string `json:"source"`
	// Error is set when the ConfigMap could not be read or parsed; Items then holds only entries seen in runs.
	Error string `json:"error,omitempty"`
	// Items are the listed entries in list order, then retired entries that still have runs.
	Items []ModelEntry `json:"items"`
}

// ModelEntry is one model list entry with its run statistics.
type ModelEntry struct {
	// Agent, AgentVersion and Model identify the entry.
	Agent        string `json:"agent"`
	AgentVersion string `json:"agentVersion,omitempty"`
	Model        string `json:"model"`
	// Entry is agent@version/model.
	Entry string `json:"entry"`
	// Required marks an entry that gates promotion.
	Required bool `json:"required"`
	// SandboxProfile is the profile of the entry's trials, left out for the default.
	SandboxProfile string `json:"sandboxProfile,omitempty"`
	// Listed is false for a retired entry that is no longer in the list but still has runs.
	Listed bool `json:"listed"`
	// Runs counts the entry's SkillEvals; Failed those that ended Failed; Retries sums their retries.
	Runs    int `json:"runs"`
	Failed  int `json:"failed"`
	Retries int `json:"retries"`
	// Verdicts counts runs per verdict.
	Verdicts map[string]int `json:"verdicts"`
	// MeanLift is the mean lift over the runs with a lift.
	MeanLift *float64 `json:"meanLift,omitempty"`
	// LastRunAt is when the newest run was created.
	LastRunAt *time.Time `json:"lastRunAt,omitempty"`
}

// Error is the body of every API error response.
type Error struct {
	// Error describes the problem.
	Error ErrorBody `json:"error"`
}

// ErrorBody is the content of an Error.
type ErrorBody struct {
	// Status repeats the HTTP status code.
	Status int `json:"status"`
	// Code is a stable machine-readable code (see the Code constants).
	Code string `json:"code"`
	// Message is a sentence for people.
	Message string `json:"message"`
}

// Error codes.
const (
	// CodeBadRequest is a malformed path or query parameter (400).
	CodeBadRequest = "bad_request"
	// CodeNotFound is an unknown run, skill, attempt, trial, case or route (404).
	CodeNotFound = "not_found"
	// CodeWithheld is a trial or bundle the runner withheld (404).
	CodeWithheld = "withheld"
	// CodeNotImplemented is a route that exists but is not built yet (501).
	CodeNotImplemented = "not_implemented"
	// CodeRegistry is a registry failure while reading a report or bundle (502).
	CodeRegistry = "registry_error"
	// CodeUnavailable is a failure to read the cluster (503).
	CodeUnavailable = "unavailable"
	// CodeInternal is a server bug, for example a response that could not be encoded (500).
	CodeInternal = "internal"
)

// RawJSON is a JSON value passed through as SkillEvaluator wrote it.
type RawJSON = json.RawMessage
