package apiv1

import "time"

// RunRef names the run a trial belongs to.
type RunRef struct {
	// Name is the SkillEval name.
	Name string `json:"name"`
	// Skill and Version identify the skill version.
	Skill   string `json:"skill"`
	Version string `json:"version"`
	// Agent, AgentVersion, Model and Entry identify the model list entry.
	Agent        string `json:"agent"`
	AgentVersion string `json:"agentVersion,omitempty"`
	Model        string `json:"model"`
	Entry        string `json:"entry"`
	// Job is the Job name of the run attempt the trial belongs to.
	Job string `json:"job"`
	// CurrentJob reports whether Job is the run's current attempt.
	CurrentJob bool `json:"currentJob"`
}

// TrialSession is the response of GET /api/v1/runs/{name}/trials/{trial}[?job=].
type TrialSession struct {
	// Run names the run.
	Run RunRef `json:"run"`
	// Trial is the trial's grid cell.
	Trial TrialCell `json:"trial"`
	// Header is the top of the session page.
	Header TrialHeader `json:"header"`
	// Metadata is the metadata strip.
	Metadata TrialMetadata `json:"metadata"`
	// Log is the session log.
	Log TrialLog `json:"log"`
	// Criteria are the eval criteria of the right column.
	Criteria Criteria `json:"criteria"`
	// Findings are the derived session findings of the right column.
	Findings []Finding `json:"findings"`
	// Files are the files the agent wrote or edited, from its tool calls.
	Files []FileChange `json:"files"`
	// Judge holds every judge metric with its details.
	Judge []JudgeMetric `json:"judge"`
	// Network holds the trial's flows; left out when the bundle has no network section.
	Network *TrialNetwork `json:"network,omitempty"`
	// Truncations list strings the runner shortened, as pointers into the trial document.
	Truncations []Truncation `json:"truncations"`
	// Redaction counts what was redacted, never the values.
	Redaction RedactionCounts `json:"redaction"`
	// Downloads link to raw downloads of the trial.
	Downloads Downloads `json:"downloads"`
}

// TrialHeader is the top of the session page.
type TrialHeader struct {
	// Prompt is the first user step (or the case question when there is no trajectory).
	Prompt string `json:"prompt"`
	// Status is scored, unscored or error.
	Status string `json:"status"`
	// Score is the overall score of this trial; ArmMean the mean of its arm in this run attempt.
	Score   *float64 `json:"score,omitempty"`
	ArmMean *float64 `json:"armMean,omitempty"`
	// Dimensions are the six metrics of this trial next to the arm means.
	Dimensions []TrialDimension `json:"dimensions"`
	// Error is the failure of an unscored trial.
	Error *TrialError `json:"error,omitempty"`
	// Final is the agent's last message, the final answer.
	Final string `json:"final,omitempty"`
}

// TrialDimension is one metric score of a trial.
type TrialDimension struct {
	// Key is the metric key; Label its display name.
	Key   string `json:"key"`
	Label string `json:"label"`
	// Score is this trial's score; ArmMean the arm's mean in the run attempt.
	Score   float64  `json:"score"`
	ArmMean *float64 `json:"armMean,omitempty"`
}

// TrialError is why a trial was not scored.
type TrialError struct {
	// Type is the exception type, for example AgentTimeoutError.
	Type string `json:"type"`
	// Message is the exception message.
	Message string `json:"message"`
	// OccurredAt is when it was raised.
	OccurredAt *time.Time `json:"occurredAt,omitempty"`
	// Traceback is the stored traceback, at most 8 KiB.
	Traceback string `json:"traceback,omitempty"`
}

// TrialMetadata is the metadata strip of a session.
type TrialMetadata struct {
	// Agent, AgentVersion, Model and Provider describe what ran.
	Agent        string `json:"agent"`
	AgentVersion string `json:"agentVersion,omitempty"`
	Model        string `json:"model"`
	Provider     string `json:"provider,omitempty"`
	// Arm, Case, Attempt and AttemptsPerCase place the trial ("case rn-basic, attempt 1 of 3").
	Arm             string `json:"arm"`
	Case            string `json:"case"`
	Attempt         int    `json:"attempt"`
	AttemptsPerCase int    `json:"attemptsPerCase"`
	// Workspace is the agent's working directory (ATIF agent.extra.cwds).
	Workspace string `json:"workspace,omitempty"`
	// Skill says whether the skill was used.
	Skill SkillUse `json:"skill"`
	// Credentials name the provider routes, names only.
	Credentials []Credential `json:"credentials"`
	// StartedAt and FinishedAt bound the trial; DurationSec is their difference.
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	DurationSec *float64   `json:"durationSec,omitempty"`
	// Phases are Harbor's trial phases in order.
	Phases []Phase `json:"phases"`
	// Tokens are the trajectory's final totals.
	Tokens Tokens `json:"tokens"`
	// CostUSD is the total cost; CostEstimated is true when it is a LiteLLM estimate.
	CostUSD       *float64 `json:"costUsd,omitempty"`
	CostEstimated bool     `json:"costEstimated"`
	// Criteria counts the expected behaviors that passed.
	Criteria Ratio `json:"criteria"`
	// Trigger is why the run exists; PushedBy who pushed the version, when known.
	Trigger  string `json:"trigger"`
	PushedBy string `json:"pushedBy,omitempty"`
	// SchemaVersion is the ATIF schema version of the trajectory, for example ATIF-v1.7.
	SchemaVersion string `json:"schemaVersion,omitempty"`
}

