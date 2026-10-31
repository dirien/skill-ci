package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Errors of the package.
var (
	// ErrNotFound is returned when a step, an observation result or a trajectory does not exist.
	ErrNotFound = errors.New("session: not found")
	// ErrLeak is returned when a rendered view still holds an exact secret value after redaction.
	ErrLeak = errors.New("session: redaction left a secret value in the output")
)

// DecodeIndex parses index.json and checks its schema and trial ids.
func DecodeIndex(data []byte) (*Index, error) {
	if len(data) > MaxIndexBytes {
		return nil, fmt.Errorf("session: index.json has %d bytes, more than %d", len(data), MaxIndexBytes)
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("session: decode index.json: %w", err)
	}
	if idx.Schema != SchemaIndex {
		return nil, fmt.Errorf("session: index.json has schema %q, want %q", idx.Schema, SchemaIndex)
	}
	seen := map[string]bool{}
	for _, entry := range idx.Trials {
		if !TrialID.MatchString(entry.ID) {
			return nil, fmt.Errorf("session: index.json names an invalid trial id %q", entry.ID)
		}
		if entry.Layer != TrialLayer(entry.ID) {
			return nil, fmt.Errorf("session: trial %s has layer %q, want %q", entry.ID, entry.Layer, TrialLayer(entry.ID))
		}
		if seen[entry.ID] {
			return nil, fmt.Errorf("session: index.json names trial %s twice", entry.ID)
		}
		seen[entry.ID] = true
	}
	return &idx, nil
}

// DecodeTrial parses one decompressed trial document and checks its schema. A
// trajectory that cannot be read does not fail the document: it is left out
// and TrajectoryOmitted says why.
func DecodeTrial(data []byte) (*Trial, error) {
	if len(data) > MaxTrialBytes {
		return nil, fmt.Errorf("session: trial document has %d bytes, more than %d", len(data), MaxTrialBytes)
	}
	var shadow struct {
		Trial
		Trajectory json.RawMessage `json:"trajectory,omitempty"`
	}
	if err := json.Unmarshal(data, &shadow); err != nil {
		return nil, fmt.Errorf("session: decode trial document: %w", err)
	}
	t := shadow.Trial
	if t.Schema != SchemaTrial {
		return nil, fmt.Errorf("session: trial document has schema %q, want %q", t.Schema, SchemaTrial)
	}
	if !TrialID.MatchString(t.ID) {
		return nil, fmt.Errorf("session: trial document has an invalid id %q", t.ID)
	}
	t.Trajectory = nil
	if raw := shadow.Trajectory; len(raw) > 0 && !isNull(raw) {
		var traj Trajectory
		if err := json.Unmarshal(raw, &traj); err != nil {
			if t.TrajectoryOmitted == "" {
				t.TrajectoryOmitted = "the trajectory could not be read: " + err.Error()
			}
		} else {
			t.Trajectory = &traj
			t.rawTrajectory = raw
		}
	}
	return &t, nil
}

// UnmarshalJSON decodes a step leniently: step_id as a number or a numeric
// string, reasoning_content as a string or a list of content parts, and
// non-boolean or non-numeric values of is_copied_context and llm_call_count as unknown.
func (s *Step) UnmarshalJSON(data []byte) error {
	type plain Step
	var aux struct {
		plain
		StepID           json.RawMessage `json:"step_id"`
		ReasoningContent json.RawMessage `json:"reasoning_content,omitempty"`
		IsCopiedContext  json.RawMessage `json:"is_copied_context,omitempty"`
		LLMCallCount     json.RawMessage `json:"llm_call_count,omitempty"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return fmt.Errorf("decode ATIF step: %w", err)
	}
	*s = Step(aux.plain)
	if n := lenientInt(aux.StepID); n != nil {
		s.StepID = int(clamp(*n))
	}
	s.ReasoningContent = textOf(aux.ReasoningContent)
	s.IsCopiedContext = lenientBool(aux.IsCopiedContext)
	if n := lenientInt(aux.LLMCallCount); n != nil {
		v := int(clamp(*n))
		s.LLMCallCount = &v
	}
	return nil
}

// UnmarshalJSON decodes step metrics leniently: a count or cost that is not a
// number (SkillEvaluator writes "<redacted>" for some) is unknown.
func (m *Metrics) UnmarshalJSON(data []byte) error {
	var aux struct {
		PromptTokens     json.RawMessage `json:"prompt_tokens"`
		CompletionTokens json.RawMessage `json:"completion_tokens"`
		CachedTokens     json.RawMessage `json:"cached_tokens"`
		CostUSD          json.RawMessage `json:"cost_usd"`
		Extra            json.RawMessage `json:"extra,omitempty"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return fmt.Errorf("decode ATIF metrics: %w", err)
	}
	*m = Metrics{
		PromptTokens:     lenientInt(aux.PromptTokens),
		CompletionTokens: lenientInt(aux.CompletionTokens),
		CachedTokens:     lenientInt(aux.CachedTokens),
		CostUSD:          lenientFloat(aux.CostUSD),
		Extra:            aux.Extra,
	}
	return nil
}

