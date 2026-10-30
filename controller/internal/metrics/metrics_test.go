package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/stats"
)

func TestMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	Register(reg)

	se := &v1alpha1.SkillEval{Spec: v1alpha1.SkillEvalSpec{Skill: "acme", Version: "v1.0.0", Agent: "claude-code", AgentVersion: "2.1.292", Model: "haiku"}}
	ObserveScores(se, 0.95, 0.36, 0.59, &stats.Interval{Diff: 0.59, Lower: 0.41, Upper: 0.73})
	SetInconclusive("acme", "v1.3.0", true)
	SetStable("acme", "v1.0.0", true)
	SetObsolete("acme", "v1.0.0", false)
	SetRegressed("acme", "v1.2.0", true)
	CountPhase(v1alpha1.PhaseSucceeded)

	want := `
# HELP skillci_lift With-skill minus without-skill score of the latest eval.
# TYPE skillci_lift gauge
skillci_lift{agent="claude-code",agent_version="2.1.292",model="haiku",skill="acme",version="v1.0.0"} 0.59
# HELP skillci_lift_ci_lower Lower bound of the confidence interval of the lift.
# TYPE skillci_lift_ci_lower gauge
skillci_lift_ci_lower{agent="claude-code",agent_version="2.1.292",model="haiku",skill="acme",version="v1.0.0"} 0.41
# HELP skillci_lift_ci_upper Upper bound of the confidence interval of the lift.
# TYPE skillci_lift_ci_upper gauge
skillci_lift_ci_upper{agent="claude-code",agent_version="2.1.292",model="haiku",skill="acme",version="v1.0.0"} 0.73
# HELP skillci_inconclusive 1 when the skill version scores lower than the stable version by more than the tolerance, but not beyond noise.
# TYPE skillci_inconclusive gauge
skillci_inconclusive{skill="acme",version="v1.3.0"} 1
# HELP skillci_stable 1 when the skill version is tagged stable.
# TYPE skillci_stable gauge
skillci_stable{skill="acme",version="v1.0.0"} 1
# HELP skillci_regressed 1 when the skill version scores worse than the stable version.
# TYPE skillci_regressed gauge
skillci_regressed{skill="acme",version="v1.2.0"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "skillci_lift", "skillci_lift_ci_lower", "skillci_lift_ci_upper", "skillci_inconclusive", "skillci_stable", "skillci_regressed"); err != nil {
		t.Error(err)
	}
	if n, err := testutil.GatherAndCount(reg); err != nil || n != 20 {
		t.Errorf("series = %d, %v; want 20", n, err)
	}
}

func se(name, skill, version, model string) *v1alpha1.SkillEval {
	s := &v1alpha1.SkillEval{Spec: v1alpha1.SkillEvalSpec{Skill: skill, Version: version, Agent: "claude-code", AgentVersion: "2.1.292", Model: model}}
	s.Namespace, s.Name = "skill-ci", name
	return s
}

// resetAll clears the package-level series so cases do not see each other.
func resetAll() {
	for _, g := range []*prometheus.GaugeVec{lift, liftCILower, liftCIUpper, withScore, withoutScore, stable, obsolete, regressed, inconclusive, rejected, publication} {
		g.Reset()
	}
	clear(tracked)
}

func TestForgetRemovesSeries(t *testing.T) {
	type obs struct{ name, skill, version, model string }
	tests := []struct {
		name       string
		observed   []obs
		forget     []string
		wantSeries int // gauge series left, excluding the five phase gauges
	}{
		{"unknown name changes nothing", []obs{{"a", "acme", "v1", "haiku"}}, []string{"zzz"}, 5 + 4},
		{"last eval of a version drops its version series", []obs{{"a", "acme", "v1", "haiku"}}, []string{"a"}, 0},
		{"a sibling keeps the version series", []obs{{"a", "acme", "v1", "haiku"}, {"b", "acme", "v1", "sonnet"}}, []string{"a"}, 5 + 4},
		{"other version keeps its series", []obs{{"a", "acme", "v1", "haiku"}, {"b", "acme", "v2", "haiku"}}, []string{"a"}, 5 + 4},
		{"forgetting twice is harmless", []obs{{"a", "acme", "v1", "haiku"}}, []string{"a", "a"}, 0},
		{"all gone", []obs{{"a", "acme", "v1", "haiku"}, {"b", "acme", "v1", "sonnet"}}, []string{"a", "b"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			Register(reg)
			resetAll()
			t.Cleanup(resetAll)
			for _, o := range tt.observed {
				ObserveScores(se(o.name, o.skill, o.version, o.model), 0.9, 0.3, 0.6, &stats.Interval{Diff: 0.6, Lower: 0.4, Upper: 0.8})
				SetStable(o.skill, o.version, true)
				SetObsolete(o.skill, o.version, false)
				SetRegressed(o.skill, o.version, false)
				SetInconclusive(o.skill, o.version, false)
			}
			for _, name := range tt.forget {
				Forget("skill-ci", name)
			}
			n, err := testutil.GatherAndCount(reg, "skillci_lift", "skillci_with_score", "skillci_without_score", "skillci_stable", "skillci_obsolete", "skillci_regressed", "skillci_lift_ci_lower", "skillci_lift_ci_upper", "skillci_inconclusive")
			if err != nil {
				t.Fatal(err)
			}
			if n != tt.wantSeries {
				t.Errorf("series = %d, want %d", n, tt.wantSeries)
			}
		})
	}
}

func TestPublication(t *testing.T) {
	reg := prometheus.NewRegistry()
	Register(reg)
	resetAll()
	t.Cleanup(resetAll)

	run := se("a", "acme", "v1.0.0", "haiku")
	run.Status.Publication = "failed"
	ObservePublication(run)
	SetRejected("acme", "v1.0.0", true)

	want := `
