package reportsite

import (
	"encoding/json"
	"fmt"
)

// dimensionOrder is the order of the six SkillEvaluator dimensions on the run page.
var dimensionOrder = []struct{ Key, Label string }{
	{"accuracy", "Accuracy"},
	{"goal_accuracy", "Goal accuracy"},
	{"behavior_check", "Behavior check"},
	{"skill_execution", "Skill execution"},
	{"skill_efficiency", "Skill efficiency"},
	{"security", "Security"},
}

// Dimension compares one SkillEvaluator dimension with and without the skill.
type Dimension struct {
	// Label is the display name.
	Label string
	// With and Without are scores from 0 to 1.
	With, Without float64
	// Delta is With minus Without.
	Delta float64
	// WithPct and WithoutPct are the scores as whole percentages, for bar widths.
	WithPct, WithoutPct int
	// Overall marks the summary row.
	Overall bool
}

// Activation is SkillEvaluator's observation of whether the agent used the skill.
type Activation struct {
	// Status is the reported status, for example "activated".
	Status string
	// Activated and Positive count trials in which the skill was used and trials where it could be.
	Activated, Positive int
}

// Result is the part of result.json the run page shows.
type Result struct {
	// Dimensions are the six dimensions in display order, then the overall row.
	Dimensions []Dimension
	// Activation is empty when result.json does not report it.
	Activation Activation
}

type rawResult struct {
	Agents map[string]struct {
		Lift  map[string]json.RawMessage `json:"lift"`
		Skill struct {
			Status    string `json:"status"`
			Positive  int    `json:"positive_trials"`
			Activated int    `json:"activated_trials"`
		} `json:"skill_activation"`
	} `json:"agents"`
}

type rawLift struct {
	With    *float64 `json:"with_skill"`
	Without *float64 `json:"without_skill"`
	Delta   *float64 `json:"delta"`
}

// parseResult extracts the dimension scores of one agent from result.json.
// An agent missing from the file falls back to the only agent in it.
func parseResult(data []byte, agent string) (Result, error) {
	var raw rawResult
	if err := json.Unmarshal(data, &raw); err != nil {
		return Result{}, fmt.Errorf("decoding result.json: %w", err)
	}
	a, ok := raw.Agents[agent]
	if !ok && len(raw.Agents) == 1 {
		for _, only := range raw.Agents {
			a, ok = only, true
		}
	}
	if !ok {
		return Result{}, fmt.Errorf("result.json has no scores for agent %q", agent)
	}
	var res Result
	res.Activation = Activation{Status: a.Skill.Status, Activated: a.Skill.Activated, Positive: a.Skill.Positive}
	add := func(key, label string, overall bool) error {
		raw, present := a.Lift[key]
		if !present {
			return nil // a dimension SkillEvaluator did not score is left out
		}
		var l rawLift
		if err := json.Unmarshal(raw, &l); err != nil {
			return fmt.Errorf("decoding lift of %s: %w", key, err)
		}
		if l.With == nil || l.Without == nil {
			return nil
		}
		d := Dimension{Label: label, With: *l.With, Without: *l.Without, Overall: overall}
		d.Delta = d.With - d.Without
		if l.Delta != nil {
			d.Delta = *l.Delta
		}
		d.WithPct, d.WithoutPct = pct(d.With), pct(d.Without)
		res.Dimensions = append(res.Dimensions, d)
		return nil
	}
	for _, d := range dimensionOrder {
		if err := add(d.Key, d.Label, false); err != nil {
			return Result{}, err
		}
	}
	if err := add("overall", "Overall", true); err != nil {
		return Result{}, err
	}
	if len(res.Dimensions) == 0 {
		return Result{}, fmt.Errorf("result.json has no dimension scores for agent %q", agent)
	}
	return res, nil
}

func pct(v float64) int {
	switch {
	case v <= 0:
		return 0
	case v >= 1:
		return 100
	default:
		return int(v*100 + 0.5)
	}
}
