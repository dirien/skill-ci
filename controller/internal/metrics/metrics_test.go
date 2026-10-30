package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
)

func TestMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	Register(reg)

	se := &v1alpha1.SkillEval{Spec: v1alpha1.SkillEvalSpec{Skill: "acme", Version: "v1.0.0", Agent: "claude-code", Model: "haiku"}}
	ObserveScores(se, 0.95, 0.36, 0.59)
	SetStable("acme", "v1.0.0", true)
	SetObsolete("acme", "v1.0.0", false)
	CountPhase(v1alpha1.PhaseSucceeded)

	want := `
# HELP skillci_lift With-skill minus without-skill score of the latest eval.
# TYPE skillci_lift gauge
skillci_lift{agent="claude-code",model="haiku",skill="acme",version="v1.0.0"} 0.59
# HELP skillci_stable 1 when the skill version is tagged stable.
# TYPE skillci_stable gauge
skillci_stable{skill="acme",version="v1.0.0"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "skillci_lift", "skillci_stable"); err != nil {
		t.Error(err)
	}
	if n, err := testutil.GatherAndCount(reg); err != nil || n != 6 {
		t.Errorf("series = %d, %v; want 6", n, err)
	}
}
