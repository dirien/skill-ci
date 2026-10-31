package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
)

// skillBaseDir starts the user turn in which Claude Code loads a skill's instructions.
const skillBaseDir = "Base directory for this skill:"

// buildCtx holds what every part of a session view needs.
type buildCtx struct {
	in    Input
	idx   *Index
	t     *Trial
	entry *IndexTrial
	cse   *Case
	rew   *reward
	ms    []metric
	r     *Redactor
	// render counts render-time replacements outside the trial document.
	render int
}

// str redacts a string that does not come from the (already redacted) trial document.
func (c *buildCtx) str(s string) string {
	out, n := c.r.String(s)
	c.render += n
	return out
}

func (c *buildCtx) strs(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, c.str(s))
	}
	return out
}

// Build makes the session view of one trial: header, metadata, log entries
// with categories and counts, criteria, findings, files, judge metrics and
// network flows. Every string goes through render-time redaction, and the
// view fails with ErrLeak when an exact value survives it.
func Build(in Input) (apiv1.TrialSession, error) {
	if in.Index == nil || in.Trial == nil {
		return apiv1.TrialSession{}, fmt.Errorf("session: build needs an index and a trial document")
	}
	red, n, err := in.Redactor.redactTrial(in.Trial)
	if err != nil {
		return apiv1.TrialSession{}, err
	}
	c := &buildCtx{in: in, idx: in.Index, t: red, r: in.Redactor, render: n}
	c.entry = findTrial(in.Index, red.ID)
	c.cse = findCase(in.Index, red.Case)
	c.rew = parseReward(red.Reward)
	c.ms = c.rew.metrics()

	view := apiv1.TrialSession{
		Run:         in.Run,
		Trial:       c.cell(),
		Header:      c.header(),
		Metadata:    c.metadata(),
		Criteria:    criteria(c.caseForCriteria(), c.rew, c.ms),
		Files:       []apiv1.FileChange{},
		Judge:       c.judge(),
		Truncations: truncations(red.Truncations),
		Downloads:   apiv1.Downloads{},
	}
	if red.Network != nil {
		net, err := MapNetwork(red.Network, red.Trajectory)
		if err != nil {
			return apiv1.TrialSession{}, err
		}
		view.Network = net
	}
	view.Log, view.Files = c.log(view.Network)
	view.Findings = findings(findingInput{
		metrics:         c.ms,
		reward:          c.rew,
		exception:       view.Header.Error,
		network:         view.Network,
		skillNotInvoked: view.Metadata.Skill.State == apiv1.SkillLoaded,
		suggestions:     c.strs(in.Suggestions),
	})
	if red.Trajectory != nil {
		view.Downloads.Trajectory = in.TrajectoryHref
	}
	view.Redaction = apiv1.RedactionCounts{
		Version: red.Redaction.Version, Exact: red.Redaction.Exact, Pattern: red.Redaction.Pattern,
		EnvDump: red.Redaction.EnvDump, Render: c.render,
	}
	if err := checkLeak(in.Redactor, view); err != nil {
		return apiv1.TrialSession{}, err
	}
	return view, nil
}

// checkLeak serializes v and fails closed when an exact value is still in it.
func checkLeak(r *Redactor, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("session: encode view: %w", err)
	}
	if r.Leaks(data) {
		return ErrLeak
	}
	return nil
}

func findTrial(idx *Index, id string) *IndexTrial {
	for i := range idx.Trials {
		if idx.Trials[i].ID == id {
			return &idx.Trials[i]
		}
	}
	return nil
}

func findCase(idx *Index, id string) *Case {
	for i := range idx.Cases {
		if idx.Cases[i].ID == id {
			return &idx.Cases[i]
		}
	}
	return nil
}

// caseForCriteria returns the case with redacted behavior texts.
func (c *buildCtx) caseForCriteria() *Case {
	if c.cse == nil {
		return nil
	}
	cp := *c.cse
	cp.ExpectedBehaviors = c.strs(c.cse.ExpectedBehaviors)
	return &cp
}

// cell is the grid cell of the trial, from its index entry (or the document when the index has none).
func (c *buildCtx) cell() apiv1.TrialCell {
	if c.entry != nil {
		cell := indexCell(*c.entry)
		cell.ErrorType = c.str(cell.ErrorType)
		return cell
	}
	t := c.t
	cell := apiv1.TrialCell{ID: t.ID, Arm: t.Arm, Case: t.Case, Attempt: t.Attempt, Status: t.Status, HasSession: true}
	if t.Trajectory != nil {
		cell.Steps = len(t.Trajectory.Steps)
	}
	return cell
}

