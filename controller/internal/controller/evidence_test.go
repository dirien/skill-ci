package controller

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/client-go/tools/events"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/metrics"
	"github.com/dirien/skill-ci/controller/internal/models"
	"github.com/dirien/skill-ci/controller/internal/policy"
)

// Trial scores of 3 cases x 3 attempts (see internal/policy tests).
var (
	stableTight = []float64{0.95, 0.95, 0.94, 0.96, 0.95, 0.95, 0.94, 0.96, 0.95}
	stableNoisy = []float64{1.0, 0.95, 0.9, 0.95, 1.0, 0.85, 0.95, 1.0, 0.95}
	dropTight   = []float64{0.70, 0.71, 0.69, 0.70, 0.70, 0.71, 0.69, 0.70, 0.70}
	dropNoisy   = []float64{0.95, 0.55, 0.9, 0.85, 1.0, 0.8, 0.9, 0.95, 0.95}
	better      = []float64{0.99, 0.98, 1.0, 0.99, 0.99, 0.98, 1.0, 0.99, 0.99}
	baseline    = []float64{0.36, 0.38, 0.35, 0.30, 0.40, 0.35, 0.36, 0.37, 0.38}
	weakWith    = []float64{0.9, 0.2, 0.7, 0.3, 0.8, 0.1, 0.6, 0.4, 0.5}
	weakWithout = []float64{0.3, 0.6, 0.2, 0.5, 0.1, 0.4, 0.3, 0.2, 0.4}
)

// trialMessage renders a runner termination message with trial scores.
func trialMessage(with, without float64, withS, withoutS []float64) string {
	b, err := json.Marshal(map[string]any{
		"withSkill": with, "withoutSkill": without, "lift": with - without,
		"samples": map[string]any{"withSkill": withS, "withoutSkill": withoutS},
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestStatisticsAreStored(t *testing.T) {
	e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
		map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}})
	se := e.run(e.create("acme", "v1.0.0", digestA, haiku), trialMessage(0.95, 0.36, stableTight, baseline))
	st := se.Status
	if st.Samples == nil || len(st.Samples.WithSkill) != 9 || st.Samples.WithSkill[2] != "0.94" || len(st.Samples.WithoutSkill) != 9 {
		t.Errorf("samples = %+v", st.Samples)
	}
	if st.N != "9/9" || st.WithSkillStdErr != "0.00" || st.WithoutSkillStdErr != "0.01" || st.LiftCI != "[+0.57, +0.61]" {
		t.Errorf("n %q stderr %q/%q liftCI %q", st.N, st.WithSkillStdErr, st.WithoutSkillStdErr, st.LiftCI)
	}
	if st.Verdict != v1alpha1.VerdictHelps {
		t.Errorf("verdict = %s", st.Verdict)
	}

	// A runner without trial scores leaves the statistics empty.
	old := e.run(e.create("acme", "v1.0.0", digestA, sonnet), scoreWith(0.95, 0.36)).Status
	if old.Samples != nil || old.N != "" || old.LiftCI != "" || old.WithSkillStdErr != "" {
		t.Errorf("run without samples has statistics: %+v", old)
	}
}

func TestConfidenceChangeRefreshesTheInterval(t *testing.T) {
	cfg := Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}
	e := newEnv(t, cfg, 200, map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}})
	se := e.run(e.create("acme", "v1.0.0", digestA, haiku), trialMessage(0.9, 0.36, weakWith, baseline))
	narrow := se.Status.LiftCI
	e.r.Config.Thresholds.Confidence = 0.99
	e.reconcile(se)
	if wide := e.get(se).Status.LiftCI; wide == narrow || wide == "" {
		t.Errorf("liftCI at 99%% = %q, want it to differ from %q", wide, narrow)
	}
}

