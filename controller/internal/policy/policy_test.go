package policy

import (
	"math"
	"reflect"
	"testing"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/stats"
)

// Trial scores of 3 cases x 3 attempts. Means: 0.95, 0.70, 0.87 and 0.36.
var (
	stableTight = []float64{0.95, 0.95, 0.94, 0.96, 0.95, 0.95, 0.94, 0.96, 0.95}
	stableNoisy = []float64{1.0, 0.95, 0.9, 0.95, 1.0, 0.85, 0.95, 1.0, 0.95}
	dropTight   = []float64{0.70, 0.71, 0.69, 0.70, 0.70, 0.71, 0.69, 0.70, 0.70}
	dropNoisy   = []float64{0.95, 0.55, 0.9, 0.85, 1.0, 0.8, 0.9, 0.95, 0.95}
	baseline    = []float64{0.36, 0.38, 0.35, 0.30, 0.40, 0.35, 0.36, 0.37, 0.38}
	// weakLift beats the baseline by about 0.12 on average, with a lot of spread.
	weakWith    = []float64{0.9, 0.2, 0.7, 0.3, 0.8, 0.1, 0.6, 0.4, 0.5}
	weakWithout = []float64{0.3, 0.6, 0.2, 0.5, 0.1, 0.4, 0.3, 0.2, 0.4}
)

func ciOf(a, b []float64) *stats.Interval { return DefaultThresholds().CI(a, b) }