// Skill use states.
const (
	// SkillInvoked means the agent activated the skill (Skill tool, SKILL.md read).
	SkillInvoked = "invoked"
	// SkillLoaded means the skill was installed and listed but the agent did not activate it.
	SkillLoaded = "loaded"
	// SkillNotUsed means the skill was not present (the without-skill arm).
	SkillNotUsed = "not-used"
	// SkillUnknown means the trial was not scored, so nothing is known.
	SkillUnknown = "unknown"
)

// SkillUse is whether a trial used the skill.
type SkillUse struct {
	// State is invoked, loaded, not-used or unknown.
	State string `json:"state"`
	// Expected is the skill the case expects; Matched the one the agent activated.
	Expected string `json:"expected,omitempty"`
	Matched  string `json:"matched,omitempty"`
	// Reason is SkillEvaluator's activation reason.
	Reason string `json:"reason,omitempty"`
}

// Phase is one Harbor trial phase.
type Phase struct {
	// Name is environment_setup, agent_setup, agent_execution or verifier.
	Name string `json:"name"`
	// Label is the display name.
	Label string `json:"label"`
	// StartedAt and FinishedAt bound the phase; DurationSec is their difference.
	StartedAt   time.Time `json:"startedAt"`
	FinishedAt  time.Time `json:"finishedAt"`
	DurationSec float64   `json:"durationSec"`
}

// Tokens are token totals; a count that is not known is left out.
type Tokens struct {
	// In, Out and Cached are prompt, completion and cached prompt tokens.
	In     *int64 `json:"in,omitempty"`
	Out    *int64 `json:"out,omitempty"`
	Cached *int64 `json:"cached,omitempty"`
}

// TrialLog is the session log: one entry per ATIF part, then judge entries.
type TrialLog struct {
	// Entries are all entries in order.
	Entries []LogEntry `json:"entries"`
	// Counts holds one count per category, in Categories order, including zero counts.
	Counts []CategoryCount `json:"counts"`
	// Jumps are entry ids of notable places; a place that does not exist is left out.
	Jumps Jumps `json:"jumps"`
}

// CategoryCount is the count of one log category.
type CategoryCount struct {
	// Category is a Category constant.
	Category string `json:"category"`
	// Count is the number of entries.
	Count int `json:"count"`
	// Focus marks the categories of the Focus preset.
	Focus bool `json:"focus"`
}

// Jumps are the entry ids of the "jump to" links.
type Jumps struct {
	// FirstTool, Skill, FirstError and Final are entry ids.
	FirstTool  string `json:"firstTool,omitempty"`
	Skill      string `json:"skill,omitempty"`
	FirstError string `json:"firstError,omitempty"`
	Final      string `json:"final,omitempty"`
}

// LogEntry is one entry of the session log.
type LogEntry struct {
	// ID is the anchor: s-<step> for the first entry of a step, s-<step>-<n> for its later parts,
	// j-<metric> for judge entries and n-<n> for network entries.
	ID string `json:"id"`
	// Step is the ATIF step_id (1-based); 0 for judge and network entries.
	Step int `json:"step"`
	// Category is a Category constant.
	Category string `json:"category"`
	// Source is user, agent, system, judge or network.
	Source string `json:"source"`
	// At is the step timestamp; OffsetSec the seconds since the first step.
	At        *time.Time `json:"at,omitempty"`
	OffsetSec *float64   `json:"offsetSec,omitempty"`
	// Title is a one-line summary, for example "$ ls -la /workspace".
	Title string `json:"title"`
	// Text is the message, reasoning or prompt, as a preview (thinking at most 1 KiB).
	Text string `json:"text,omitempty"`
	// TextTruncated reports whether Text is shorter than the stored text.
	TextTruncated bool `json:"textTruncated"`
	// Tool is the tool call of a tools, web, pkg or skill entry.
	Tool *ToolCall `json:"tool,omitempty"`
	// Usage is the step's token and cost usage (usage entries).
	Usage *Usage `json:"usage,omitempty"`
	// Flow is the flow of a net entry.
	Flow *FlowRow `json:"flow,omitempty"`
	// Metric is the judge metric key of a judge entry.
	Metric string `json:"metric,omitempty"`
	// Error marks a failed tool call, a failed metric or a DROPPED flow.
	Error bool `json:"error"`
	// Label is the accessible name, for example "Step 12, Bash tool call, failed".
	Label string `json:"label"`
}