// TestStatisticalGate runs the gate end to end: v1.0.0 is stable with scores 0.95;
// v1.2.0 arrives with the given trial scores on both required models.
func TestStatisticalGate(t *testing.T) {
	tests := []struct {
		name         string
		stable       []float64 // trial scores of the stable version; nil: a runner without samples
		candidate    []float64
		candWith     float64
		wantStable   string
		wantVerdict  v1alpha1.Verdict
		wantCond     string // condition type expected on the candidate runs, "" for none
		wantInMsg    []string
		wantEventTyp string
	}{
		{name: "clear regression: tight samples", stable: stableTight, candidate: dropTight, candWith: 0.70,
			wantStable: digestA, wantVerdict: v1alpha1.VerdictRegressed, wantCond: ConditionRegressed,
			wantInMsg: []string{"scores 0.70 with v1.2.0", "stable v1.0.0 scored 0.95", "difference -0.25", "95% CI [-0.26, -0.24]"}, wantEventTyp: "Warning"},
		{name: "noisy drop: inconclusive", stable: stableNoisy, candidate: dropNoisy, candWith: 0.87,
			wantStable: digestA, wantVerdict: v1alpha1.VerdictInconclusive, wantCond: ConditionInconclusive,
			wantInMsg: []string{"0.87 with v1.2.0 vs 0.95 with stable v1.0.0", "difference -0.08", "95% CI [-0.18, +0.03]", "not enough evidence to call it a regression"}, wantEventTyp: "Normal"},
		{name: "equal is promoted", stable: stableTight, candidate: stableTight, candWith: 0.95,
			wantStable: digestB, wantVerdict: v1alpha1.VerdictHelps},
		{name: "better is promoted", stable: stableTight, candidate: better, candWith: 0.99,
			wantStable: digestB, wantVerdict: v1alpha1.VerdictHelps},
		{name: "stable without samples: the fixed rule judges the drop", candidate: dropNoisy, candWith: 0.87,
			wantStable: digestA, wantVerdict: v1alpha1.VerdictRegressed, wantCond: ConditionRegressed,
			wantInMsg: []string{"scores 0.87 with v1.2.0", "fixed rule, no trial scores"}, wantEventTyp: "Warning"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
				map[string][]harbor.Artifact{"acme": {
					{Digest: digestA, Tags: []string{"v1.0.0", "beta", "stable"}},
					{Digest: digestB, Tags: []string{"v1.2.0"}},
				}})
			for _, m := range []models.Model{haiku, sonnet} {
				msg := scoreWith(0.95, 0.36)
				if tt.stable != nil {
					msg = trialMessage(0.95, 0.36, tt.stable, baseline)
				}
				e.run(e.create("acme", "v1.0.0", digestA, m), msg)
			}
			var cand []*v1alpha1.SkillEval
			for _, m := range []models.Model{haiku, sonnet} {
				se := e.create("acme", "v1.2.0", digestB, m)
				e.run(se, trialMessage(tt.candWith, 0.36, tt.candidate, baseline))
				cand = append(cand, se)
			}
			if got := tags(t, e.harbor, "acme", tt.wantStable); !slices.Contains(got, "stable") {
				t.Errorf("tags of %s = %v, want stable", tt.wantStable[:10], got)
			}
			if got := tags(t, e.harbor, "acme", digestB); !slices.Contains(got, "beta") {
				t.Errorf("tags of v1.2.0 = %v, want beta (a held-back version stays eligible)", got)
			}
			for _, se := range cand {
				got := e.get(se)
				if got.Status.Verdict != tt.wantVerdict {
					t.Errorf("%s verdict = %s, want %s", se.Name, got.Status.Verdict, tt.wantVerdict)
				}
				for _, typ := range []string{ConditionRegressed, ConditionInconclusive} {
					cond := findCondition(got.Status.Conditions, typ)
					if (cond != nil) != (typ == tt.wantCond) {
						t.Fatalf("%s condition %s = %+v, want present: %v", se.Name, typ, cond, typ == tt.wantCond)
					}
					if cond == nil {
						continue
					}
					for _, want := range tt.wantInMsg {
						if !strings.Contains(cond.Message, want) {
							t.Errorf("%s message %q lacks %q", typ, cond.Message, want)
						}
					}
				}
			}
			// Converged: reconciling the finished runs again writes nothing.
			before := map[string]string{}
			for _, se := range cand {
				before[se.Name] = e.get(se).ResourceVersion
				e.reconcile(se)
			}
			for _, se := range cand {
				if after := e.get(se).ResourceVersion; after != before[se.Name] {
					t.Errorf("%s was rewritten on a second pass (%s -> %s)", se.Name, before[se.Name], after)
				}
			}
			rec, ok := e.r.Recorder.(*events.FakeRecorder)
			if !ok {
				t.Fatalf("recorder is %T", e.r.Recorder)
			}
			close(rec.Events)
			var found bool
			for ev := range rec.Events {
				found = found || tt.wantEventTyp != "" && strings.HasPrefix(ev, tt.wantEventTyp) && strings.Contains(ev, map[string]string{ConditionRegressed: ReasonWorseThanStable, ConditionInconclusive: ReasonNotEnoughEvidence}[tt.wantCond])
			}
			if tt.wantEventTyp != "" && !found {
				t.Errorf("no %s event for %s", tt.wantEventTyp, tt.wantCond)
			}
		})
	}
}

