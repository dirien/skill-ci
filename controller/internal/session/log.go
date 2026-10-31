package session

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
)

// stepEntries collects the entries of one step and numbers their ids.
type stepEntries struct {
	step    int
	entries []apiv1.LogEntry
}

func (s *stepEntries) add(e apiv1.LogEntry) *apiv1.LogEntry {
	e.Step = s.step
	if len(s.entries) == 0 {
		e.ID = "s-" + itoa(s.step)
	} else {
		e.ID = "s-" + itoa(s.step) + "-" + itoa(len(s.entries)+1)
	}
	s.entries = append(s.entries, e)
	return &s.entries[len(s.entries)-1]
}

// log builds the session log and the files from the trajectory, the network
// rows and the judge metrics.
func (c *buildCtx) log(net *apiv1.TrialNetwork) (apiv1.TrialLog, []apiv1.FileChange) {
	files := []apiv1.FileChange{}
	var entries []apiv1.LogEntry
	var flows []apiv1.FlowRow
	if net != nil {
		flows = net.Flows
	}
	if traj := c.t.Trajectory; traj != nil {
		origin := firstTime(traj)
		byStep := map[int][]apiv1.FlowRow{}
		for _, f := range flows {
			byStep[f.Step] = append(byStep[f.Step], f)
		}
		n := 0
		flowAt := func(step int) []apiv1.LogEntry {
			var out []apiv1.LogEntry
			for _, f := range byStep[step] {
				n++
				out = append(out, flowEntry(n, f, origin))
			}
			return out
		}
		entries = append(entries, flowAt(0)...)
		prompt := true
		for i, s := range traj.Steps {
			id := stepID(i, s)
			se := &stepEntries{step: id}
			stepFiles := c.stepEntries(se, i, s, &prompt, origin)
			files = append(files, stepFiles...)
			entries = append(entries, se.entries...)
			entries = append(entries, flowAt(id)...)
		}
	} else {
		for i, f := range flows {
			entries = append(entries, flowEntry(i+1, f, nil))
		}
	}
	entries = append(entries, c.judgeEntries()...)
	return apiv1.TrialLog{Entries: entriesOrEmpty(entries), Counts: counts(entries), Jumps: jumps(entries)}, files
}

func entriesOrEmpty(e []apiv1.LogEntry) []apiv1.LogEntry {
	if e == nil {
		return []apiv1.LogEntry{}
	}
	return e
}

// stepID is the ATIF step_id, or the index plus one when the step has none.
func stepID(i int, s Step) int {
	if s.StepID > 0 {
		return s.StepID
	}
	return i + 1
}

// firstTime is the timestamp of the first step that has one.
func firstTime(traj *Trajectory) *time.Time {
	for _, s := range traj.Steps {
		if t, ok := parseTime(s.Timestamp); ok {
			return &t
		}
	}
	return nil
}

