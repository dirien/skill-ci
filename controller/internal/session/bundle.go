// Package session reads trial bundles (CONTRACT.md, "Trial bundles") and turns
// one trial into the session view of the reporting site API: log entries with
// categories and counts, eval criteria, derived findings, files from tool
// calls, network flows and the comparison of the two arms of an eval case.
//
// The package is pure: no HTTP, no registry, no Kubernetes. The reporting site
// fetches the index and trial documents through reportstore and hands the bytes
// here. Trajectories are parsed leniently (unknown fields are ignored, ATIF
// v1.5 to v1.8), because the agents write the schema version of their Harbor
// release.
package session

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

// ErrNotImplemented is returned by the functions of this package until they are built.
var ErrNotImplemented = errors.New("session: not implemented")

// OCI artifact and media types of a trial bundle.
const (
	// ArtifactType is the artifact type of the trial bundle referrer.
	ArtifactType = "application/vnd.skillci.trials.v1+json"
	// MediaTypeIndex is the media type of the index.json layer.
	MediaTypeIndex = "application/vnd.skillci.trials.index.v1+json"
	// MediaTypeTrial is the media type of a trial-<id>.json.gz layer: gzip of one JSON trial document.
	MediaTypeTrial = "application/vnd.skillci.trial.v1+gzip"
	// IndexFile is the layer title of the index.
	IndexFile = "index.json"
)

// Schema names written into the documents.
const (
	// SchemaIndex is the schema field of index.json.
	SchemaIndex = "skillci.trials.index.v1"
	// SchemaTrial is the schema field of a trial document.
	SchemaTrial = "skillci.trial.v1"
)

// Manifest annotations of the bundle and of the report that references it.
const (
	// AnnotationTrials on the report referrer is the digest of its trial bundle.
	AnnotationTrials = "skillci.dev/trials"
	// AnnotationTrialsWithheld on the report referrer is why no bundle was attached.
	AnnotationTrialsWithheld = "skillci.dev/trials-withheld"
	// AnnotationTrialsSchema on the bundle is the schema major version, "1".
	AnnotationTrialsSchema = "skillci.dev/trials-schema"
	// AnnotationRedaction on the bundle is the runner's redaction version, "v1".
	AnnotationRedaction = "skillci.dev/redaction"
	// AnnotationEval on the bundle is the SkillEval name.
	AnnotationEval = "skillci.dev/eval"
)

// Limits the controller applies when it reads a bundle.
const (
	// MaxIndexBytes bounds index.json.
	MaxIndexBytes = 1 << 20
	// MaxTrialBytes bounds one decompressed trial document (against decompression bombs).
	MaxTrialBytes = 8 << 20
)

// TrialID is the pattern of a trial id. A request names a trial only by an id
// that matches it and appears in the index; registry paths are never built from raw input.
var TrialID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// TrialLayer is the layer title of the trial document with the given id.
func TrialLayer(id string) string { return "trial-" + id + ".json.gz" }

// Index is the index.json layer of a trial bundle (docs/schemas/skillci.trials.index.v1.json).
type Index struct {
	// Schema is SchemaIndex.
	Schema string `json:"schema"`
	// Skill, Version and Digest identify the evaluated skill version. Version is
	// empty when the Job had no SKILL_VERSION.
	Skill   string `json:"skill"`
	Version string `json:"version,omitempty"`
	Digest  string `json:"digest"`
	// Eval is the SkillEval name (SKILLEVAL_NAME).
	Eval string `json:"eval"`
	// Agent, AgentVersion, Model and Entry identify the model list entry.
	Agent        string `json:"agent"`
	AgentVersion string `json:"agentVersion,omitempty"`
	Model        string `json:"model"`
	Entry        string `json:"entry"`
	// RunID is SkillEvaluator's run id; EvaluatedAt its evaluated_at.
	RunID       string     `json:"runId"`
	EvaluatedAt *time.Time `json:"evaluatedAt,omitempty"`
	// Evaluator describes the SkillEvaluator run.
	Evaluator Evaluator `json:"evaluator"`
	// AttemptsPerCase is the number of attempts per case and arm (ATTEMPTS).
	AttemptsPerCase int `json:"attemptsPerCase"`
	// Redaction is the runner's redaction version, "v1".
	Redaction string `json:"redaction"`
	// Credentials name the provider routes, never values.
	Credentials []Credential `json:"credentials"`
	// Limits are the caps the runner applied.
	Limits Limits `json:"limits"`
	// Cases are the eval cases in dataset order.
	Cases []Case `json:"cases"`
	// Trials are the bundled trials.
	Trials []IndexTrial `json:"trials"`
	// Withheld are trials left out of the bundle.
	Withheld []Withheld `json:"withheld"`
	// Network says whether flows were collected for the run.
	Network IndexNetwork `json:"network"`
}