// UnmarshalJSON decodes final metrics leniently, like Metrics.
func (m *FinalMetrics) UnmarshalJSON(data []byte) error {
	var aux struct {
		TotalPromptTokens     json.RawMessage `json:"total_prompt_tokens"`
		TotalCompletionTokens json.RawMessage `json:"total_completion_tokens"`
		TotalCachedTokens     json.RawMessage `json:"total_cached_tokens"`
		TotalCostUSD          json.RawMessage `json:"total_cost_usd"`
		TotalSteps            json.RawMessage `json:"total_steps"`
		Extra                 json.RawMessage `json:"extra,omitempty"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return fmt.Errorf("decode ATIF final metrics: %w", err)
	}
	*m = FinalMetrics{
		TotalPromptTokens:     lenientInt(aux.TotalPromptTokens),
		TotalCompletionTokens: lenientInt(aux.TotalCompletionTokens),
		TotalCachedTokens:     lenientInt(aux.TotalCachedTokens),
		TotalCostUSD:          lenientFloat(aux.TotalCostUSD),
		Extra:                 aux.Extra,
	}
	if n := lenientInt(aux.TotalSteps); n != nil {
		v := int(clamp(*n))
		m.TotalSteps = &v
	}
	return nil
}

// clamp keeps n in the range of a 32-bit int, so the conversion to int is safe everywhere.
func clamp(n int64) int32 {
	switch {
	case n > math.MaxInt32:
		return math.MaxInt32
	case n < math.MinInt32:
		return math.MinInt32
	}
	return int32(n)
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// lenientFloat reads a JSON number or numeric string; anything else is unknown.
func lenientFloat(raw json.RawMessage) *float64 {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isNull(raw) {
		return nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil
		}
		return &f
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
			return &v
		}
	}
	return nil
}

// lenientInt reads a JSON integer (or a whole float, or a numeric string); anything else is unknown.
func lenientInt(raw json.RawMessage) *int64 {
	f := lenientFloat(raw)
	if f == nil || *f != math.Trunc(*f) || math.Abs(*f) > 1<<53 {
		return nil
	}
	n := int64(*f)
	return &n
}

func lenientBool(raw json.RawMessage) *bool {
	var b bool
	if len(raw) == 0 || json.Unmarshal(raw, &b) != nil {
		return nil
	}
	return &b
}

// textOf returns the text of an ATIF message or content: a string as is, a
// list of parts as their texts joined by newlines (an image part becomes
// "[image]"), an object by its text field, and other JSON as written.
func textOf(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isNull(raw) {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	case '[':
		var parts []json.RawMessage
		if err := json.Unmarshal(raw, &parts); err == nil {
			texts := make([]string, 0, len(parts))
			for _, p := range parts {
				if t := partText(p); t != "" {
					texts = append(texts, t)
				}
			}
			return strings.Join(texts, "\n")
		}
	case '{':
		return partText(raw)
	}
	return string(raw)
}

// partText returns the text of one content part.
func partText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] != '{' {
		return textOf(raw)
	}
	var part map[string]json.RawMessage
	if err := json.Unmarshal(raw, &part); err != nil {
		return string(raw)
	}
	kind := jsonString(part["type"])
	if strings.Contains(kind, "image") {
		return "[image]"
	}
	for _, key := range []string{"text", "content", "thinking"} {
		if v, ok := part[key]; ok {
			return textOf(v)
		}
	}
	if kind != "" {
		return "[" + kind + "]"
	}
	return string(raw)
}

// member is one member of a JSON object, in document order.
type member struct {
	Key string
	Val json.RawMessage
}

// objectMembers returns the members of a JSON object in document order, or nil when raw is not an object.
func objectMembers(raw json.RawMessage) []member {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var out []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		key, ok := tok.(string)
		if !ok {
			return out
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return out
		}
		out = append(out, member{Key: key, Val: val})
	}
	return out
}

// object decodes a JSON object into a map; nil when raw is not an object.
func object(raw json.RawMessage) map[string]json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// array decodes a JSON array; nil when raw is not an array.
func array(raw json.RawMessage) []json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var a []json.RawMessage
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil
	}
	return a
}

// jsonString returns raw as a string when it is a JSON string, else "".
func jsonString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// jsonBool returns raw as a bool when it is a JSON boolean.
func jsonBool(raw json.RawMessage) *bool {
	return lenientBool(bytes.TrimSpace(raw))
}

// jsonNumber returns raw as a number when it is a JSON number (not a string).
func jsonNumber(raw json.RawMessage) *float64 {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] == '"' {
		return nil
	}
	return lenientFloat(raw)
}

// dig walks nested objects by key.
func dig(raw json.RawMessage, keys ...string) json.RawMessage {
	for _, k := range keys {
		m := object(raw)
		if m == nil {
			return nil
		}
		raw = m[k]
	}
	return raw
}
