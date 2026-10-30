package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Tier and publication values the runner reports.
const (
	// StatusPassed means every check of the tier passed.
	StatusPassed = "passed"
	// StatusFailed means a check of the tier failed.
	StatusFailed = "failed"
	// StatusIncomplete means a check could not produce trustworthy evidence.
	StatusIncomplete = "incomplete"
	// StatusSkipped means the tier was not run.
	StatusSkipped = "skipped"
)

// maxFindings caps the findings kept from a termination message, as CONTRACT.md does.
const maxFindings = 5

// maxFindingLen caps one finding so a chatty runner cannot bloat the status.
const maxFindingLen = 300

// Tiers is the outcome of the three SkillEvaluator tiers. A value the
// controller does not know, or a missing one, is the empty string: unknown.
type Tiers struct {
	// Tier1 is the static, security and quality tier.
	Tier1 string `json:"tier1"`
	// Tier2 is the deduplication tier.
	Tier2 string `json:"tier2"`
	// Tier3 is the live with and without skill evaluation.
	Tier3 string `json:"tier3"`
}

// Known reports whether the runner said anything about the tiers.
func (t Tiers) Known() bool { return t != Tiers{} }

// maxSamples caps the trial scores kept per arm, as the runner does.
const maxSamples = 50

// Samples are the per-trial overall scores of the two arms.
type Samples struct {
	// WithSkill holds one score per with-skill trial.
	WithSkill []float64 `json:"withSkill"`
	// WithoutSkill holds one score per baseline trial.
	WithoutSkill []float64 `json:"withoutSkill"`
}

// Result is the JSON the runner writes to /dev/termination-log.
type Result struct {
	// WithSkill is the score with the skill installed.
	WithSkill *float64 `json:"withSkill"`
	// WithoutSkill is the baseline score.
	WithoutSkill *float64 `json:"withoutSkill"`
	// Lift is WithSkill minus WithoutSkill.
	Lift *float64 `json:"lift"`
	// Report is the digest of the report the runner attached to the skill, if any.
	Report string `json:"report,omitempty"`
	// Samples holds the per-trial scores of both arms; nil when the runner did
	// not report them (older runners) or an arm has fewer than two trials.
	Samples *Samples `json:"samples,omitempty"`
	// Tiers is the outcome of the three tiers; empty fields mean unknown.
	Tiers Tiers `json:"tiers"`
	// Publication is SkillEvaluator's publication assessment: passed, failed
	// or incomplete; empty when the runner did not say.
	Publication string `json:"publication"`
	// Findings names the failing tier and check, at most five short strings.
	Findings []string `json:"findings"`
	// Error is set when the runner produced no scores.
	Error string `json:"error"`
}

// Rejected reports whether Tier 1 or Tier 2 failed.
func (r Result) Rejected() bool {
	return r.Tiers.Tier1 == StatusFailed || r.Tiers.Tier2 == StatusFailed
}

// HasScores reports whether the runner produced the with and without scores.
func (r Result) HasScores() bool { return r.WithSkill != nil && r.WithoutSkill != nil }

// ParseResult reads a termination message. A message that is not JSON as a
// whole may be the log tail Kubernetes falls back to, so the last line is tried
// before giving up.
func ParseResult(msg string) (Result, error) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return Result{}, errors.New("runner left no termination message")
	}
	var res Result
	if err := json.Unmarshal([]byte(msg), &res); err != nil {
		last := msg[strings.LastIndex(msg, "\n")+1:]
		if json.Unmarshal([]byte(last), &res) != nil {
			return Result{}, fmt.Errorf("termination message is not a result: %.200s", last)
		}
	}
	if !strings.HasPrefix(res.Report, "sha256:") {
		res.Report = ""
	}
	res.Tiers = Tiers{Tier1: tierValue(res.Tiers.Tier1), Tier2: tierValue(res.Tiers.Tier2), Tier3: tierValue(res.Tiers.Tier3)}
	res.Publication = publicationValue(res.Publication)
	res.Findings = cleanFindings(res.Findings)
	res.Samples = cleanSamples(res.Samples)
	if res.Error != "" {
		return res, nil
	}
	if !res.HasScores() {
		// Without Tier 3 there are no scores, which is a result and not a failure.
		if res.Rejected() || res.Tiers.Tier3 == StatusSkipped {
			res.Lift = nil
			return res, nil
		}
		return Result{}, errors.New("termination message has no withSkill and withoutSkill scores")
	}
	if res.Lift == nil {
		lift := *res.WithSkill - *res.WithoutSkill
		res.Lift = &lift
	}
	return res, nil
}

// tierValue keeps a known tier status and turns anything else into unknown.
func tierValue(v string) string {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case StatusPassed, StatusFailed, StatusIncomplete, StatusSkipped:
		return v
	default:
		return ""
	}
}

// publicationValue keeps a known publication status and turns anything else into unknown.
func publicationValue(v string) string {
	if v = tierValue(v); v == StatusSkipped {
		return ""
	}
	return v
}

// cleanFindings drops blanks, shortens long entries and keeps the first five.
func cleanFindings(in []string) []string {
	var out []string
	for _, f := range in {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if r := []rune(f); len(r) > maxFindingLen {
			f = string(r[:maxFindingLen-1]) + "…"
		}
		out = append(out, f)
		if len(out) == maxFindings {
			break
		}
	}
	return out
}

// cleanSamples rounds the scores to two decimals, keeps at most maxSamples per
// arm and drops the whole block unless both arms have at least two scores: one
// score has no spread, so no interval can be drawn from it.
func cleanSamples(s *Samples) *Samples {
	if s == nil {
		return nil
	}
	clean := func(in []float64) []float64 {
		out := make([]float64, 0, min(len(in), maxSamples))
		for _, v := range in[:min(len(in), maxSamples)] {
			out = append(out, math.Round(v*100)/100)
		}
		return out
	}
	out := &Samples{WithSkill: clean(s.WithSkill), WithoutSkill: clean(s.WithoutSkill)}
	if len(out.WithSkill) < 2 || len(out.WithoutSkill) < 2 {
		return nil
	}
	return out
}
