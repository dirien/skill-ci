package session

import "encoding/json"

// Trajectory is the subset of an ATIF trajectory the session view reads. Field
// definitions are Harbor's (harbor/models/trajectories). Unknown fields are
// ignored; content that ATIF allows as a string or a list of parts stays raw.
type Trajectory struct {
	// SchemaVersion is for example ATIF-v1.7.
	SchemaVersion string `json:"schema_version"`
	// SessionID is the agent's session id.
	SessionID string `json:"session_id,omitempty"`
	// Agent describes the agent.
	Agent ATIFAgent `json:"agent"`
	// Steps are the steps; step_id is the 1-based index.
	Steps []Step `json:"steps"`
	// FinalMetrics are the totals.
	FinalMetrics *FinalMetrics `json:"final_metrics,omitempty"`
	// Extra is the trajectory's free-form extra.
	Extra json.RawMessage `json:"extra,omitempty"`
}

// ATIFAgent is the agent block of a trajectory.
type ATIFAgent struct {
	// Name, Version and ModelName describe the agent.
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
	ModelName string `json:"model_name,omitempty"`
	// Extra holds for example cwds.
	Extra json.RawMessage `json:"extra,omitempty"`
}

// Step is one ATIF step.
type Step struct {
	// StepID is 1-based and equals the index plus one.
	StepID int `json:"step_id"`
	// Timestamp is the step time as written (ISO 8601).
	Timestamp string `json:"timestamp,omitempty"`
	// Source is user, agent or system.
	Source string `json:"source"`
	// ModelName is the model of an agent step.
	ModelName string `json:"model_name,omitempty"`
	// Message is a string or a list of content parts.
	Message json.RawMessage `json:"message,omitempty"`
	// ReasoningContent is the agent's reasoning.
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// ToolCalls are the calls of the step.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Observation holds the results of the calls.
	Observation *Observation `json:"observation,omitempty"`
	// Metrics is the step's usage.
	Metrics *Metrics `json:"metrics,omitempty"`
	// IsCopiedContext marks context copied from an earlier session.
	IsCopiedContext *bool `json:"is_copied_context,omitempty"`
	// LLMCallCount is the number of model calls of the step.
	LLMCallCount *int `json:"llm_call_count,omitempty"`
	// Extra holds for example stop_reason and cwd.
	Extra json.RawMessage `json:"extra,omitempty"`
}

// ToolCall is one ATIF tool call.
type ToolCall struct {
	// ToolCallID matches ObservationResult.SourceCallID.
	ToolCallID string `json:"tool_call_id"`
	// FunctionName is the tool, for example Bash or exec_command.
	FunctionName string `json:"function_name"`
	// Arguments are the call arguments as an object.
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// Extra holds for example raw_arguments and status.
	Extra json.RawMessage `json:"extra,omitempty"`
}

// Observation is the observation of a step.
type Observation struct {
	// Results are one per answered call.
	Results []ObservationResult `json:"results"`
}

// ObservationResult is one tool result.
type ObservationResult struct {
	// SourceCallID is the tool call it answers.
	SourceCallID string `json:"source_call_id,omitempty"`
	// Content is a string or a list of content parts.
	Content json.RawMessage `json:"content,omitempty"`
	// SubagentTrajectoryRef points at a subagent trajectory (shown as a summary only).
	SubagentTrajectoryRef json.RawMessage `json:"subagent_trajectory_ref,omitempty"`
	// Extra holds for example tool_result_metadata.
	Extra json.RawMessage `json:"extra,omitempty"`
}

// Metrics is the usage of one step. Redacted extras are strings.
type Metrics struct {
	// PromptTokens, CompletionTokens and CachedTokens are the counts.
	PromptTokens     *int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens *int64 `json:"completion_tokens,omitempty"`
	CachedTokens     *int64 `json:"cached_tokens,omitempty"`
	// CostUSD is the step cost.
	CostUSD *float64 `json:"cost_usd,omitempty"`
	// Extra holds for example cost_source.
	Extra json.RawMessage `json:"extra,omitempty"`
}

// FinalMetrics are the trajectory totals.
type FinalMetrics struct {
	// TotalPromptTokens, TotalCompletionTokens and TotalCachedTokens are the totals.
	TotalPromptTokens     *int64 `json:"total_prompt_tokens,omitempty"`
	TotalCompletionTokens *int64 `json:"total_completion_tokens,omitempty"`
	TotalCachedTokens     *int64 `json:"total_cached_tokens,omitempty"`
	// TotalCostUSD is the total cost.
	TotalCostUSD *float64 `json:"total_cost_usd,omitempty"`
	// TotalSteps is the step count.
	TotalSteps *int `json:"total_steps,omitempty"`
	// Extra holds for example cost_source.
	Extra json.RawMessage `json:"extra,omitempty"`
}
