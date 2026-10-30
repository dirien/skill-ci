package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/metrics"
	"github.com/dirien/skill-ci/controller/internal/policy"
)

func TestPhaseGaugeCountsPhases(t *testing.T) {
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, phase v1alpha1.Phase, namespace string) *v1alpha1.SkillEval {
		return &v1alpha1.SkillEval{
			Name: name, Namespace: namespace,
			Status: v1alpha1.SkillEvalStatus{Phase: phase},
		}
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		mk("a", v1alpha1.PhaseSucceeded, ns), mk("b", v1alpha1.PhaseSucceeded, ns), mk("c", v1alpha1.PhaseRunning, ns),
		mk("d", "", ns), mk("e", v1alpha1.PhaseFailed, "other"),
	).Build()
	reg := prometheus.NewRegistry()
	metrics.Register(reg)

	if err := (&PhaseGauge{Reader: c, Namespace: ns}).Count(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := `
# HELP skillci_skillevals SkillEvals that exist now, by phase.
# TYPE skillci_skillevals gauge
skillci_skillevals{phase="Failed"} 0
skillci_skillevals{phase="Pending"} 1
skillci_skillevals{phase="Planned"} 0
skillci_skillevals{phase="Running"} 1
skillci_skillevals{phase="Succeeded"} 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "skillci_skillevals"); err != nil {
		t.Error(err)
	}
}

func TestDeletedSkillEvalDropsItsSeries(t *testing.T) {
	e := newEnv(t, Config{Thresholds: policy.DefaultThresholds()}, 100, map[string][]harbor.Artifact{
		"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}},
	})
	se := e.create("acme", "v1.0.0", digestA, haiku)
	e.run(se, `{"withSkill": 0.95, "withoutSkill": 0.36, "lift": 0.59, "agent": "claude-code", "model": "claude-haiku-5-5"}`)

	reg := prometheus.NewRegistry()
	metrics.Register(reg)
	// Other tests in the package leave series behind, so look for this one's labels only.
	has := func() bool {
		families, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range families {
			if f.GetName() != "skillci_lift" {
				continue
			}
			for _, m := range f.GetMetric() {
				labels := map[string]string{}
				for _, l := range m.GetLabel() {
					labels[l.GetName()] = l.GetValue()
				}
				if labels["skill"] == "acme" && labels["version"] == "v1.0.0" && labels["model"] == "claude-haiku-5-5" {
					return true
				}
			}
		}
		return false
	}
	if !has() {
		t.Fatal("a finished SkillEval left no series")
	}
	if err := e.client.Delete(context.Background(), se); err != nil {
		t.Fatal(err)
	}
	e.reconcile(se) // the delete event reaches the reconciler as NotFound
	if has() {
		t.Error("the series of a deleted SkillEval is still exported")
	}
}
