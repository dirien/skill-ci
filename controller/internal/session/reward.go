package session

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
)

// lowScore is the score below which a metric is friction even when it passed.
const lowScore = 0.8

// metricOrder is the display order of SkillEvaluator's six metrics.
var metricOrder = []string{"accuracy", "goal_accuracy", "behavior_check", "skill_execution", "skill_efficiency", "security"}

// compositeMetrics are the metrics made of sub-checks.
var compositeMetrics = set("skill_execution", "skill_efficiency")

var metricLabels = map[string]string{
	"accuracy":         "Accuracy",
	"goal_accuracy":    "Goal accuracy",
	"behavior_check":   "Behavior check",
	"skill_execution":  "Skill execution",
	"skill_efficiency": "Skill efficiency",
	"security":         "Security",
	"overall":          "Overall",
}

var phaseLabels = map[string]string{
	"environment_setup": "Environment setup",
	"agent_setup":       "Agent setup",
	"agent_execution":   "Agent execution",
	"verifier":          "Verifier",
}

// humanize turns a snake_case key into a label: "script_execution" is "Script execution".
func humanize(key string) string {
	s := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(key, "_", " "), "-", " "))
	if s == "" {
		return key
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func metricLabel(key string) string {
	if l, ok := metricLabels[key]; ok {
		return l
	}
	return humanize(key)
}

func phaseLabel(name string) string {
	if l, ok := phaseLabels[name]; ok {
		return l
	}
	return humanize(name)
}

// reward is a parsed reward.json.
type reward struct {
	top     map[string]json.RawMessage
	details map[string]json.RawMessage
}

func parseReward(raw json.RawMessage) *reward {
	top := object(raw)
	if top == nil {
		return nil
	}
	return &reward{top: top, details: object(top["details"])}
}

// metric is one judge metric of a reward with what the session derives from it.
type metric struct {
	key, label string
	score      *float64
	passed     *bool
	reason     string
	model      string
	steps      []int
	details    json.RawMessage
	checks     []apiv1.Check
	failed     bool
	low        bool
}

// metrics returns the reward's metrics: the six in display order, then any
// other scored metric in key order.
func (r *reward) metrics() []metric {
	if r == nil {
		return nil
	}
	keys := []string{}
	known := set(metricOrder...)
	for _, k := range metricOrder {
		if _, ok := r.top[k]; ok {
			keys = append(keys, k)
		} else if _, ok := r.details[k]; ok {
			keys = append(keys, k)
		}
	}
	var extra []string
	for k, v := range r.top {
		if known[k] || k == "overall" {
			continue
		}
		if jsonNumber(v) != nil && r.details[k] != nil {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)
	out := make([]metric, 0, len(keys))
	for _, k := range keys {
		out = append(out, r.metric(k))
	}
	return out
}

func (r *reward) metric(key string) metric {
	d := r.details[key]
	m := metric{key: key, label: metricLabel(key), details: d, steps: evidenceSteps(d), checks: []apiv1.Check{}}
	m.score = jsonNumber(r.top[key])
	if m.score == nil {
		m.score = jsonNumber(dig(d, "score"))
	}
	m.passed = jsonBool(dig(d, "passed"))
	m.reason = jsonString(dig(d, "reason"))
	m.model = jsonString(dig(d, "judge_model_used"))
	if m.model == "" {
		m.model = jsonString(dig(d, "model"))
	}
	if subs := subChecks(key, d); len(subs) > 0 && (compositeMetrics[key] || m.reason == "") {
		m.checks = subs
		if m.passed == nil {
			all := true
			known := false
			for _, c := range subs {
				if c.Passed != nil {
					known = true
					all = all && *c.Passed
				}
			}
			if known {
				m.passed = &all
			}
		}
		if m.reason == "" {
			parts := make([]string, 0, len(subs))
			for _, c := range subs {
				if c.Reason != "" {
					parts = append(parts, c.Name+": "+c.Reason)
				}
			}
			m.reason = strings.Join(parts, "; ")
		}
	}
	m.failed = m.passed != nil && !*m.passed
	m.low = !m.failed && m.score != nil && *m.score < lowScore
	return m
}

// subChecks returns the sub-checks of a composite metric in document order.
func subChecks(key string, d json.RawMessage) []apiv1.Check {
	var out []apiv1.Check
	for _, mem := range objectMembers(d) {
		sub := object(mem.Val)
		if sub == nil || mem.Key == "details" || mem.Key == "omitted" {
			continue
		}
		_, hasPassed := sub["passed"]
		_, hasScore := sub["score"]
		if !hasPassed && !hasScore {
			continue
		}
		c := apiv1.Check{
			Metric: key,
			Name:   mem.Key,
			Label:  humanize(mem.Key),
			Passed: jsonBool(sub["passed"]),
			Score:  jsonNumber(sub["score"]),
			Reason: jsonString(sub["reason"]),
			Steps:  evidenceSteps(mem.Val),
		}
		c.Steps = mergeSteps(c.Steps, correctionSteps(mem.Val))
		out = append(out, c)
	}
	return out
}

// evidenceSteps maps the trajectory evidence pointers of a metric to ATIF steps, sorted and unique.
func evidenceSteps(d json.RawMessage) []int {
	steps := []int{}
	for _, ref := range array(dig(d, "evidence_refs")) {
		obj := object(ref)
		if obj == nil {
			continue
		}
		src := jsonString(obj["source"])
		if src != "" && src != "trajectory.json" {
			continue
		}
		if n, err := StepForPointer(jsonString(obj["json_pointer"])); err == nil {
			steps = append(steps, n)
		}
	}
	return mergeSteps(steps, nil)
}

// correctionSteps returns the steps of error_recovery corrections that name a
// step (counted from 0 like the evidence pointers).
func correctionSteps(d json.RawMessage) []int {
	var steps []int
	for _, c := range array(dig(d, "corrections")) {
		if n := jsonNumber(dig(c, "step")); n != nil && *n >= 0 && *n < 1<<20 {
			steps = append(steps, int(*n)+1)
		}
	}
	return steps
}

// mergeSteps returns the sorted union of a and b, never nil.
func mergeSteps(a, b []int) []int {
	seen := map[int]bool{}
	out := []int{}
	for _, list := range [][]int{a, b} {
		for _, n := range list {
			if n > 0 && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Ints(out)
	return out
}

// criteria builds the eval criteria of a trial.
func criteria(c *Case, r *reward, ms []metric) apiv1.Criteria {
	out := apiv1.Criteria{Behaviors: []apiv1.BehaviorResult{}, BehaviorSteps: []int{}, Checks: []apiv1.Check{}}
	var behaviors []string
	if c != nil {
		behaviors = c.ExpectedBehaviors
	}
	var results []json.RawMessage
	if r != nil {
		results = array(dig(r.details["behavior_check"], "results"))
	}
	byIndex := map[int]json.RawMessage{}
	for i, res := range results {
		idx := i + 1
		if n := jsonNumber(dig(res, "step")); n != nil && *n >= 1 && *n <= float64(len(results)+len(behaviors)) {
			idx = int(*n)
		}
		if _, dup := byIndex[idx]; !dup {
			byIndex[idx] = res
		}
	}
	total := len(behaviors)
	if total == 0 {
		total = len(results)
	}
	for i := 1; i <= total; i++ {
		b := apiv1.BehaviorResult{Index: i}
		if i <= len(behaviors) {
			b.Text = behaviors[i-1]
		}
		if res, ok := byIndex[i]; ok {
			b.Passed = jsonBool(dig(res, "passed"))
			b.Reason = jsonString(dig(res, "reason"))
			if b.Passed != nil && *b.Passed {
				out.Passed++
			}
		}
		out.Behaviors = append(out.Behaviors, b)
	}
	out.Total = total
	for _, m := range ms {
		switch {
		case m.key == "behavior_check":
			out.BehaviorSteps = m.steps
		case m.key == "accuracy":
			acc := &apiv1.AccuracyCriteria{Criteria: []apiv1.NamedCheck{}, Reason: m.reason, Steps: m.steps}
			for _, mem := range objectMembers(dig(m.details, "criteria")) {
				if b := jsonBool(mem.Val); b != nil {
					acc.Criteria = append(acc.Criteria, apiv1.NamedCheck{Name: mem.Key, Passed: *b})
				}
			}
			out.Accuracy = acc
		case compositeMetrics[m.key]:
			out.Checks = append(out.Checks, m.checks...)
		}
	}
	return out
}

// findings derives the session findings by rule.
func findings(in findingInput) []apiv1.Finding {
	out := []apiv1.Finding{}
	for _, m := range in.metrics {
		if !m.failed && !m.low {
			continue
		}
		rule := "metric-low"
		if m.failed {
			rule = "metric-failed"
		}
		out = append(out, apiv1.Finding{
			Kind: apiv1.FindingFriction, Rule: rule, Metric: m.key,
			Title: m.label + " " + scoreText(m.score), Detail: m.reason, Steps: m.steps,
		})
	}
	for _, m := range in.metrics {
		for _, c := range m.checks {
			if c.Passed == nil || *c.Passed {
				continue
			}
			out = append(out, apiv1.Finding{
				Kind: apiv1.FindingFriction, Rule: "check-failed", Metric: m.key,
				Title: c.Label + " failed", Detail: c.Reason, Steps: c.Steps,
			})
		}
	}
	if in.reward != nil {
		for _, c := range array(dig(in.reward.details["skill_execution"], "error_recovery", "corrections")) {
			out = append(out, correctionFinding(c))
		}
		for _, f := range array(dig(in.reward.details["security"], "findings")) {
			sev := jsonString(dig(f, "severity"))
			title := "Security finding"
			if sev != "" {
				title += " (" + sev + ")"
			}
			detail := jsonString(dig(f, "message"))
			if detail == "" {
				detail = textOf(f)
			}
			out = append(out, apiv1.Finding{
				Kind: apiv1.FindingFriction, Rule: "security-finding", Metric: "security",
				Title: title, Detail: detail, Steps: evidenceSteps(f),
			})
		}
		out = append(out, evaluationErrors(in.reward.top["evaluation_errors"])...)
	}
	if in.exception != nil {
		out = append(out, apiv1.Finding{
			Kind: apiv1.FindingFriction, Rule: "exception",
			Title: in.exception.Type, Detail: cutRunes(in.exception.Message, 300), Steps: []int{},
		})
	}
	if in.network != nil {
		for _, f := range in.network.Flows {
			if f.Verdict != "DROPPED" {
				continue
			}
			steps := []int{}
			if f.Step > 0 {
				steps = []int{f.Step}
			}
			out = append(out, apiv1.Finding{
				Kind: apiv1.FindingFriction, Rule: "flow-dropped",
				Title:  fmt.Sprintf("DROPPED %s:%d", f.Destination, f.Port),
				Detail: droppedDetail(f), Steps: steps,
			})
		}
	}
	if in.skillNotInvoked {
		out = append(out, apiv1.Finding{
			Kind: apiv1.FindingOpportunity, Rule: "skill-not-invoked",
			Title:  "Skill not invoked",
			Detail: "The skill was installed but the agent did not activate it: its description did not trigger for this prompt.",
			Steps:  []int{},
		})
	}
	for _, s := range in.suggestions {
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, apiv1.Finding{
			Kind: apiv1.FindingOpportunity, Rule: "suggestion", Title: "Suggestion (run-level)",
			Detail: s, Steps: []int{}, RunLevel: true,
		})
	}
	return out
}

type findingInput struct {
	metrics         []metric
	reward          *reward
	exception       *apiv1.TrialError
	network         *apiv1.TrialNetwork
	skillNotInvoked bool
	suggestions     []string
}

// correctionFinding describes one error_recovery correction: SkillEvaluator
// writes failed_cmd, retry_cmd, error and fault; older shapes step and description.
func correctionFinding(c json.RawMessage) apiv1.Finding {
	f := apiv1.Finding{Kind: apiv1.FindingFriction, Rule: "correction", Metric: "skill_execution", Steps: correctionSteps(json.RawMessage(`{"corrections":[` + string(c) + `]}`))}
	if f.Steps == nil {
		f.Steps = []int{}
	}
	failed := jsonString(dig(c, "failed_cmd"))
	desc := jsonString(dig(c, "description"))
	switch {
	case failed != "":
		f.Title = "Corrected: " + firstLine(failed, 120)
		parts := []string{"Failed: " + failed}
		if retry := jsonString(dig(c, "retry_cmd")); retry != "" {
			parts = append(parts, "retried as: "+retry)
		}
		if fault := jsonString(dig(c, "fault")); fault != "" {
			parts = append(parts, "fault: "+fault)
		}
		f.Detail = strings.Join(parts, "; ")
	case desc != "":
		f.Title = "Correction"
		if len(f.Steps) == 1 {
			f.Title = "Correction at step " + itoa(f.Steps[0])
		}
		f.Detail = desc
	default:
		f.Title = "Correction"
		f.Detail = textOf(c)
	}
	return f
}

// evaluationErrors turns the judge's evaluation_errors (a map, a list or a string) into findings.
func evaluationErrors(raw json.RawMessage) []apiv1.Finding {
	var details []string
	if mem := objectMembers(raw); mem != nil {
		for _, m := range mem {
			details = append(details, m.Key+": "+textOf(m.Val))
		}
	} else if list := array(raw); list != nil {
		for _, e := range list {
			details = append(details, textOf(e))
		}
	} else if s := jsonString(raw); s != "" {
		details = append(details, s)
	}
	out := make([]apiv1.Finding, 0, len(details))
	for _, d := range details {
		out = append(out, apiv1.Finding{
			Kind: apiv1.FindingFriction, Rule: "evaluation-error", Title: "Judge evaluation error", Detail: d, Steps: []int{},
		})
	}
	return out
}

func droppedDetail(f apiv1.FlowRow) string {
	conn := "connections"
	if f.Count == 1 {
		conn = "connection"
	}
	if f.Reason == "POLICY_DENIED" {
		return fmt.Sprintf("%d %s denied by the network policy (POLICY_DENIED).", f.Count, conn)
	}
	if f.Reason != "" {
		return fmt.Sprintf("%d %s dropped (%s).", f.Count, conn, f.Reason)
	}
	return fmt.Sprintf("%d %s dropped.", f.Count, conn)
}

// scoreText formats a score with two decimals, or "no score".
func scoreText(score *float64) string {
	if score == nil {
		return "no score"
	}
	return fmt.Sprintf("%.2f", *score)
}

// cutRunes returns the first n characters of s.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