// indexCell turns an index entry into a grid cell.
func indexCell(e IndexTrial) apiv1.TrialCell {
	cell := apiv1.TrialCell{
		ID: e.ID, Arm: e.Arm, Case: e.Case, Attempt: e.Attempt, Status: e.Status,
		Score: e.Score, Passed: e.Passed, ErrorType: e.ErrorType,
		DurationSec: e.DurationSec, AgentSec: e.AgentSec, Steps: e.Steps, ToolCalls: e.ToolCalls,
		TokensIn: e.TokensIn, TokensOut: e.TokensOut, TokensCached: e.TokensCached,
		CostUSD: e.CostUSD, CostEstimated: e.CostSource == "estimate",
		SkillInvoked: e.SkillInvoked, HasSession: true,
	}
	if e.Criteria != nil {
		cell.Criteria = &apiv1.Ratio{Passed: e.Criteria.Passed, Total: e.Criteria.Total}
	}
	if e.Network != nil {
		cell.Dropped = e.Network.Dropped
	}
	return cell
}

func (c *buildCtx) header() apiv1.TrialHeader {
	t := c.t
	h := apiv1.TrialHeader{Prompt: t.Prompt, Status: t.Status, Dimensions: []apiv1.TrialDimension{}}
	if c.entry != nil && c.entry.Score != nil {
		h.Score = c.entry.Score
	} else if c.rew != nil {
		h.Score = jsonNumber(c.rew.top["overall"])
	}
	h.ArmMean = armMean(c.idx, t.Arm)
	singleScored := armScored(c.idx, t.Arm) == 1
	for _, m := range c.ms {
		if m.score == nil {
			continue
		}
		d := apiv1.TrialDimension{Key: m.key, Label: m.label, Score: *m.score}
		if v, ok := c.in.ArmMeans[m.key]; ok {
			d.ArmMean = &v
		} else if singleScored && t.Status == apiv1.StatusScored {
			v := *m.score
			d.ArmMean = &v
		}
		h.Dimensions = append(h.Dimensions, d)
	}
	h.Error = trialError(t)
	h.Final = finalMessage(t.Trajectory)
	return h
}

// trialError is the exception of a trial, from meta.exception or failure.json.
func trialError(t *Trial) *apiv1.TrialError {
	if e := t.Meta.Exception; e != nil {
		return &apiv1.TrialError{Type: e.Type, Message: e.Message, OccurredAt: utc(e.OccurredAt), Traceback: e.Traceback}
	}
	if t.Status == apiv1.StatusScored {
		return nil
	}
	errObj := dig(t.Failure, "error")
	typ, msg := jsonString(dig(errObj, "type")), jsonString(dig(errObj, "message"))
	if typ == "" && msg == "" {
		return nil
	}
	te := &apiv1.TrialError{Type: typ, Message: msg}
	if at, err := time.Parse(time.RFC3339Nano, jsonString(dig(errObj, "occurred_at"))); err == nil {
		te.OccurredAt = utc(&at)
	}
	return te
}

// armMean is the mean score of the scored trials of an arm, rounded to 3 decimals.
func armMean(idx *Index, arm string) *float64 {
	sum, n := 0.0, 0
	for _, e := range idx.Trials {
		if e.Arm == arm && e.Score != nil {
			sum += *e.Score
			n++
		}
	}
	if n == 0 {
		return nil
	}
	v := round(sum/float64(n), 3)
	return &v
}

func armScored(idx *Index, arm string) int {
	n := 0
	for _, e := range idx.Trials {
		if e.Arm == arm && e.Score != nil {
			n++
		}
	}
	return n
}

// finalMessage is the last non-empty agent message.
func finalMessage(traj *Trajectory) string {
	if traj == nil {
		return ""
	}
	for i := len(traj.Steps) - 1; i >= 0; i-- {
		s := traj.Steps[i]
		if s.Source == "agent" {
			if text := strings.TrimSpace(textOf(s.Message)); text != "" {
				return textOf(s.Message)
			}
		}
	}
	return ""
}

func (c *buildCtx) metadata() apiv1.TrialMetadata {
	t, idx := c.t, c.idx
	md := apiv1.TrialMetadata{
		Agent: first(t.Meta.Agent.Name, idx.Agent), AgentVersion: first(t.Meta.Agent.Version, idx.AgentVersion),
		Model: first(idx.Model, t.Meta.Agent.Model), Provider: t.Meta.Agent.Provider,
		Arm: t.Arm, Case: t.Case, Attempt: t.Attempt, AttemptsPerCase: idx.AttemptsPerCase,
		Credentials: credentials(idx.Credentials), Phases: []apiv1.Phase{},
		StartedAt: utc(t.Meta.StartedAt), FinishedAt: utc(t.Meta.FinishedAt),
		Trigger: c.in.Trigger, PushedBy: c.in.PushedBy,
	}
	if traj := t.Trajectory; traj != nil {
		md.SchemaVersion = traj.SchemaVersion
		var cwds []string
		if json.Unmarshal(dig(traj.Agent.Extra, "cwds"), &cwds) == nil && len(cwds) > 0 {
			md.Workspace = cwds[0]
		}
	}
	md.Skill = c.skillUse()
	if c.entry != nil && c.entry.DurationSec != nil {
		md.DurationSec = c.entry.DurationSec
	} else if md.StartedAt != nil && md.FinishedAt != nil {
		v := round(md.FinishedAt.Sub(*md.StartedAt).Seconds(), 1)
		md.DurationSec = &v
	}
	for _, p := range t.Meta.Phases {
		md.Phases = append(md.Phases, apiv1.Phase{
			Name: p.Name, Label: phaseLabel(p.Name), StartedAt: p.StartedAt.UTC(), FinishedAt: p.FinishedAt.UTC(),
			DurationSec: round(p.FinishedAt.Sub(p.StartedAt).Seconds(), 1),
		})
	}
	md.Tokens = c.tokens()
	md.CostUSD, md.CostEstimated = c.cost()
	cr := criteria(c.cse, c.rew, nil)
	md.Criteria = apiv1.Ratio{Passed: cr.Passed, Total: cr.Total}
	return md
}

