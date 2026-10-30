package reportsite

import (
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/policy"
)

const (
	steady = "steady-notes" // lift beyond noise, then a noisy drop
	weak   = "weak-notes"   // lift of +0.12 whose interval crosses zero
	older  = "older-notes"  // a runner without trial scores
)

const noisyDrop = "claude-code/claude-haiku-5-5: 0.87 with v1.1.0 vs 0.95 with stable v1.0.0, difference -0.08, " +
	"95% CI [-0.18, +0.03]; not enough evidence to call it a regression"

func evidenceSpecs() []spec {
	push := v1alpha1.TriggerPush
	withCI := func(s spec, ci string) spec { s.n, s.ci = "9/9", ci; return s }
	specs := []spec{
		withCI(ok(steady, "v1.0.0", haiku, true, push, at(13, 0), "0.95", "0.36", "+0.59", "Helps"), "[+0.41, +0.73]"),
		withCI(ok(steady, "v1.0.0", sonnet, true, push, at(13, 0), "0.95", "0.36", "+0.59", "Helps"), "[+0.41, +0.73]"),
		withCI(ok(steady, "v1.1.0", haiku, true, push, at(13, 30), "0.87", "0.36", "+0.51", "Inconclusive"), "[+0.30, +0.72]"),
		withCI(ok(steady, "v1.1.0", sonnet, true, push, at(13, 30), "0.95", "0.36", "+0.59", "Helps"), "[+0.41, +0.73]"),
		withCI(ok(weak, "v1.0.0", haiku, true, push, at(14, 0), "0.47", "0.35", "+0.12", "Neutral"), "[-0.05, +0.29]"),
		ok(older, "v1.0.0", haiku, true, push, at(14, 10), "0.95", "0.36", "+0.59", "Helps"),
	}
	specs[2].inconclusive = noisyDrop
	return specs
}

func evidenceHarbor() *harbor.Fake {
	return harbor.NewFake(map[string][]harbor.Artifact{
		steady: {
			{Digest: digestOf(steady, "v1.0.0"), Tags: []string{"v1.0.0", "stable"}},
			{Digest: digestOf(steady, "v1.1.0"), Tags: []string{"v1.1.0", "beta"}},
		},
		weak:  {{Digest: digestOf(weak, "v1.0.0"), Tags: []string{"v1.0.0", "beta"}}},
		older: {{Digest: digestOf(older, "v1.0.0"), Tags: []string{"v1.0.0", "beta", "stable"}}},
	})
}