// Evaluator describes the SkillEvaluator run behind a bundle.
type Evaluator struct {
	// Version is SkillEvaluator's version; MetricSet and ScorePolicy name its scoring.
	Version     string `json:"version,omitempty"`
	MetricSet   string `json:"metricSet,omitempty"`
	ScorePolicy string `json:"scorePolicy,omitempty"`
}

// Credential is one provider route, names only.
type Credential struct {
	// Role is agent (the route of the agent under test) or judge.
	Role string `json:"role"`
	// Env is the variable name; Host the endpoint host.
	Env  string `json:"env"`
	Host string `json:"host"`
}

// Limits are the caps the runner applied, in bytes or counts.
type Limits struct {
	// StringBytes caps one string (head and tail kept).
	StringBytes int `json:"stringBytes"`
	// OutputBytes caps observations when a trial is over TrialBytes.
	OutputBytes int `json:"outputBytes"`
	// LogTailBytes caps the agent log tail.
	LogTailBytes int `json:"logTailBytes"`
	// ExceptionBytes caps the stored traceback.
	ExceptionBytes int `json:"exceptionBytes"`
	// TrialBytes caps one compressed trial layer.
	TrialBytes int `json:"trialBytes"`
	// BundleBytes caps all compressed layers together.
	BundleBytes int `json:"bundleBytes"`
	// TrialsPerArm caps the trials per arm.
	TrialsPerArm int `json:"trialsPerArm"`
	// FlowRows caps the flow and DNS rows per trial.
	FlowRows int `json:"flowRows"`
}

// Case is one eval case.
type Case struct {
	// ID is the case id.
	ID string `json:"id"`
	// Question is the prompt, at most 300 characters.
	Question string `json:"question"`
	// ExpectedBehaviors are the behaviors the judge checks.
	ExpectedBehaviors []string `json:"expectedBehaviors"`
	// ExpectedSkill is the skill a positive case expects; empty for a negative case.
	ExpectedSkill string `json:"expectedSkill,omitempty"`
}