// Tool kinds.
const (
	// ToolShell is a shell command (Bash, exec_command, shell).
	ToolShell = "shell"
	// ToolRead is a file read (Read, Grep, Glob, read).
	ToolRead = "read"
	// ToolWrite is a file write.
	ToolWrite = "write"
	// ToolEdit is a file edit.
	ToolEdit = "edit"
	// ToolPatch is apply_patch.
	ToolPatch = "patch"
	// ToolSkill is a Skill tool call.
	ToolSkill = "skill"
	// ToolWeb is WebFetch or WebSearch.
	ToolWeb = "web"
	// ToolOther is anything else.
	ToolOther = "other"
)

// ToolCall is one tool call with its observation.
type ToolCall struct {
	// Name is the function name; CallID the tool_call_id.
	Name   string `json:"name"`
	CallID string `json:"callId"`
	// Kind is a Tool constant.
	Kind string `json:"kind"`
	// Command is the shell command of a shell call.
	Command string `json:"command,omitempty"`
	// Path is the file of a read, write or edit call.
	Path string `json:"path,omitempty"`
	// Input is the arguments as indented JSON, shortened to 4 KiB.
	Input string `json:"input"`
	// Output is the matching observation result, when there is one.
	Output *ToolOutput `json:"output,omitempty"`
}

// ToolOutput is an observation result, as a preview.
type ToolOutput struct {
	// Preview is at most 4 KiB or 40 lines.
	Preview string `json:"preview"`
	// Bytes and Lines measure the stored output.
	Bytes int `json:"bytes"`
	Lines int `json:"lines"`
	// Truncated reports whether Preview is shorter than the stored output.
	Truncated bool `json:"truncated"`
	// Shortened reports whether the runner shortened the stored output (see Truncations).
	Shortened bool `json:"shortened"`
	// Error is true when the tool reported a failure.
	Error bool `json:"error"`
	// Href is the full text, text/plain: /api/v1/runs/{name}/trials/{trial}/steps/{step}/output/{k}.
	Href string `json:"href"`
}

// Usage is the token and cost usage of one step.
type Usage struct {
	// Tokens are the step's counts.
	Tokens Tokens `json:"tokens"`
	// CostUSD is the step cost; per-step costs are estimates unless the agent reported them.
	CostUSD   *float64 `json:"costUsd,omitempty"`
	Estimated bool     `json:"estimated"`
}

// Criteria are the eval criteria of a trial.
type Criteria struct {
	// Passed and Total count the expected behaviors.
	Passed int `json:"passed"`
	Total  int `json:"total"`
	// Behaviors are the expected behaviors with the judge's result.
	Behaviors []BehaviorResult `json:"behaviors"`
	// BehaviorSteps are the steps the behavior_check metric cites (per metric, not per behavior).
	BehaviorSteps []int `json:"behaviorSteps"`
	// Accuracy is the accuracy metric's criteria, when scored.
	Accuracy *AccuracyCriteria `json:"accuracy,omitempty"`
	// Checks are the skill_execution and skill_efficiency sub-checks.
	Checks []Check `json:"checks"`
}

// BehaviorResult is one expected behavior with the judge's result.
type BehaviorResult struct {
	// Index is the 1-based behavior number.
	Index int `json:"index"`
	// Text is the behavior from the dataset.
	Text string `json:"text"`
	// Passed is the judge's result, left out when the judge gave none.
	Passed *bool `json:"passed,omitempty"`
	// Reason is the judge's one-line reason.
	Reason string `json:"reason,omitempty"`
}

// AccuracyCriteria are the accuracy metric's boolean criteria with their shared reason.
type AccuracyCriteria struct {
	// Criteria are the named booleans, for example SKILL_IDENTIFIED.
	Criteria []NamedCheck `json:"criteria"`
	// Reason is the one shared reason.
	Reason string `json:"reason"`
	// Steps are the steps the metric cites.
	Steps []int `json:"steps"`
}