func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// stepEntries adds the entries of one step: reasoning, message, tool calls,
// usage and metadata, and returns the files its tool calls changed.
func (c *buildCtx) stepEntries(se *stepEntries, i int, s Step, prompt *bool, origin *time.Time) []apiv1.FileChange {
	base := apiv1.LogEntry{Source: s.Source}
	if base.Source == "" {
		base.Source = "agent"
	}
	if t, ok := parseTime(s.Timestamp); ok {
		base.At = &t
		if origin != nil {
			off := round(t.Sub(*origin).Seconds(), 1)
			base.OffsetSec = &off
		}
	}
	label := func(what string) string { return "Step " + itoa(se.step) + ", " + what }

	if s.ReasoningContent != "" {
		e := base
		e.Category, e.Title, e.Label = apiv1.CategoryThink, "Thinking", label("thinking")
		e.Text, e.TextTruncated = cutText(s.ReasoningContent, ThinkPreviewBytes)
		se.add(e)
	}
	if text := textOf(s.Message); strings.TrimSpace(text) != "" {
		e := base
		switch {
		case s.Source == "user" && *prompt:
			e.Category, e.Title, e.Label = apiv1.CategoryUser, "Prompt", label("user turn")
		case s.Source == "user" && strings.HasPrefix(strings.TrimSpace(text), skillBaseDir):
			e.Category, e.Label = apiv1.CategorySkill, label("skill")
			e.Title = "Skill instructions loaded"
			line, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(text), skillBaseDir), "\n")
			if dir := strings.TrimSpace(cutBytes(line, 300)); dir != "" {
				e.Title += ": " + path.Base(dir)
			}
		case s.Source == "user":
			e.Category, e.Title, e.Label = apiv1.CategoryUser, "User", label("user turn")
		case s.Source == "system":
			e.Category, e.Title, e.Label = apiv1.CategoryMeta, "System message", label("system message")
		default:
			e.Category, e.Title, e.Label = apiv1.CategoryMsg, "Message", label("message")
		}
		e.Text, e.TextTruncated = cutText(text, OutputPreviewBytes)
		se.add(e)
	}
	if s.Source == "user" {
		*prompt = false
	}

	var files []apiv1.FileChange
	used := map[int]bool{}
	var results []ObservationResult
	if s.Observation != nil {
		results = s.Observation.Results
	}
	for j, tc := range s.ToolCalls {
		k := matchResult(results, tc.ToolCallID, j, used)
		ti := classify(tc)
		call := &apiv1.ToolCall{
			Name: tc.FunctionName, CallID: tc.ToolCallID, Kind: ti.kind,
			Command: ti.command, Path: ti.path, Input: shortenInput(indentJSON(tc.Arguments)),
		}
		failed := false
		if k >= 0 {
			call.Output = c.output(i, se.step, k, results[k], s, tc)
			failed = call.Output.Error
		} else {
			failed = callFailed(tc, nil, s)
		}
		e := base
		e.Category, e.Title, e.Tool, e.Error = ti.category, ti.title, call, failed
		e.Label = label(tc.FunctionName + " " + categoryLabel(ti.category))
		if failed {
			e.Label += ", failed"
		}
		added := se.add(e)
		files = append(files, c.fileChanges(i, j, se.step, added.ID, tc, ti)...)
	}
	for k, res := range results {
		if used[k] {
			continue
		}
		e := base
		text := textOf(res.Content)
		if len(res.SubagentTrajectoryRef) > 0 && !isNull(res.SubagentTrajectoryRef) {
			e.Category, e.Title, e.Label = apiv1.CategoryMeta, "Subagent trajectory (summary only)", label("subagent")
			e.Text, e.TextTruncated = cutText(text, OutputPreviewBytes)
		} else {
			e.Category, e.Title, e.Label = apiv1.CategoryTools, "Tool result", label("tool result")
			e.Tool = &apiv1.ToolCall{Name: "", CallID: res.SourceCallID, Kind: apiv1.ToolOther, Input: "", Output: c.output(i, se.step, k, res, s, ToolCall{})}
			e.Error = e.Tool.Output.Error
		}
		se.add(e)
	}

	if u := usage(s.Metrics); u != nil {
		e := base
		e.Category, e.Usage, e.Label = apiv1.CategoryUsage, u, label("usage")
		e.Title = usageTitle(u)
		se.add(e)
	}
	if title := metaTitle(s); title != "" {
		e := base
		e.Category, e.Title, e.Label = apiv1.CategoryMeta, title, label("metadata")
		se.add(e)
	}
	if len(se.entries) == 0 {
		e := base
		e.Category, e.Title, e.Label = apiv1.CategoryMeta, "Empty step", label("empty step")
		se.add(e)
	}
	return files
}

// matchResult finds the observation result of call j: by source_call_id, else by position.
func matchResult(results []ObservationResult, callID string, j int, used map[int]bool) int {
	if callID != "" {
		for k, r := range results {
			if !used[k] && r.SourceCallID == callID {
				used[k] = true
				return k
			}
		}
	}
	if j < len(results) && !used[j] && (results[j].SourceCallID == "" || callID == "") {
		used[j] = true
		return j
	}
	return -1
}

func categoryLabel(cat string) string {
	switch cat {
	case apiv1.CategorySkill:
		return "skill"
	case apiv1.CategoryPkg:
		return "package install"
	case apiv1.CategoryWeb:
		return "web request"
	}
	return "tool call"
}