func TestVerdict(t *testing.T) {
	tests := []struct {
		name          string
		lift, without float64
		want          v1alpha1.Verdict
		ci            *stats.Interval // nil: a run without trial scores
	}{
		{"clear help", 0.59, 0.36, v1alpha1.VerdictHelps, nil},
		{"exactly helps band", 0.05, 0.30, v1alpha1.VerdictHelps, nil},
		{"just below helps", 0.04, 0.30, v1alpha1.VerdictNeutral, nil},
		{"float noise at helps band", 0.95 - 0.90, 0.30, v1alpha1.VerdictHelps, nil},
		{"exactly hurts band", -0.10, 0.30, v1alpha1.VerdictHurts, nil},
		{"just above hurts", -0.09, 0.30, v1alpha1.VerdictNeutral, nil},
		{"neutral baseline below obsolete", 0.0, 0.79, v1alpha1.VerdictNeutral, nil},
		{"neutral baseline obsolete", 0.0, 0.80, v1alpha1.VerdictObsolete, nil},
		{"helps beats obsolete", 0.06, 0.90, v1alpha1.VerdictHelps, nil},
		{"hurts beats obsolete", -0.20, 0.90, v1alpha1.VerdictHurts, nil},
		{"helps with an interval above zero", 0.59, 0.36, v1alpha1.VerdictHelps, &stats.Interval{Lower: 0.41, Upper: 0.73}},
		{"big lift but interval crosses zero is neutral", 0.30, 0.36, v1alpha1.VerdictNeutral, &stats.Interval{Lower: -0.05, Upper: 0.65}},
		{"interval crossing zero keeps the obsolete rule", 0.30, 0.85, v1alpha1.VerdictObsolete, &stats.Interval{Lower: -0.05, Upper: 0.65}},
		{"hurts with an interval below zero", -0.20, 0.50, v1alpha1.VerdictHurts, &stats.Interval{Lower: -0.35, Upper: -0.05}},
		{"big drop but interval crosses zero is neutral", -0.20, 0.50, v1alpha1.VerdictNeutral, &stats.Interval{Lower: -0.45, Upper: 0.05}},
	}
	th := DefaultThresholds()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := th.Verdict(tt.lift, tt.without, tt.ci); got != tt.want {
				t.Errorf("Verdict(%v, %v) = %s, want %s", tt.lift, tt.without, got, tt.want)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	ok := func(lift float64, v v1alpha1.Verdict) Result {
		return Result{Required: true, Succeeded: true, Lift: lift, Verdict: v}
	}
	withCI := func(lift float64, ci *stats.Interval) Result {
		return Result{Required: true, Succeeded: true, Lift: lift, LiftCI: ci}
	}
	rejected := func(required bool) Result {
		return Result{Required: required, Succeeded: true, Rejected: true, Verdict: v1alpha1.VerdictRejected}
	}
	tests := []struct {
		name    string
		results []Result
		want    Outcome
	}{
		{"no results", nil, Outcome{}},
		{"only optional models", []Result{{Succeeded: true, Lift: 0.9}}, Outcome{}},
		{"required still running", []Result{ok(0.6, "Helps"), {Required: true}}, Outcome{}},
		{"promote on all required", []Result{ok(0.59, "Helps"), ok(0.10, "Helps")},
			Outcome{Complete: true, Promote: true}},
		{"one below promote", []Result{ok(0.59, "Helps"), ok(0.09, "Helps")}, Outcome{Complete: true}},
		{"demote on any required", []Result{ok(0.59, "Helps"), ok(-0.10, "Hurts")},
			Outcome{Complete: true, Demote: true}},
		{"optional hurt is ignored", []Result{ok(0.30, "Helps"), {Succeeded: true, Lift: -0.5}},
			Outcome{Complete: true, Promote: true}},
		{"obsolete on all", []Result{ok(0.0, "Obsolete"), ok(0.01, "Obsolete")},
			Outcome{Complete: true, Obsolete: true}},
		{"obsolete needs all", []Result{ok(0.0, "Obsolete"), ok(0.0, "Neutral")}, Outcome{Complete: true}},
		{"rejected required model is finished and blocks promotion", []Result{rejected(true)},
			Outcome{Complete: true, Rejected: true}},
		{"rejected beats a strong lift on the other model", []Result{ok(0.60, "Helps"), rejected(true)},
			Outcome{Complete: true, Rejected: true}},
		{"rejected does not hide a demote", []Result{ok(-0.2, "Hurts"), rejected(true)},
			Outcome{Complete: true, Rejected: true, Demote: true}},
		{"rejected is not obsolete", []Result{ok(0.0, "Obsolete"), rejected(true)},
			Outcome{Complete: true, Rejected: true}},
		{"rejected optional model is ignored", []Result{ok(0.30, "Helps"), rejected(false)},
			Outcome{Complete: true, Promote: true}},
		{"rejected waits for the other required model", []Result{rejected(true), {Required: true}}, Outcome{}},
		{"promote when the interval clears zero", []Result{withCI(0.59, ciOf(dropTight, baseline))},
			Outcome{Complete: true, Promote: true}},
		{"lift is high but the interval crosses zero", []Result{withCI(0.12, ciOf(weakWith, weakWithout))},
			Outcome{Complete: true, Unproven: true}},
		{"one unproven model blocks promotion", []Result{withCI(0.59, ciOf(dropTight, baseline)), withCI(0.12, ciOf(weakWith, weakWithout))},
			Outcome{Complete: true, Unproven: true}},
		{"lift below promote with a clear interval is not unproven", []Result{withCI(0.08, &stats.Interval{Lower: 0.01, Upper: 0.15})},
			Outcome{Complete: true}},
		{"demote needs the interval below zero", []Result{withCI(-0.15, &stats.Interval{Lower: -0.4, Upper: 0.1})},
			Outcome{Complete: true}},
		{"demote with an interval below zero", []Result{withCI(-0.15, &stats.Interval{Lower: -0.3, Upper: -0.01})},
			Outcome{Complete: true, Demote: true}},
		{"no trial scores keeps the fixed rules", []Result{ok(0.12, "Helps")}, Outcome{Complete: true, Promote: true}},
	}
	th := DefaultThresholds()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := th.Evaluate(tt.results); got != tt.want {
				t.Errorf("Evaluate() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPlan(t *testing.T) {
	arts := func(old, cur []string) []harbor.Artifact {
		return []harbor.Artifact{{Digest: "sha256:old", Tags: old}, {Digest: "sha256:cur", Tags: cur}}
	}
	tests := []struct {
		name   string
		out    Outcome
		newest bool
		arts   []harbor.Artifact
		want   []Op
	}{
		{"newest gets beta, old loses it", Outcome{Complete: true}, true,
			arts([]string{"v1.0.0", "beta"}, []string{"v1.1.0"}),
			[]Op{{Digest: "sha256:old", Tag: "beta"}, {Add: true, Digest: "sha256:cur", Tag: "beta"}}},
		{"beta already in place", Outcome{Complete: true}, true,
			arts([]string{"v1.0.0"}, []string{"v1.1.0", "beta"}), nil},
		{"not newest leaves beta alone", Outcome{Complete: true}, false,
			arts([]string{"v1.0.0", "beta"}, []string{"v1.1.0"}), nil},
		{"promote moves stable", Outcome{Complete: true, Promote: true}, false,
			arts([]string{"v1.0.0", "stable"}, []string{"v1.1.0"}),
			[]Op{{Digest: "sha256:old", Tag: "stable"}, {Add: true, Digest: "sha256:cur", Tag: "stable"}}},
		{"promote and beta together", Outcome{Complete: true, Promote: true}, true,
			arts([]string{"v1.0.0", "stable", "beta"}, []string{"v1.1.0"}),
			[]Op{
				{Digest: "sha256:old", Tag: "beta"}, {Add: true, Digest: "sha256:cur", Tag: "beta"},
				{Digest: "sha256:old", Tag: "stable"}, {Add: true, Digest: "sha256:cur", Tag: "stable"},
			}},
		{"demote removes stable from digest", Outcome{Complete: true, Demote: true}, false,
			arts([]string{"v1.0.0"}, []string{"v1.1.0", "stable"}),
			[]Op{{Digest: "sha256:cur", Tag: "stable"}}},
		{"demote without stable is a no-op", Outcome{Complete: true, Demote: true}, false,
			arts([]string{"v1.0.0", "stable"}, []string{"v1.1.0"}), nil},
		{"rejected never gets beta", Outcome{Complete: true, Rejected: true}, true,
			arts([]string{"v1.0.0", "beta"}, []string{"v1.1.0"}), nil},
		{"rejected never gets stable", Outcome{Complete: true, Rejected: true, Promote: true}, false,
			arts([]string{"v1.0.0", "stable"}, []string{"v1.1.0"}), nil},
		{"rejected loses stable", Outcome{Complete: true, Rejected: true}, false,
			arts([]string{"v1.0.0", "beta"}, []string{"v1.1.0", "stable"}),
			[]Op{{Digest: "sha256:cur", Tag: "stable"}}},
		{"obsolete never touches tags", Outcome{Complete: true, Obsolete: true}, false,
			arts([]string{"v1.0.0"}, []string{"v1.1.0", "stable"}), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Plan(tt.out, "sha256:cur", tt.newest, tt.arts); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Plan() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRegressions(t *testing.T) {
	const (
		haiku  = "claude-code/claude-haiku-5-5"
		sonnet = "claude-code/claude-sonnet-5-5"
	)
	req := func(model string, with float64) Score { return Score{Model: model, Required: true, With: with} }
	trials := func(model string, samples []float64) Score {
		return Score{Model: model, Required: true, With: stats.Mean(samples), Samples: samples}
	}
	tests := []struct {
		name              string
		candidate, stable []Score
		want              []Regression
	}{
		// Without trial scores: today's fixed rule.
		{"equal scores", []Score{req(haiku, 0.95)}, []Score{req(haiku, 0.95)}, nil},
		{"small drop within tolerance", []Score{req(haiku, 0.91)}, []Score{req(haiku, 0.95)}, nil},
		{"drop of exactly the tolerance", []Score{req(haiku, 0.90)}, []Score{req(haiku, 0.95)}, nil},
		{"drop beyond tolerance", []Score{req(haiku, 0.69)}, []Score{req(haiku, 0.95)},
			[]Regression{{Model: haiku, Candidate: 0.69, Stable: 0.95}}},
		{"one of two required models drops", []Score{req(haiku, 0.69), req(sonnet, 0.96)},
			[]Score{req(haiku, 0.95), req(sonnet, 0.95)},
			[]Regression{{Model: haiku, Candidate: 0.69, Stable: 0.95}}},
		{"optional model drop is ignored", []Score{{Model: haiku, With: 0.2}}, []Score{req(haiku, 0.95)}, nil},
		{"model without a stable score is skipped", []Score{req(sonnet, 0.1)}, []Score{req(haiku, 0.95)}, nil},
		{"no stable scores", []Score{req(haiku, 0.69)}, nil, nil},
		{"candidate is better", []Score{req(haiku, 0.99)}, []Score{req(haiku, 0.80)}, nil},
		{"trial scores on one side only fall back to the fixed rule",
			[]Score{trials(haiku, dropNoisy)}, []Score{req(haiku, 0.95)}, []Regression{{Model: haiku, Candidate: 0.95 - 0.0778, Stable: 0.95}}},
		// With trial scores on both sides: evidence.
		{"clear regression: tight samples", []Score{trials(haiku, dropTight)}, []Score{trials(haiku, stableTight)},
			[]Regression{{Model: haiku, Candidate: 0.70, Stable: 0.95, CI: ciOf(dropTight, stableTight)}}},
		{"noisy drop: the interval includes zero", []Score{trials(haiku, dropNoisy)}, []Score{trials(haiku, stableNoisy)},
			[]Regression{{Model: haiku, Candidate: 0.8722, Stable: 0.95, CI: ciOf(dropNoisy, stableNoisy), Inconclusive: true}}},
		{"equal samples pass", []Score{trials(haiku, stableTight)}, []Score{trials(haiku, stableTight)}, nil},
		{"better samples pass", []Score{trials(haiku, stableTight)}, []Score{trials(haiku, dropTight)}, nil},
		{"drop within tolerance passes even when significant",
			[]Score{trials(haiku, []float64{0.93, 0.93, 0.92, 0.94})}, []Score{trials(haiku, []float64{0.97, 0.97, 0.96, 0.98})}, nil},
		{"only the noisy model is inconclusive", []Score{trials(haiku, dropTight), trials(sonnet, dropNoisy)},
			[]Score{trials(haiku, stableTight), trials(sonnet, stableNoisy)},
			[]Regression{
				{Model: haiku, Candidate: 0.70, Stable: 0.95, CI: ciOf(dropTight, stableTight)},
				{Model: sonnet, Candidate: 0.8722, Stable: 0.95, CI: ciOf(dropNoisy, stableNoisy), Inconclusive: true},
			}},
	}
	th := DefaultThresholds()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := th.Regressions(tt.candidate, tt.stable)
			if len(got) != len(tt.want) {
				t.Fatalf("Regressions() = %+v, want %+v", got, tt.want)
			}
			for i, w := range tt.want {
				g := got[i]
				if g.Model != w.Model || g.Inconclusive != w.Inconclusive || math.Abs(g.Candidate-w.Candidate) > 1e-3 ||
					math.Abs(g.Stable-w.Stable) > 1e-3 || !reflect.DeepEqual(g.CI, w.CI) {
					t.Errorf("Regressions()[%d] = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

// TestRegressionNumbers pins the worked examples of CONTRACT.md.
func TestRegressionNumbers(t *testing.T) {
	th := DefaultThresholds()
	mk := func(s []float64) []Score { return []Score{{Model: "m", Required: true, Samples: s}} }
	clear := th.Regressions(mk(dropTight), mk(stableTight))[0]
	if clear.Inconclusive || !within(clear.CI.Lower, -0.2571, 1e-3) || !within(clear.CI.Upper, -0.2429, 1e-3) {
		t.Errorf("clear regression = %+v, CI %+v", clear, clear.CI)
	}
	noisy := th.Regressions(mk(dropNoisy), mk(stableNoisy))[0]
	if !noisy.Inconclusive || !within(noisy.CI.Lower, -0.1844, 1e-3) || !within(noisy.CI.Upper, 0.0288, 1e-3) {
		t.Errorf("noisy drop = %+v, CI %+v", noisy, noisy.CI)
	}
}

func within(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

func TestConfirmed(t *testing.T) {
	regs := []Regression{{Model: "a"}, {Model: "b", Inconclusive: true}}
	if got := Confirmed(regs); len(got) != 1 || got[0].Model != "a" {
		t.Errorf("Confirmed() = %+v, want only a", got)
	}
	if len(regs) != 2 {
		t.Error("Confirmed() changed its input")
	}
}

func TestBlock(t *testing.T) {
	promote := Outcome{Complete: true, Promote: true}
	if got := promote.Block(nil); got != promote {
		t.Errorf("Block(nil) = %+v, want it unchanged", got)
	}
	want := Outcome{Complete: true, Regressed: true}
	if got := promote.Block([]Regression{{Model: "m"}}); got != want {
		t.Errorf("Block(regression) = %+v, want %+v", got, want)
	}
	if got, wantInc := promote.Block([]Regression{{Model: "m", Inconclusive: true}}), (Outcome{Complete: true, Inconclusive: true}); got != wantInc {
		t.Errorf("Block(inconclusive) = %+v, want %+v", got, wantInc)
	}
	if got, wantBoth := promote.Block([]Regression{{Model: "a"}, {Model: "b", Inconclusive: true}}), (Outcome{Complete: true, Regressed: true, Inconclusive: true}); got != wantBoth {
		t.Errorf("Block(both) = %+v, want %+v", got, wantBoth)
	}
	ops := Plan(want, "sha256:cur", false, []harbor.Artifact{{Digest: "sha256:old", Tags: []string{"stable"}}, {Digest: "sha256:cur"}})
	if len(ops) != 0 {
		t.Errorf("Plan() of a regressed digest = %+v, want no changes", ops)
	}
}

func TestPercent(t *testing.T) {
	for conf, want := range map[float64]string{0.95: "95%", 0.99: "99%", 0.9: "90%", 0.975: "97.5%", 0.8: "80%"} {
		th := Thresholds{Confidence: conf}
		if got := th.Percent(); got != want {
			t.Errorf("Percent(%v) = %q, want %q", conf, got, want)
		}
	}
}