func (c *buildCtx) tokens() apiv1.Tokens {
	if traj := c.t.Trajectory; traj != nil && traj.FinalMetrics != nil {
		fm := traj.FinalMetrics
		return apiv1.Tokens{In: fm.TotalPromptTokens, Out: fm.TotalCompletionTokens, Cached: fm.TotalCachedTokens}
	}
	if e := c.entry; e != nil && (e.TokensIn != nil || e.TokensOut != nil || e.TokensCached != nil) {
		return apiv1.Tokens{In: e.TokensIn, Out: e.TokensOut, Cached: e.TokensCached}
	}
	if ar := c.t.Meta.AgentResult; ar != nil {
		return apiv1.Tokens{In: ar.TokensIn, Out: ar.TokensOut, Cached: ar.TokensCached}
	}
	return apiv1.Tokens{}
}

func (c *buildCtx) cost() (*float64, bool) {
	if e := c.entry; e != nil && e.CostUSD != nil {
		return e.CostUSD, e.CostSource == "estimate"
	}
	if traj := c.t.Trajectory; traj != nil && traj.FinalMetrics != nil && traj.FinalMetrics.TotalCostUSD != nil {
		return traj.FinalMetrics.TotalCostUSD, jsonString(dig(traj.FinalMetrics.Extra, "cost_source")) == "litellm_estimate"
	}
	if ar := c.t.Meta.AgentResult; ar != nil && ar.CostUSD != nil {
		return ar.CostUSD, false
	}
	return nil, false
}

// skillUse says whether the trial used the skill.
func (c *buildCtx) skillUse() apiv1.SkillUse {
	u := apiv1.SkillUse{}
	if c.cse != nil {
		u.Expected = c.cse.ExpectedSkill
	}
	switch {
	case c.t.Status != apiv1.StatusScored:
		u.State = apiv1.SkillUnknown
		return u
	case c.t.Arm == apiv1.ArmWithoutSkill:
		u.State = apiv1.SkillNotUsed
		return u
	}
	var activation json.RawMessage
	if c.rew != nil {
		activation = dig(c.rew.details["skill_execution"], "activation")
	}
	u.Reason = jsonString(dig(activation, "reason"))
	invoked := jsonBool(c.rewardTop("skill_invoked"))
	if invoked == nil {
		invoked = jsonBool(dig(activation, "passed"))
	}
	switch {
	case invoked == nil:
		u.State = apiv1.SkillUnknown
	case *invoked:
		u.State = apiv1.SkillInvoked
		u.Matched = jsonString(dig(activation, "details", "matched_skill"))
	default:
		u.State = apiv1.SkillLoaded
	}
	return u
}

func (c *buildCtx) rewardTop(key string) json.RawMessage {
	if c.rew == nil {
		return nil
	}
	return c.rew.top[key]
}

func (c *buildCtx) judge() []apiv1.JudgeMetric {
	out := make([]apiv1.JudgeMetric, 0, len(c.ms))
	for _, m := range c.ms {
		out = append(out, apiv1.JudgeMetric{
			Key: m.key, Label: m.label, Score: m.score, Passed: m.passed, Reason: m.reason,
			Model: m.model, Steps: m.steps, Details: apiv1.RawJSON(m.details),
		})
	}
	return out
}

func credentials(list []Credential) []apiv1.Credential {
	out := make([]apiv1.Credential, 0, len(list))
	for _, c := range list {
		out = append(out, apiv1.Credential{Role: c.Role, Env: c.Env, Host: c.Host})
	}
	return out
}

func truncations(list []Truncation) []apiv1.Truncation {
	out := make([]apiv1.Truncation, 0, len(list))
	for _, t := range list {
		out = append(out, apiv1.Truncation{Pointer: t.Pointer, OriginalBytes: t.OriginalBytes})
	}
	return out
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func round(v float64, decimals int) float64 {
	p := math.Pow(10, float64(decimals))
	return math.Round(v*p) / p
}

func itoa(n int) string { return strconv.Itoa(n) }

// indentJSON returns raw as indented JSON, or as written when it is not JSON.
func indentJSON(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}