// NamedCheck is one boolean criterion.
type NamedCheck struct {
	// Name is the criterion name.
	Name string `json:"name"`
	// Passed is its value.
	Passed bool `json:"passed"`
}

// Check is one sub-check of skill_execution or skill_efficiency.
type Check struct {
	// Metric is the parent metric key; Name the sub-check key; Label its display name.
	Metric string `json:"metric"`
	Name   string `json:"name"`
	Label  string `json:"label"`
	// Passed and Score are the result, when reported.
	Passed *bool    `json:"passed,omitempty"`
	Score  *float64 `json:"score,omitempty"`
	// Reason is the check's reason.
	Reason string `json:"reason,omitempty"`
	// Steps are the steps the check cites.
	Steps []int `json:"steps"`
}

// Finding kinds.
const (
	// FindingFriction is something that went wrong or cost effort.
	FindingFriction = "friction"
	// FindingOpportunity is something that could be better.
	FindingOpportunity = "opportunity"
)

// Finding is a session finding derived by rule (SkillEvaluator does not type findings).
type Finding struct {
	// Kind is friction or opportunity.
	Kind string `json:"kind"`
	// Rule names the rule that produced it, for example metric-failed or skill-not-invoked.
	Rule string `json:"rule"`
	// Metric is the metric it comes from, when it comes from one.
	Metric string `json:"metric,omitempty"`
	// Title is a short line; Detail the reason.
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	// Steps are the steps it cites.
	Steps []int `json:"steps"`
	// RunLevel marks findings from the whole run (suggestions), which have no step links.
	RunLevel bool `json:"runLevel"`
}

// File change operations.
const (
	// FileWrite is a full write; Content is the whole file.
	FileWrite = "write"
	// FileEdit is an edit; Hunks hold old and new text.
	FileEdit = "edit"
	// FilePatch is an apply_patch; Patch holds it.
	FilePatch = "patch"
	// FileCommand is a file written by a shell command; its content was not captured.
	FileCommand = "command"
)

// FileChange is one file the agent changed, from a tool call.
type FileChange struct {
	// Path is the file.
	Path string `json:"path"`
	// Op is write, edit, patch or command.
	Op string `json:"op"`
	// Step is the ATIF step and EntryID the log entry of the call.
	Step    int    `json:"step"`
	EntryID string `json:"entryId"`
	// Tool is the function name.
	Tool string `json:"tool"`
	// Content is the written content (write).
	Content string `json:"content,omitempty"`
	// Hunks are old and new text pairs (edit).
	Hunks []Hunk `json:"hunks"`
	// Patch is the patch text (patch).
	Patch string `json:"patch,omitempty"`
	// Captured is false when the content is not known (command).
	Captured bool `json:"captured"`
	// Truncated reports whether the runner shortened the content.
	Truncated bool `json:"truncated"`
}

// Hunk is one replaced piece of a file.
type Hunk struct {
	// Old and New are the text before and after.
	Old string `json:"old"`
	New string `json:"new"`
}

// JudgeMetric is one metric of reward.json with its details.
type JudgeMetric struct {
	// Key is the metric key; Label its display name.
	Key   string `json:"key"`
	Label string `json:"label"`
	// Score and Passed are the result, when reported.
	Score  *float64 `json:"score,omitempty"`
	Passed *bool    `json:"passed,omitempty"`
	// Reason is the judge's reason.
	Reason string `json:"reason,omitempty"`
	// Model is the judge model, when an LLM judged.
	Model string `json:"model,omitempty"`
	// Steps are the steps the metric's evidence cites.
	Steps []int `json:"steps"`
	// Details is details.<metric> of reward.json as stored.
	Details RawJSON `json:"details,omitempty"`
}

// Network states.
const (
	// NetworkCollected means flows were collected for the whole trial.
	NetworkCollected = "collected"
	// NetworkPartial means some flows are missing (Hubble lost events, or caps applied).
	NetworkPartial = "partial"
	// NetworkUnavailable means flows were expected but could not be collected.
	NetworkUnavailable = "unavailable"
	// NetworkOff means no Hubble Relay is configured.
	NetworkOff = "off"
)