# HELP skillci_publication 1 for the publication status SkillEvaluator reported for the run.
# TYPE skillci_publication gauge
skillci_publication{agent="claude-code",agent_version="2.1.292",model="haiku",skill="acme",status="failed",version="v1.0.0"} 1
# HELP skillci_rejected 1 when a required model's run of the skill version failed Tier 1 or Tier 2.
# TYPE skillci_rejected gauge
skillci_rejected{skill="acme",version="v1.0.0"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "skillci_publication", "skillci_rejected"); err != nil {
		t.Error(err)
	}

	// A rerun with another status replaces the series instead of adding one.
	run.Status.Publication = "passed"
	ObservePublication(run)
	if n, err := testutil.GatherAndCount(reg, "skillci_publication"); err != nil || n != 1 {
		t.Errorf("publication series after a rerun = %d, %v; want 1", n, err)
	}
	// A run from a runner without publication leaves no series.
	run.Status.Publication = ""
	ObservePublication(run)
	if n, err := testutil.GatherAndCount(reg, "skillci_publication"); err != nil || n != 0 {
		t.Errorf("publication series without a status = %d, %v; want 0", n, err)
	}
}

func TestForgetRemovesPublication(t *testing.T) {
	tests := []struct {
		name       string
		forget     []string
		wantSeries int // publication and rejected series left
	}{
		{"forget one of two models", []string{"a"}, 1 + 1},
		{"forget both", []string{"a", "b"}, 0},
		{"forget nothing", nil, 2 + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			Register(reg)
			resetAll()
			t.Cleanup(resetAll)
			for _, o := range []struct{ name, model string }{{"a", "haiku"}, {"b", "sonnet"}} {
				run := se(o.name, "acme", "v1.0.0", o.model)
				run.Status.Publication = "failed"
				ObservePublication(run)
			}
			SetRejected("acme", "v1.0.0", true)
			for _, name := range tt.forget {
				Forget("skill-ci", name)
			}
			n, err := testutil.GatherAndCount(reg, "skillci_publication", "skillci_rejected")
			if err != nil {
				t.Fatal(err)
			}
			if n != tt.wantSeries {
				t.Errorf("series = %d, want %d", n, tt.wantSeries)
			}
		})
	}
}

func TestPhaseCounts(t *testing.T) {
	reg := prometheus.NewRegistry()
	Register(reg)
	SetPhaseCounts(map[v1alpha1.Phase]int{v1alpha1.PhaseRunning: 2, v1alpha1.PhaseSucceeded: 5})
	SetPhaseCounts(map[v1alpha1.Phase]int{v1alpha1.PhaseSucceeded: 4}) // running drained
	want := `
# HELP skillci_skillevals SkillEvals that exist now, by phase.
# TYPE skillci_skillevals gauge
skillci_skillevals{phase="Failed"} 0
skillci_skillevals{phase="Pending"} 0
skillci_skillevals{phase="Planned"} 0
skillci_skillevals{phase="Running"} 0
skillci_skillevals{phase="Succeeded"} 4
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "skillci_skillevals"); err != nil {
		t.Error(err)
	}
}

func TestAgentVersionLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	Register(reg)
	resetAll()
	t.Cleanup(resetAll)

	// The same model under two agents, and the same agent in two versions, are four series.
	for i, o := range []struct{ agent, version string }{
		{"claude-code", "2.1.292"}, {"claude-code", "2.1.300"}, {"opencode", "1.19.1"}, {"opencode", "1.19.2"},
	} {
		run := se(o.agent+o.version, "acme", "v1.0.0", "claude-opus-5-5")
		run.Spec.Agent, run.Spec.AgentVersion = o.agent, o.version
		ObserveScores(run, 0.9, 0.3, 0.6, nil)
		run.Status.Publication = "passed"
		ObservePublication(run)
		if n, err := testutil.GatherAndCount(reg, "skillci_lift", "skillci_publication"); err != nil || n != 2*(i+1) {
			t.Fatalf("after %d runs: series = %d, %v; want %d", i+1, n, err, 2*(i+1))
		}
	}

	// Without a pinned version the label carries the one the runner reported; before that, unknown.
	run := se("unpinned", "acme", "v1.0.0", "claude-opus-5-5")
	run.Spec.AgentVersion, run.Status.AgentVersion = "", "2.1.292"
	run.Spec.Agent = "codex"
	ObserveScores(run, 0.9, 0.3, 0.6, nil)
	run.Status.AgentVersion = ""
	run.Spec.Agent = "codex2"
	ObserveScores(run, 0.9, 0.3, 0.6, nil)
	want := `
# HELP skillci_lift With-skill minus without-skill score of the latest eval.
# TYPE skillci_lift gauge
skillci_lift{agent="claude-code",agent_version="2.1.292",model="claude-opus-5-5",skill="acme",version="v1.0.0"} 0.6
skillci_lift{agent="claude-code",agent_version="2.1.300",model="claude-opus-5-5",skill="acme",version="v1.0.0"} 0.6
skillci_lift{agent="codex2",agent_version="unknown",model="claude-opus-5-5",skill="acme",version="v1.0.0"} 0.6
skillci_lift{agent="opencode",agent_version="1.19.1",model="claude-opus-5-5",skill="acme",version="v1.0.0"} 0.6
skillci_lift{agent="opencode",agent_version="1.19.2",model="claude-opus-5-5",skill="acme",version="v1.0.0"} 0.6
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "skillci_lift"); err != nil {
		t.Error(err)
	}
}