func TestEvidenceOnThePages(t *testing.T) {
	h := newHarnessWith(t, evidenceSpecs(), evidenceHarbor())
	tests := []struct {
		name, path string
		want       []string
		absent     []string
	}{
		{name: "runs table shows the interval and n", path: "/",
			want: []string{"95% CI +0.41 to +0.73, n=9/9", "95% CI -0.05 to +0.29, n=9/9", ">Inconclusive<", "<strong>Inconclusive</strong>",
				"the 95% CI is the range the true lift most likely lies in"}},
		{name: "skill page shows it in each cell", path: "/skills/" + steady,
			want: []string{"95% CI +0.41 to +0.73, n=9/9", ">Inconclusive<"}},
		{name: "skill page explains Inconclusive in plain words", path: "/skills/" + steady,
			want: []string{"Not proven", "difference -0.08", "95% CI [-0.18, +0.03]", "The stable version stays; more trials would show whether the drop is real."}},
		{name: "skill page explains an unproven lift", path: "/skills/" + weak,
			want: []string{"Not promoted", "The lift reaches +0.10 but is not beyond noise", "claude-haiku-5-5 +0.12 (95% CI -0.05 to +0.29, n=9/9)", "whole interval above zero"}},
		{name: "run page", path: runPath(steady, "v1.0.0", haiku),
			want: []string{"+0.59", "95% CI +0.41 to +0.73, n=9/9"}},
		{name: "inconclusive run page", path: runPath(steady, "v1.1.0", haiku),
			want: []string{"Inconclusive", "not enough evidence to call it a regression", "The stable version stays"}},
		{name: "older runner shows no interval", path: runPath(older, "v1.0.0", haiku),
			want: []string{"+0.59"}, absent: []string{"CI +", "n=9"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.get(tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			body := html.UnescapeString(rec.Body.String())
			for _, w := range tt.want {
				if !strings.Contains(body, w) {
					t.Errorf("body lacks %q", w)
				}
			}
			for _, a := range tt.absent {
				if strings.Contains(body, a) {
					t.Errorf("body contains %q", a)
				}
			}
		})
	}
}

func TestInconclusiveIsNeutral(t *testing.T) {
	got := Run{Verdict: "Inconclusive"}.VerdictStatus()
	if got != (Status{"neutral", "Inconclusive"}) {
		t.Errorf("VerdictStatus() = %+v, want neutral Inconclusive", got)
	}
}

func TestRunEvidence(t *testing.T) {
	tests := []struct {
		name         string
		run          Run
		evidence     string
		liftEvidence string
	}{
		{"with samples", Run{Lift: newScore("+0.59"), CI: "[+0.41, +0.73]", N: "9/9", Confidence: "95%"}, "95% CI +0.41 to +0.73, n=9/9", "+0.59 (95% CI +0.41 to +0.73, n=9/9)"},
		{"other level", Run{Lift: newScore("+0.59"), CI: "[+0.30, +0.80]", N: "6/6", Confidence: "99%"}, "99% CI +0.30 to +0.80, n=6/6", "+0.59 (99% CI +0.30 to +0.80, n=6/6)"},
		{"without samples", Run{Lift: newScore("+0.59"), Confidence: "95%"}, "", "+0.59"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.run.Evidence(); got != tt.evidence {
				t.Errorf("Evidence() = %q, want %q", got, tt.evidence)
			}
			if got := tt.run.LiftEvidence(); got != tt.liftEvidence {
				t.Errorf("LiftEvidence() = %q, want %q", got, tt.liftEvidence)
			}
		})
	}
}

func TestParseCI(t *testing.T) {
	tests := []struct {
		in     string
		lo, hi float64
		ok     bool
	}{
		{"[+0.41, +0.73]", 0.41, 0.73, true},
		{"[-0.05, +0.29]", -0.05, 0.29, true},
		{"", 0, 0, false},
		{"[oops]", 0, 0, false},
	}
	for _, tt := range tests {
		lo, hi, ok := parseCI(tt.in)
		if ok != tt.ok || (ok && (lo != tt.lo || hi != tt.hi)) {
			t.Errorf("parseCI(%q) = %v, %v, %v", tt.in, lo, hi, ok)
		}
	}
}

func TestDecideEvidence(t *testing.T) {
	th := policy.DefaultThresholds()
	proven := Run{Model: "haiku", Required: true, Phase: "Succeeded", Lift: newScore("+0.59"), CI: "[+0.41, +0.73]", CILower: 0.41, N: "9/9", Confidence: "95%"}
	noisy := Run{Model: "sonnet", Required: true, Phase: "Succeeded", Lift: newScore("+0.12"), CI: "[-0.05, +0.29]", CILower: -0.05, N: "9/9", Confidence: "95%"}
	got := decide([]Run{proven, noisy}, stableInfo{Known: true, Holder: "v1.0.0"}, th)
	if got.Kind != "neutral" || got.Headline != "Not promoted" || !strings.Contains(got.Detail, "sonnet +0.12 (95% CI -0.05 to +0.29, n=9/9)") {
		t.Errorf("decide(unproven) = %+v", got)
	}
	got = decide([]Run{proven}, stableInfo{Known: true, Holder: "v1.0.0"}, th)
	if got.Headline != "Meets threshold" {
		t.Errorf("decide(proven) = %+v, want Meets threshold", got)
	}
}