// TrialNetwork is what one trial asked of the network.
type TrialNetwork struct {
	// State is collected, partial, unavailable or off.
	State string `json:"state"`
	// Reason says why flows are partial, unavailable or off.
	Reason string `json:"reason,omitempty"`
	// From and To bound the observation window.
	From *time.Time `json:"from,omitempty"`
	To   *time.Time `json:"to,omitempty"`
	// DNS are the names the trial asked for.
	DNS []DNSRow `json:"dns"`
	// Flows are connections grouped by verdict, destination and port.
	Flows []FlowRow `json:"flows"`
	// Truncated reports whether caps dropped rows.
	Truncated bool `json:"truncated"`
	// Lost counts flows Hubble reported as lost.
	Lost int `json:"lost"`
}

// DNSRow is one name a trial asked for.
type DNSRow struct {
	// Name is the query name without the trailing dot.
	Name string `json:"name"`
	// Count is the number of queries; FirstAt and LastAt bound them.
	Count   int       `json:"count"`
	FirstAt time.Time `json:"firstAt"`
	LastAt  time.Time `json:"lastAt"`
	// Rcode is the response code, for example NOERROR, when seen.
	Rcode string `json:"rcode,omitempty"`
}

// FlowRow is the flows of one verdict, destination and port.
type FlowRow struct {
	// Verdict is FORWARDED, DROPPED, ERROR or AUDIT.
	Verdict string `json:"verdict"`
	// Destination is the DNS name Hubble knew for the IP, else the IP.
	Destination string `json:"destination"`
	// IP is the destination address.
	IP string `json:"ip"`
	// Port and Protocol are the destination port and L4 protocol.
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	// Count is the number of flows; FirstAt and LastAt bound them.
	Count   int       `json:"count"`
	FirstAt time.Time `json:"firstAt"`
	LastAt  time.Time `json:"lastAt"`
	// Reason is the drop reason of a DROPPED row, for example POLICY_DENIED.
	Reason string `json:"reason,omitempty"`
	// Step is the step that was running at FirstAt, 0 when none was.
	Step int `json:"step"`
}

// Truncation is one string the runner shortened in the trial document.
type Truncation struct {
	// Pointer is a JSON pointer into the trial document, for example /trajectory/steps/3/observation/results/0/content.
	Pointer string `json:"pointer"`
	// OriginalBytes is the size before shortening.
	OriginalBytes int `json:"originalBytes"`
}

// RedactionCounts count redactions by rule, never values.
type RedactionCounts struct {
	// Version is the runner's redaction version, for example v1.
	Version string `json:"version"`
	// Exact, Pattern and EnvDump count the runner's replacements per rule.
	Exact   int `json:"exact"`
	Pattern int `json:"pattern"`
	EnvDump int `json:"envDump"`
	// Render counts replacements the controller made when it rendered the session (defense in depth).
	Render int `json:"render"`
}

// Downloads are raw downloads of a trial.
type Downloads struct {
	// Trajectory is the redacted ATIF file, left out when the bundle has none.
	Trajectory string `json:"trajectory,omitempty"`
}

// Compare is the response of GET /api/v1/runs/{name}/cases/{case}/compare.
type Compare struct {
	// Run names the run.
	Run RunRef `json:"run"`
	// Case is the eval case.
	Case CaseInfo `json:"case"`
	// Attempt is the paired attempt (attempt k with attempt k); AttemptsPerCase the number of attempts.
	Attempt         int `json:"attempt"`
	AttemptsPerCase int `json:"attemptsPerCase"`
	// WithSkill and WithoutSkill are the two sides.
	WithSkill    CompareSide `json:"withSkill"`
	WithoutSkill CompareSide `json:"withoutSkill"`
}

// CaseInfo describes one eval case.
type CaseInfo struct {
	// ID is the case id.
	ID string `json:"id"`
	// Question is the case prompt, at most 300 characters.
	Question string `json:"question"`
	// ExpectedBehaviors are the behaviors the judge checks.
	ExpectedBehaviors []string `json:"expectedBehaviors"`
}

// CompareSide is one arm of a comparison. A side without a trial (missing,
// withheld or unscored without a document) has only Trial or Missing set.
type CompareSide struct {
	// Trial is the grid cell, left out when the arm has no trial for the attempt.
	Trial *TrialCell `json:"trial,omitempty"`
	// Missing says why the side is empty.
	Missing string `json:"missing,omitempty"`
	// Header, Metadata and Criteria are those of the session.
	Header   *TrialHeader   `json:"header,omitempty"`
	Metadata *TrialMetadata `json:"metadata,omitempty"`
	Criteria *Criteria      `json:"criteria,omitempty"`
	// Log holds the entries of the Focus preset only.
	Log []LogEntry `json:"log"`
	// Files are the files the agent changed.
	Files []FileChange `json:"files"`
}
