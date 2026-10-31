package reportsite

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
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

// Limits on what the site takes from result.json for run-level suggestions.
const (
	maxSuggestions     = 20
	maxSuggestionBytes = 1000
)

// reportResult is what the site reads from a report's result.json for one agent.
type reportResult struct {
	// Dimensions are the six dimensions in display order, then the overall row;
	// empty when result.json has no lift block (Tier 3 left no scores).
	Dimensions []apiv1.Dimension
	// Activation is SkillEvaluator's skill activation observation, nil when absent.
	Activation *apiv1.Activation
	// ArmMeans are the per-metric means of each arm (with-skill, without-skill).
	ArmMeans map[string]map[string]float64
	// Suggestions are the run-level suggestions (tier3_feedback.suggestions_v2).
	Suggestions []string
}

type rawResult struct {
	Agents map[string]struct {
		Lift  map[string]json.RawMessage `json:"lift"`
		Skill *struct {
			Status    string `json:"status"`
			Positive  int    `json:"positive_trials"`
			Activated int    `json:"activated_trials"`
		} `json:"skill_activation"`
	} `json:"agents"`
	Feedback struct {
		Suggestions []json.RawMessage `json:"suggestions_v2"`
	} `json:"tier3_feedback"`
}

type rawLift struct {
	With    *float64 `json:"with_skill"`
	Without *float64 `json:"without_skill"`
	Delta   *float64 `json:"delta"`
}

// parseResult extracts the dimension scores, the arm means and the
// suggestions of one agent from result.json. An agent missing from the file
// falls back to the only agent in it.
func parseResult(data []byte, agent string) (reportResult, error) {
	var raw rawResult
	if err := json.Unmarshal(data, &raw); err != nil {
		return reportResult{}, fmt.Errorf("decoding result.json: %w", err)
	}
	a, ok := raw.Agents[agent]
	if !ok && len(raw.Agents) == 1 {
		for _, only := range raw.Agents {
			a, ok = only, true
		}
	}
	if !ok {
		return reportResult{}, fmt.Errorf("result.json has no scores for agent %q", agent)
	}
	res := reportResult{
		Dimensions: []apiv1.Dimension{},
		ArmMeans:   map[string]map[string]float64{apiv1.ArmWithSkill: {}, apiv1.ArmWithoutSkill: {}},
	}
	if s := a.Skill; s != nil && s.Status != "" {
		res.Activation = &apiv1.Activation{Status: s.Status, Activated: s.Activated, Positive: s.Positive}
	}
	for key, value := range a.Lift {
		var l rawLift
		if err := json.Unmarshal(value, &l); err != nil {
			return reportResult{}, fmt.Errorf("decoding lift of %s: %w", key, err)
		}
		if l.With != nil {
			res.ArmMeans[apiv1.ArmWithSkill][key] = *l.With
		}
		if l.Without != nil {
			res.ArmMeans[apiv1.ArmWithoutSkill][key] = *l.Without
		}
	}
	add := func(key, label string, overall bool) {
		var l rawLift
		if err := json.Unmarshal(a.Lift[key], &l); err != nil || l.With == nil || l.Without == nil {
			return // a dimension SkillEvaluator did not score is left out
		}
		d := apiv1.Dimension{Key: key, Label: label, WithSkill: *l.With, WithoutSkill: *l.Without, Overall: overall}
		d.Delta = round2(d.WithSkill - d.WithoutSkill)
		if l.Delta != nil {
			d.Delta = *l.Delta
		}
		res.Dimensions = append(res.Dimensions, d)
	}
	for _, d := range dimensionOrder {
		add(d.Key, d.Label, false)
	}
	if len(res.Dimensions) > 0 {
		add("overall", "Overall", true)
	}
	res.Suggestions = suggestions(raw.Feedback.Suggestions)
	return res, nil
}

// suggestions reads suggestions_v2 entries: plain strings, or objects with a
// message, text or title.
func suggestions(list []json.RawMessage) []string {
	var out []string
	for _, raw := range list {
		if len(out) == maxSuggestions {
			break
		}
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			var obj struct{ Message, Text, Title string }
			if json.Unmarshal(raw, &obj) != nil {
				continue
			}
			text = firstNonEmpty(obj.Message, obj.Text, obj.Title)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if len(text) > maxSuggestionBytes {
			text = strings.ToValidUTF8(text[:maxSuggestionBytes], "") + "..."
		}
		out = append(out, text)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