// output builds the preview of observation result k of step index i.
func (c *buildCtx) output(i, step, k int, res ObservationResult, s Step, tc ToolCall) *apiv1.ToolOutput {
	text := textOf(res.Content)
	preview, cut := previewOf(text)
	out := &apiv1.ToolOutput{
		Preview: preview, Bytes: len(text), Lines: countLines(text), Truncated: cut,
		Shortened: c.shortened(fmt.Sprintf("/trajectory/steps/%d/observation/results/%d", i, k)),
		Error:     callFailed(tc, &res, s),
	}
	if c.in.OutputHref != nil {
		out.Href = c.in.OutputHref(step, k)
	}
	return out
}

// shortened reports whether the runner shortened the string at prefix or below it.
func (c *buildCtx) shortened(prefix string) bool {
	for _, t := range c.t.Truncations {
		if t.Pointer == prefix || strings.HasPrefix(t.Pointer, prefix+"/") {
			return true
		}
	}
	return false
}

// previewOf cuts text to OutputPreviewLines lines, then to OutputPreviewBytes.
func previewOf(text string) (string, bool) {
	p := text
	if idx := nthIndex(p, '\n', OutputPreviewLines); idx >= 0 && idx+1 < len(p) {
		p = p[:idx]
	}
	p = cutBytes(p, OutputPreviewBytes)
	return p, len(p) < len(text)
}

// nthIndex returns the index of the nth occurrence of b in s, or -1.
func nthIndex(s string, b byte, n int) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			n--
			if n == 0 {
				return i
			}
		}
	}
	return -1
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// cutText cuts s to n bytes on a rune boundary.
func cutText(s string, n int) (string, bool) {
	c := cutBytes(s, n)
	return c, len(c) < len(s)
}

func shortenInput(s string) string {
	if len(s) <= InputPreviewBytes {
		return s
	}
	return cutBytes(s, InputPreviewBytes) + "\n... [shortened to 4 KiB]"
}

// callFailed reports whether a tool call failed: an error flag, a non-zero
// exit code or a failed status in the call, its result or the step's
// per-call details (codex), or the markers Harbor writes into the content.
func callFailed(tc ToolCall, res *ObservationResult, s Step) bool {
	var places []json.RawMessage
	places = append(places, tc.Extra)
	if res != nil {
		places = append(places, res.Extra)
	}
	if tc.ToolCallID != "" {
		places = append(places, dig(s.Extra, "tool_call_details", tc.ToolCallID))
	}
	if len(s.ToolCalls) == 1 {
		if st := object(s.Extra); st != nil {
			flat, _ := json.Marshal(map[string]json.RawMessage{"status": st["status"], "exit_code": st["exit_code"]})
			places = append(places, flat)
		}
	}
	for _, p := range places {
		if failedIn(p, 0) {
			return true
		}
	}
	if res != nil {
		text := textOf(res.Content)
		if strings.Contains(text, "[error] tool reported failure") {
			return true
		}
		for _, line := range strings.Split(text, "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "[exit_code] "); ok && strings.TrimSpace(rest) != "0" {
				return true
			}
		}
	}
	return false
}

var errorFlags = set("is_error", "iserror", "tool_result_is_error", "tool_use_is_error", "error")

var failedStatuses = set("failed", "failure", "error", "errored")

// failedIn searches an extra object (three levels deep) for a failure.
func failedIn(raw json.RawMessage, depth int) bool {
	if depth > 3 {
		return false
	}
	for _, m := range objectMembers(raw) {
		key := strings.ToLower(m.Key)
		switch {
		case errorFlags[key]:
			if b := jsonBool(m.Val); b != nil && *b {
				return true
			}
		case key == "exit_code" || key == "exitcode":
			if n := jsonNumber(m.Val); n != nil && *n != 0 {
				return true
			}
		case key == "status":
			if failedStatuses[strings.ToLower(jsonString(m.Val))] {
				return true
			}
		}
		if len(m.Val) > 0 && m.Val[0] == '{' && failedIn(m.Val, depth+1) {
			return true
		}
	}
	return false
}

