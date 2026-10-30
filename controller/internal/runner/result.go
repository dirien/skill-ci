package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Result is the JSON the runner writes to /dev/termination-log.
type Result struct {
	// WithSkill is the score with the skill installed.
	WithSkill *float64 `json:"withSkill"`
	// WithoutSkill is the baseline score.
	WithoutSkill *float64 `json:"withoutSkill"`
	// Lift is WithSkill minus WithoutSkill.
	Lift *float64 `json:"lift"`
	// Error is set when the runner produced no scores.
	Error string `json:"error"`
}

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
	if res.Error != "" {
		return res, nil
	}
	if res.WithSkill == nil || res.WithoutSkill == nil {
		return Result{}, errors.New("termination message has no withSkill and withoutSkill scores")
	}
	if res.Lift == nil {
		lift := *res.WithSkill - *res.WithoutSkill
		res.Lift = &lift
	}
	return res, nil
}
