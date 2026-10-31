package reportsite

import (
	"testing"

	"github.com/dirien/skill-ci/controller/internal/policy"
)

func required(model, lift string, phase string, regressed string) Run {
	return Run{Model: model, Required: true, Phase: phase, Lift: newScore(lift), Regressed: regressed}
}

func rejected(model string, required bool, tier1, tier2 string, findings ...string) Run {
	return Run{Model: model, Required: required, Phase: "Succeeded", Verdict: "Rejected", Tier1: tier1, Tier2: tier2, Tier3: "skipped", Findings: findings}
}

func withCI(r Run, ci string, lower float64) Run {
	r.CI, r.CILower, r.N, r.Confidence = ci, lower, "9/9", "95%"
	return r
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
		{"rejected by tier 1", []Run{rejected("haiku", true, "failed", "passed", "tier1/security: hardcoded credential"), required("sonnet", "+0.60", "Succeeded", "")}, stableInfo{Known: true},
			"failed", "Rejected", "Rejected by Tier 1: tier1/security: hardcoded credential"},
		{"rejected by tier 2 without findings", []Run{rejected("haiku", true, "passed", "failed")}, stableInfo{Known: true}, "failed", "Rejected", "Rejected by Tier 2"},
		{"rejected while it holds stable", []Run{rejected("haiku", true, "failed", "passed", "tier1/pii: email")}, stableInfo{Known: true, This: true, Holder: "v1.0.0"},
			"failed", "Rejected", "Rejected by Tier 1: tier1/pii: email"},
		{"an optional model rejecting does not decide", []Run{required("haiku", "+0.60", "Succeeded", ""), rejected("opus", false, "failed", "passed", "x")}, stableInfo{Known: true},
			"neutral", "Meets threshold", "Lift is +0.10 or more on every required model; stable is not assigned yet."},
		{"a running required model still waits", []Run{rejected("haiku", true, "failed", "passed", "x"), required("sonnet", "", "Running", "")}, stableInfo{Known: true},
			"neutral", "Waiting", "1 of 2 required models have finished."},
		{"lift not beyond noise", []Run{withCI(required("haiku", "+0.59", "Succeeded", ""), "[+0.41, +0.73]", 0.41), withCI(required("sonnet", "+0.12", "Succeeded", ""), "[-0.05, +0.29]", -0.05)},
			stableInfo{Known: true, Holder: "v1.0.0"}, "neutral", "Not promoted",
			"The lift reaches +0.10 but is not beyond noise, so the skill may not help: sonnet +0.12 (95% CI -0.05 to +0.29, n=9/9). Stable needs the whole interval above zero."},
		{"lift beyond noise", []Run{withCI(required("haiku", "+0.59", "Succeeded", ""), "[+0.41, +0.73]", 0.41)}, stableInfo{Known: true, Holder: "v1.0.0"},
			"neutral", "Meets threshold", "Lift is +0.10 or more on every required model; v1.0.0 holds stable."},
		{"not proven", []Run{{Model: "haiku", Required: true, Phase: "Succeeded", Lift: newScore("+0.51"), Inconclusive: "haiku: 0.87 vs 0.95, difference -0.08"}}, stableInfo{Known: true, Holder: "v1.0.0"},
			"neutral", "Not proven", "haiku: 0.87 vs 0.95, difference -0.08. The stable version stays; more trials would show whether the drop is real."},
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