// usage is the usage of a step, nil when it has none.
func usage(m *Metrics) *apiv1.Usage {
	if m == nil || m.PromptTokens == nil && m.CompletionTokens == nil && m.CachedTokens == nil && m.CostUSD == nil {
		return nil
	}
	return &apiv1.Usage{
		Tokens:    apiv1.Tokens{In: m.PromptTokens, Out: m.CompletionTokens, Cached: m.CachedTokens},
		CostUSD:   m.CostUSD,
		Estimated: m.CostUSD != nil && jsonString(dig(m.Extra, "cost_source")) == "litellm_estimate",
	}
}

func usageTitle(u *apiv1.Usage) string {
	var parts []string
	if u.Tokens.In != nil {
		parts = append(parts, fmt.Sprintf("%d in", *u.Tokens.In))
	}
	if u.Tokens.Out != nil {
		parts = append(parts, fmt.Sprintf("%d out", *u.Tokens.Out))
	}
	if u.Tokens.Cached != nil {
		parts = append(parts, fmt.Sprintf("%d cached", *u.Tokens.Cached))
	}
	if u.CostUSD != nil {
		cost := fmt.Sprintf("$%.4f", *u.CostUSD)
		if u.Estimated {
			cost += " est."
		}
		parts = append(parts, cost)
	}
	return "Usage: " + strings.Join(parts, ", ")
}

// metaTitle summarizes a step's metadata: stop reason, working directory and copied context.
func metaTitle(s Step) string {
	var parts []string
	if v := jsonString(dig(s.Extra, "stop_reason")); v != "" {
		parts = append(parts, "stop_reason "+v)
	}
	if v := jsonString(dig(s.Extra, "cwd")); v != "" {
		parts = append(parts, "cwd "+v)
	}
	if s.IsCopiedContext != nil && *s.IsCopiedContext {
		parts = append(parts, "copied context")
	}
	return strings.Join(parts, ", ")
}

func flowEntry(n int, f apiv1.FlowRow, origin *time.Time) apiv1.LogEntry {
	at := f.FirstAt
	e := apiv1.LogEntry{
		ID: "n-" + itoa(n), Category: apiv1.CategoryNet, Source: "network", At: &at,
		Title: fmt.Sprintf("%s %s:%d x%d", f.Verdict, f.Destination, f.Port, f.Count),
		Flow:  &f, Error: f.Verdict == "DROPPED" || f.Verdict == "ERROR",
		Label: fmt.Sprintf("Network, %s %s port %d", f.Verdict, f.Destination, f.Port),
	}
	if origin != nil {
		off := round(at.Sub(*origin).Seconds(), 1)
		e.OffsetSec = &off
	}
	return e
}

func (c *buildCtx) judgeEntries() []apiv1.LogEntry {
	var out []apiv1.LogEntry
	for _, m := range c.ms {
		e := apiv1.LogEntry{ID: "j-" + m.key, Category: apiv1.CategoryJudge, Source: "judge", Metric: m.key, Error: m.failed || m.low}
		e.Title = m.label
		e.Label = "Judge, " + m.label
		if m.score != nil {
			e.Title += " " + scoreText(m.score)
			e.Label += ", score " + scoreText(m.score)
		}
		switch {
		case m.failed:
			e.Title += " failed"
			e.Label += ", failed"
		case m.low:
			e.Title += " below 0.8"
			e.Label += ", below 0.8"
		}
		e.Text, e.TextTruncated = cutText(m.reason, OutputPreviewBytes)
		out = append(out, e)
	}
	return out
}

func counts(entries []apiv1.LogEntry) []apiv1.CategoryCount {
	n := map[string]int{}
	for _, e := range entries {
		n[e.Category]++
	}
	out := make([]apiv1.CategoryCount, 0, len(apiv1.Categories))
	for _, cat := range apiv1.Categories {
		out = append(out, apiv1.CategoryCount{Category: cat, Count: n[cat], Focus: slices.Contains(FocusCategories, cat)})
	}
	return out
}