func TestLiftBeyondNoiseDecidesPromotion(t *testing.T) {
	// A lift of +0.14 whose interval crosses zero is not enough for stable.
	e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
		map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}})
	var runs []*v1alpha1.SkillEval
	for _, m := range []models.Model{haiku, sonnet} {
		se := e.create("acme", "v1.0.0", digestA, m)
		e.run(se, trialMessage(0.47, 0.33, weakWith, weakWithout))
		runs = append(runs, se)
	}
	if got := tags(t, e.harbor, "acme", digestA); slices.Contains(got, "stable") {
		t.Errorf("tags = %v, a lift that is not beyond noise must not earn stable", got)
	}
	got := e.get(runs[0])
	if got.Status.Verdict != v1alpha1.VerdictNeutral {
		t.Errorf("verdict = %s, want Neutral", got.Status.Verdict)
	}
	cond := findCondition(got.Status.Conditions, ConditionPolicy)
	if cond == nil || !strings.Contains(cond.Message, "lift not beyond noise on claude-code/claude-haiku-5-5 (+0.14, 95% CI [") {
		t.Errorf("PolicyApplied = %+v, want the not-beyond-noise note", cond)
	}
}

func TestInconclusiveMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics.Register(reg)
	e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
		map[string][]harbor.Artifact{"gauge": {
			{Digest: digestA, Tags: []string{"v1.0.0", "stable"}},
			{Digest: digestB, Tags: []string{"v1.2.0"}},
		}})
	for _, m := range []models.Model{haiku, sonnet} {
		e.run(e.create("gauge", "v1.0.0", digestA, m), trialMessage(0.95, 0.36, stableNoisy, baseline))
		e.run(e.create("gauge", "v1.2.0", digestB, m), trialMessage(0.87, 0.36, dropNoisy, baseline))
	}
	n, err := testutil.GatherAndCount(reg, "skillci_lift_ci_lower", "skillci_lift_ci_upper")
	if err != nil || n < 8 {
		t.Errorf("lift interval series = %d, %v; want at least 8", n, err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, mf := range families {
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["skill"] == "gauge" && (mf.GetName() == "skillci_inconclusive" || mf.GetName() == "skillci_regressed") {
				got[mf.GetName()+"/"+labels["version"]] = m.GetGauge().GetValue()
			}
		}
	}
	if got["skillci_inconclusive/v1.2.0"] != 1 || got["skillci_regressed/v1.2.0"] != 0 {
		t.Errorf("gauges = %v, want inconclusive 1 and regressed 0 for v1.2.0", got)
	}
}
