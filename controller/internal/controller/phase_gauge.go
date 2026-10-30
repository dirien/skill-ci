package controller

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/metrics"
)

// PhaseGauge keeps skillci_skillevals current: every Interval it counts the
// SkillEvals in the cache by phase.
type PhaseGauge struct {
	// Reader lists SkillEvals; the manager's cached client fits.
	Reader client.Reader
	// Namespace is where the SkillEvals live.
	Namespace string
	// Interval is the time between counts; zero means 5 seconds.
	Interval time.Duration
}

// Start counts once, then on every tick until ctx is done.
func (g *PhaseGauge) Start(ctx context.Context) error {
	interval := g.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := g.Count(ctx); err != nil {
			logf.FromContext(ctx).Error(err, "counting SkillEvals by phase")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Count publishes the current number of SkillEvals per phase. A SkillEval
// without a phase counts as Pending.
func (g *PhaseGauge) Count(ctx context.Context) error {
	var list v1alpha1.SkillEvalList
	if err := g.Reader.List(ctx, &list, client.InNamespace(g.Namespace)); err != nil {
		return fmt.Errorf("listing SkillEvals: %w", err)
	}
	counts := map[v1alpha1.Phase]int{}
	for i := range list.Items {
		phase := list.Items[i].Status.Phase
		if phase == "" {
			phase = v1alpha1.PhasePending
		}
		counts[phase]++
	}
	metrics.SetPhaseCounts(counts)
	return nil
}

// NeedLeaderElection reports false: every replica serves its own metrics.
func (g *PhaseGauge) NeedLeaderElection() bool { return false }