// jumps are the first tool call (tools, web or pkg), the first skill entry,
// the first failed tool call and the final agent message.
func jumps(entries []apiv1.LogEntry) apiv1.Jumps {
	var j apiv1.Jumps
	for _, e := range entries {
		if e.Step == 0 {
			continue
		}
		switch {
		case j.FirstTool == "" && e.Tool != nil && e.Category != apiv1.CategorySkill:
			j.FirstTool = e.ID
		}
		if j.Skill == "" && e.Category == apiv1.CategorySkill {
			j.Skill = e.ID
		}
		if j.FirstError == "" && e.Error && e.Tool != nil {
			j.FirstError = e.ID
		}
		if e.Category == apiv1.CategoryMsg && e.Source == "agent" {
			j.Final = e.ID
		}
	}
	return j
}

// fileChanges returns the files tool call j of step index i changed.
func (c *buildCtx) fileChanges(i, j, step int, entryID string, tc ToolCall, ti toolInfo) []apiv1.FileChange {
	argPtr := fmt.Sprintf("/trajectory/steps/%d/tool_calls/%d/arguments", i, j)
	base := apiv1.FileChange{Path: ti.path, Step: step, EntryID: entryID, Tool: tc.FunctionName, Hunks: []apiv1.Hunk{}, Captured: true, Truncated: c.shortened(argPtr)}
	switch ti.kind {
	case apiv1.ToolWrite:
		f := base
		f.Op = apiv1.FileWrite
		f.Content = ti.arg("content", "file_text", "contents", "text")
		if f.Path == "" {
			return nil
		}
		return []apiv1.FileChange{f}
	case apiv1.ToolEdit:
		f := base
		f.Op = apiv1.FileEdit
		f.Hunks = hunks(ti)
		if f.Path == "" {
			return nil
		}
		return []apiv1.FileChange{f}
	case apiv1.ToolPatch:
		return patchChanges(base, ti.patchText())
	case apiv1.ToolShell:
		if strings.Contains(ti.command, "*** Begin Patch") && strings.Contains(ti.command, "apply_patch") {
			return patchChanges(base, ti.command)
		}
		var out []apiv1.FileChange
		for _, p := range redirectTargets(ti.command) {
			f := base
			f.Path, f.Op, f.Captured, f.Truncated = p, apiv1.FileCommand, false, false
			out = append(out, f)
		}
		return out
	}
	return nil
}

// hunks returns the old and new text pairs of an edit call: Edit and opencode
// edit (old_string/new_string, oldString/newString), MultiEdit (edits[]) and
// str_replace_editor (old_str/new_str).
func hunks(ti toolInfo) []apiv1.Hunk {
	out := []apiv1.Hunk{}
	pair := func(args map[string]json.RawMessage) {
		get := func(names ...string) (string, bool) {
			for _, n := range names {
				if v, ok := args[n]; ok {
					return jsonString(v), true
				}
			}
			return "", false
		}
		o, okOld := get("old_string", "oldString", "old_str", "old_text")
		n, okNew := get("new_string", "newString", "new_str", "new_text")
		if okOld || okNew {
			out = append(out, apiv1.Hunk{Old: o, New: n})
		}
	}
	pair(ti.args)
	for _, e := range array(ti.args["edits"]) {
		if obj := object(e); obj != nil {
			pair(obj)
		}
	}
	return out
}

// patchChanges returns one change per file of an apply_patch patch: the whole
// patch for a single file, the file's section when the patch has several.
func patchChanges(base apiv1.FileChange, patch string) []apiv1.FileChange {
	files := patchFile.FindAllStringSubmatchIndex(patch, -1)
	if len(files) == 0 {
		if base.Path == "" {
			return nil
		}
		base.Op, base.Patch = apiv1.FilePatch, patch
		return []apiv1.FileChange{base}
	}
	out := make([]apiv1.FileChange, 0, len(files))
	for n, m := range files {
		f := base
		f.Op, f.Path = apiv1.FilePatch, patch[m[2]:m[3]]
		if len(files) == 1 {
			f.Patch = patch
		} else {
			end := len(patch)
			if n+1 < len(files) {
				end = files[n+1][0]
			} else if k := strings.Index(patch[m[0]:], "*** End Patch"); k >= 0 {
				end = m[0] + k
			}
			f.Patch = patch[m[0]:end]
		}
		out = append(out, f)
	}
	return out
}
