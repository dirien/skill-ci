package controller

import (
	"fmt"
	"math"
	"strconv"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/runner"
	"github.com/dirien/skill-ci/controller/internal/stats"
)

// trialScores reads the per-trial scores of both arms back from the status.
// Both are nil when the run has none.
func trialScores(se *v1alpha1.SkillEval) (with, without []float64) {
	s := se.Status.Samples
	if s == nil {
		return nil, nil
	}
	return parseScores(s.WithSkill), parseScores(s.WithoutSkill)
}

// parseScores converts two-decimal strings to numbers, skipping unreadable ones.
func parseScores(in []string) []float64 {
	var out []float64
	for _, v := range in {
		if f, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			out = append(out, f)
		}
	}
	return out
}

// liftCI returns the confidence interval of the lift of a run, or nil when the
// run has fewer than two trial scores in an arm.
func liftCI(se *v1alpha1.SkillEval, th policy.Thresholds) *stats.Interval {
	with, without := trialScores(se)
	return th.CI(with, without)
}

// setSamples stores the trial scores of a result in the status, as strings.
func setSamples(st *v1alpha1.SkillEvalStatus, s *runner.Samples) {
	st.Samples = nil
	if s == nil {
		return
	}
	text := func(in []float64) []string {
		out := make([]string, len(in))
		for i, v := range in {
			out[i] = format(v, "%.2f")
		}
		return out
	}
	st.Samples = &v1alpha1.Samples{WithSkill: text(s.WithSkill), WithoutSkill: text(s.WithoutSkill)}
}

// refreshStats recomputes the statistics in the status (standard errors, n and
// the lift interval) from the stored trial scores, at the configured
// confidence. It reports whether anything changed. A run without trial scores
// has no statistics.
func refreshStats(se *v1alpha1.SkillEval, th policy.Thresholds) bool {
	st := &se.Status
	with, without := trialScores(se)
	var withErr, withoutErr, n, ci string
	if interval := th.CI(with, without); interval != nil {
		withErr, withoutErr = format(stats.StdErr(with), "%.2f"), format(stats.StdErr(without), "%.2f")
		n = fmt.Sprintf("%d/%d", len(with), len(without))
		ci = formatCI(interval)
	}
	changed := st.WithSkillStdErr != withErr || st.WithoutSkillStdErr != withoutErr || st.N != n || st.LiftCI != ci
	st.WithSkillStdErr, st.WithoutSkillStdErr, st.N, st.LiftCI = withErr, withoutErr, n, ci
	return changed
}

// formatCI renders an interval as [+0.41, +0.73].
func formatCI(ci *stats.Interval) string {
	return fmt.Sprintf("[%s, %s]", format(ci.Lower, "%+.2f"), format(ci.Upper, "%+.2f"))
}
