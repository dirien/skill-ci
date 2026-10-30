// Package metrics holds the Prometheus metrics of the controller.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
)

var (
	evalLabels    = []string{"skill", "version", "agent", "model"}
	versionLabels = []string{"skill", "version"}

	lift = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_lift", Help: "With-skill minus without-skill score of the latest eval."}, evalLabels)
	withScore = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_with_score", Help: "Score with the skill."}, evalLabels)
	withoutScore = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_without_score", Help: "Score without the skill."}, evalLabels)
	stable = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_stable", Help: "1 when the skill version is tagged stable."}, versionLabels)
	obsolete = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_obsolete", Help: "1 when every required model does fine without the skill version."}, versionLabels)
	regressed = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_regressed", Help: "1 when the skill version scores worse than the stable version."}, versionLabels)
	skillEvals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "skillci_skillevals_total", Help: "SkillEval phase transitions."}, []string{"phase"})
)

// Register adds the metrics to reg.
func Register(reg prometheus.Registerer) {
	reg.MustRegister(lift, withScore, withoutScore, stable, obsolete, regressed, skillEvals)
}

// ObserveScores records the scores of a finished SkillEval.
func ObserveScores(se *v1alpha1.SkillEval, with, without, liftValue float64) {
	l := prometheus.Labels{"skill": se.Spec.Skill, "version": se.Spec.Version, "agent": se.Spec.Agent, "model": se.Spec.Model}
	withScore.With(l).Set(with)
	withoutScore.With(l).Set(without)
	lift.With(l).Set(liftValue)
}

// SetStable records whether a skill version carries the stable tag.
func SetStable(skill, version string, v bool) {
	stable.WithLabelValues(skill, version).Set(boolToFloat(v))
}

// SetObsolete records whether a skill version is flagged obsolete.
func SetObsolete(skill, version string, v bool) {
	obsolete.WithLabelValues(skill, version).Set(boolToFloat(v))
}

// SetRegressed records whether a skill version scores worse than the stable version.
func SetRegressed(skill, version string, v bool) {
	regressed.WithLabelValues(skill, version).Set(boolToFloat(v))
}

// CountPhase counts one SkillEval entering phase.
func CountPhase(phase v1alpha1.Phase) {
	skillEvals.WithLabelValues(string(phase)).Inc()
}

func boolToFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
