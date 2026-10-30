// Package metrics holds the Prometheus metrics of the controller.
package metrics

import (
	"sync"

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
	skillEvalsNow = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_skillevals", Help: "SkillEvals that exist now, by phase."}, []string{"phase"})

	phases = []v1alpha1.Phase{
		v1alpha1.PhasePending, v1alpha1.PhaseRunning, v1alpha1.PhaseSucceeded, v1alpha1.PhaseFailed, v1alpha1.PhasePlanned,
	}

	// tracked remembers the label values of every SkillEval that has score
	// series, so the series can be deleted when the object is gone.
	trackMu sync.Mutex
	tracked = map[string]evalKey{}
)

// evalKey is the label set of one SkillEval's score series.
type evalKey struct{ skill, version, agent, model string }

// Register adds the metrics to reg and starts the phase gauge at zero.
func Register(reg prometheus.Registerer) {
	reg.MustRegister(lift, withScore, withoutScore, stable, obsolete, regressed, skillEvals, skillEvalsNow)
	SetPhaseCounts(nil)
}

// ObserveScores records the scores of a finished SkillEval.
func ObserveScores(se *v1alpha1.SkillEval, with, without, liftValue float64) {
	key := evalKey{se.Spec.Skill, se.Spec.Version, se.Spec.Agent, se.Spec.Model}
	id := se.Namespace + "/" + se.Name
	trackMu.Lock()
	defer trackMu.Unlock()
	if old, ok := tracked[id]; ok && old != key {
		deleteEval(old) // the spec changed: the old label set must not linger
	}
	tracked[id] = key
	withScore.WithLabelValues(key.skill, key.version, key.agent, key.model).Set(with)
	withoutScore.WithLabelValues(key.skill, key.version, key.agent, key.model).Set(without)
	lift.WithLabelValues(key.skill, key.version, key.agent, key.model).Set(liftValue)
}

// Forget removes the series of a deleted SkillEval, named namespace/name. When
// no other tracked SkillEval has the same skill and version, the version-level
// series (stable, obsolete, regressed) go too.
func Forget(namespace, name string) {
	id := namespace + "/" + name
	trackMu.Lock()
	defer trackMu.Unlock()
	key, ok := tracked[id]
	if !ok {
		return
	}
	delete(tracked, id)
	deleteEval(key)
	for _, other := range tracked {
		if other.skill == key.skill && other.version == key.version {
			return
		}
	}
	stable.DeleteLabelValues(key.skill, key.version)
	obsolete.DeleteLabelValues(key.skill, key.version)
	regressed.DeleteLabelValues(key.skill, key.version)
}

func deleteEval(k evalKey) {
	lift.DeleteLabelValues(k.skill, k.version, k.agent, k.model)
	withScore.DeleteLabelValues(k.skill, k.version, k.agent, k.model)
	withoutScore.DeleteLabelValues(k.skill, k.version, k.agent, k.model)
}

// SetPhaseCounts publishes how many SkillEvals exist per phase. Phases missing
// from counts are set to zero, so a drained phase does not keep its last value.
func SetPhaseCounts(counts map[v1alpha1.Phase]int) {
	for _, p := range phases {
		skillEvalsNow.WithLabelValues(string(p)).Set(float64(counts[p]))
	}
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