// IndexTrial is one trial entry of the index.
type IndexTrial struct {
	// ID is the Harbor trial name; Layer its layer title.
	ID    string `json:"id"`
	Layer string `json:"layer"`
	// Arm is with-skill or without-skill; Case and Attempt place it.
	Arm     string `json:"arm"`
	Case    string `json:"case"`
	Attempt int    `json:"attempt"`
	// Status is scored, unscored or error.
	Status string `json:"status"`
	// Score is the overall score and Passed whether it reached the pass threshold (scored only).
	Score  *float64 `json:"score,omitempty"`
	Passed *bool    `json:"passed,omitempty"`
	// ErrorType and ErrorMessage describe an unscored trial (message at most 300 characters).
	ErrorType    string `json:"errorType,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	// StartedAt is when the trial started; DurationSec and AgentSec are rounded to 0.1 s.
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	DurationSec *float64   `json:"durationSec,omitempty"`
	AgentSec    *float64   `json:"agentSec,omitempty"`
	// Steps and ToolCalls count trajectory steps and tool calls.
	Steps     int `json:"steps"`
	ToolCalls int `json:"toolCalls"`
	// Tokens are final_metrics totals.
	TokensIn     *int64 `json:"tokensIn,omitempty"`
	TokensOut    *int64 `json:"tokensOut,omitempty"`
	TokensCached *int64 `json:"tokensCached,omitempty"`
	// CostUSD is final_metrics.total_cost_usd; CostSource is reported or estimate.
	CostUSD    *float64 `json:"costUsd,omitempty"`
	CostSource string   `json:"costSource,omitempty"`
	// Criteria counts behavior_check results.
	Criteria *Ratio `json:"criteria,omitempty"`
	// SkillInvoked is reward.json skill_invoked.
	SkillInvoked *bool `json:"skillInvoked,omitempty"`
	// Bytes is the compressed layer size; RawBytes the document before gzip.
	Bytes    int `json:"bytes"`
	RawBytes int `json:"rawBytes"`
	// Truncated reports whether any string or section was shortened or left out.
	Truncated bool `json:"truncated"`
	// Network counts the trial's flow rows, when flows were collected.
	Network *NetworkCounts `json:"network,omitempty"`
}

// Ratio counts passes out of a total.
type Ratio struct {
	// Passed and Total are the counts.
	Passed int `json:"passed"`
	Total  int `json:"total"`
}

// NetworkCounts count the flow rows of one trial.
type NetworkCounts struct {
	// Forwarded and Dropped count flow rows by verdict; DNS counts names.
	Forwarded int `json:"forwarded"`
	Dropped   int `json:"dropped"`
	DNS       int `json:"dns"`
}

// Withheld is a trial left out of the bundle.
type Withheld struct {
	// ID, Arm, Case and Attempt identify the trial.
	ID      string `json:"id"`
	Arm     string `json:"arm"`
	Case    string `json:"case"`
	Attempt int    `json:"attempt"`
	// Reason is secret-scan, size, limit or unreadable.
	Reason string `json:"reason"`
}

// IndexNetwork says whether flows were collected for the run.
type IndexNetwork struct {
	// State is collected, partial, unavailable or off.
	State string `json:"state"`
	// Reason says why flows are partial, unavailable or off.
	Reason string `json:"reason,omitempty"`
}

// Trial is one trial document, the content of a trial-<id>.json.gz layer
// (docs/schemas/skillci.trial.v1.json).
type Trial struct {
	// Schema is SchemaTrial.
	Schema string `json:"schema"`
	// ID, Arm, Case, Attempt and Status repeat the index entry.
	ID      string `json:"id"`
	Arm     string `json:"arm"`
	Case    string `json:"case"`
	Attempt int    `json:"attempt"`
	Status  string `json:"status"`
	// Meta is a subset of Harbor's trial result.json.
	Meta TrialMeta `json:"meta"`
	// Prompt is the first user step, or the case question without a trajectory.
	Prompt string `json:"prompt"`
	// Trajectory is the ATIF trajectory as SkillEvaluator wrote it, long strings shortened in place.
	Trajectory *Trajectory `json:"trajectory,omitempty"`
	// TrajectoryOmitted says why there is no trajectory.
	TrajectoryOmitted string `json:"trajectoryOmitted,omitempty"`
	// Reward is reward.json as written (scored trials).
	Reward json.RawMessage `json:"reward,omitempty"`
	// Failure is failure.json as written (unscored trials).
	Failure json.RawMessage `json:"failure,omitempty"`
	// AgentLog is the tail of the agent's own log.
	AgentLog *AgentLog `json:"agentLog,omitempty"`
	// Truncations list shortened strings; pointers are relative to this document.
	Truncations []Truncation `json:"truncations"`
	// Redaction counts the runner's replacements per rule.
	Redaction Redaction `json:"redaction"`
	// Network is the trial's flows, when the run collected flows.
	Network *Network `json:"network,omitempty"`
}

// TrialMeta is the subset of Harbor's trial result.json a bundle keeps.
type TrialMeta struct {
	// TaskName is Harbor's task name.
	TaskName string `json:"taskName,omitempty"`
	// StartedAt and FinishedAt bound the trial.
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// Phases are the Harbor phases that ran, in order.
	Phases []PhaseTime `json:"phases"`
	// Agent is what ran.
	Agent AgentInfo `json:"agent"`
	// AgentResult is Harbor's agent_result totals.
	AgentResult *AgentResult `json:"agentResult,omitempty"`
	// Exception is the trial's exception, traceback capped.
	Exception *Exception `json:"exception,omitempty"`
}

// PhaseTime is one Harbor trial phase.
type PhaseTime struct {
	// Name is environment_setup, agent_setup, agent_execution or verifier.
	Name string `json:"name"`
	// StartedAt and FinishedAt bound it.
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

// AgentInfo is Harbor's agent_info.
type AgentInfo struct {
	// Name, Version, Model and Provider describe the agent.
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// AgentResult is Harbor's agent_result totals.
type AgentResult struct {
	// TokensIn, TokensCached and TokensOut are n_input_tokens, n_cache_tokens and n_output_tokens.
	TokensIn     *int64 `json:"tokensIn,omitempty"`
	TokensCached *int64 `json:"tokensCached,omitempty"`
	TokensOut    *int64 `json:"tokensOut,omitempty"`
	// CostUSD is cost_usd.
	CostUSD *float64 `json:"costUsd,omitempty"`
}

// Exception is a trial exception.
type Exception struct {
	// Type and Message are the exception; OccurredAt when it was raised.
	Type       string     `json:"type"`
	Message    string     `json:"message"`
	OccurredAt *time.Time `json:"occurredAt,omitempty"`
	// Traceback is exception.txt, at most ExceptionBytes.
	Traceback string `json:"traceback,omitempty"`
}

// AgentLog is the tail of the agent's own log (claude-code.txt, codex.txt, opencode.txt).
type AgentLog struct {
	// File is the log file name.
	File string `json:"file"`
	// Text is the tail, at most LogTailBytes.
	Text string `json:"text"`
	// Bytes is the size of the whole log; Truncated whether Text is only its tail.
	Bytes     int  `json:"bytes"`
	Truncated bool `json:"truncated"`
}

// Truncation is one shortened string.
type Truncation struct {
	// Pointer is a JSON pointer into the trial document.
	Pointer string `json:"pointer"`
	// OriginalBytes is the size before shortening.
	OriginalBytes int `json:"originalBytes"`
}

// Redaction counts the runner's replacements per rule.
type Redaction struct {
	// Version is the redaction version, "v1".
	Version string `json:"version"`
	// Exact, Pattern and EnvDump count replacements per rule.
	Exact   int `json:"exact"`
	Pattern int `json:"pattern"`
	EnvDump int `json:"envDump"`
}

// Network is the network section of a trial document.
type Network struct {
	// State is collected, partial, unavailable or off.
	State string `json:"state"`
	// Reason says why flows are partial, unavailable or off.
	Reason string `json:"reason,omitempty"`
	// Source is the collector, "hubble".
	Source string `json:"source,omitempty"`
	// From and To bound the observation window of this trial.
	From *time.Time `json:"from,omitempty"`
	To   *time.Time `json:"to,omitempty"`
	// DNS are the names asked for.
	DNS []DNSQuery `json:"dns"`
	// Flows are grouped by verdict, destination, port and protocol.
	Flows []Flow `json:"flows"`
	// Truncated reports whether the row caps applied.
	Truncated bool `json:"truncated"`
	// Lost counts flows Hubble reported lost.
	Lost int `json:"lost"`
}

// DNSQuery is one name a trial asked for.
type DNSQuery struct {
	// Name is the query name without the trailing dot.
	Name string `json:"name"`
	// Count, FirstAt and LastAt describe the queries.
	Count   int       `json:"count"`
	FirstAt time.Time `json:"firstAt"`
	LastAt  time.Time `json:"lastAt"`
	// Rcode is the response code, when seen.
	Rcode string `json:"rcode,omitempty"`
}

// Flow is a group of flows with one verdict, destination, port and protocol.
type Flow struct {
	// Verdict is FORWARDED, DROPPED, ERROR or AUDIT.
	Verdict string `json:"verdict"`
	// Destination is the DNS name Hubble knew, else the IP.
	Destination string `json:"destination"`
	// IP, Port and Protocol are the destination.
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	// Count, FirstAt and LastAt describe the flows.
	Count   int       `json:"count"`
	FirstAt time.Time `json:"firstAt"`
	LastAt  time.Time `json:"lastAt"`
	// Reason is the drop reason, for example POLICY_DENIED.
	Reason string `json:"reason,omitempty"`
}
