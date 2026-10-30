package reportsite

import (
	"testing"

	"github.com/dirien/skill-ci/controller/internal/policy"
)

func required(model, lift string, phase string, regressed string) Run {
	return Run{Model: model, Required: true, Phase: phase, Lift: newScore(lift), Regressed: regressed}
}

func TestDecide(t *testing.T) {
	const gate = "claude-haiku-5-5 scores 0.69 with v1.2.0, stable v1.1.0 scored 0.95"
	tests := []struct {
		name     string
		runs     []Run
		stable   stableInfo
		kind     string
		headline string
		detail   string
	}{
		{"no required model", []Run{{Model: "opus", Phase: "Succeeded"}}, stableInfo{Known: true}, "neutral", "No decision", "No required model has been evaluated for this version."},
		{"still running", []Run{required("haiku", "+0.60", "Succeeded", ""), required("sonnet", "", "Running", "")}, stableInfo{Known: true}, "neutral", "Waiting", "1 of 2 required models have finished."},
		{"dry run", []Run{required("haiku", "", "Planned", "")}, stableInfo{Known: true}, "neutral", "Planned", "Dry run: the evals are recorded and no Job was created."},
		{"failed", []Run{required("haiku", "+0.60", "Succeeded", ""), required("sonnet", "", "Failed", "")}, stableInfo{Known: true}, "failed", "Eval failed", "An eval failed on sonnet, so the policy did not decide."},
		{"regression", []Run{required("haiku", "+0.33", "Succeeded", gate), required("sonnet", "+0.39", "Succeeded", "")}, stableInfo{Known: true, Holder: "v1.1.0"}, "regressed", "Held back", gate},
		{"on stable", []Run{required("haiku", "+0.58", "Succeeded", ""), required("sonnet", "+0.64", "Succeeded", "")}, stableInfo{Known: true, This: true, Holder: "v1.1.0"}, "pass", "On stable", "Lift is +0.58 or more on every required model (haiku lowest)."},
		{"below the threshold", []Run{required("haiku", "+0.03", "Succeeded", ""), required("sonnet", "+0.43", "Succeeded", "")}, stableInfo{Known: true}, "neutral", "Not promoted", "Stable needs +0.10 on every required model; below it: haiku +0.03."},
		{"exactly at the threshold", []Run{required("haiku", "+0.10", "Succeeded", "")}, stableInfo{Known: true, Holder: "v1.1.0"}, "neutral", "Meets threshold", "Lift is +0.10 or more on every required model; v1.1.0 holds stable."},
		{"another version holds stable", []Run{required("haiku", "+0.60", "Succeeded", "")}, stableInfo{Known: true, Holder: "v1.1.0"}, "neutral", "Meets threshold", "Lift is +0.10 or more on every required model; v1.1.0 holds stable."},
		{"nobody holds stable", []Run{required("haiku", "+0.60", "Succeeded", "")}, stableInfo{Known: true}, "neutral", "Meets threshold", "Lift is +0.10 or more on every required model; stable is not assigned yet."},
		{"registry unreadable", []Run{required("haiku", "+0.60", "Succeeded", "")}, stableInfo{}, "neutral", "Meets threshold", "Lift is +0.10 or more on every required model. Release channels are unavailable, so the tag is unknown."},
		{"stable without scores", []Run{{Model: "haiku", Required: true, Phase: "Succeeded"}}, stableInfo{Known: true, This: true}, "pass", "On stable", "This version holds the stable tag."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decide(tt.runs, tt.stable, policy.DefaultThresholds())
			want := Decision{Kind: tt.kind, Headline: tt.headline, Detail: tt.detail}
			if got != want {
				t.Errorf("decide = %+v\nwant     %+v", got, want)
			}
		})
	}
}
