// Package metrics holds the Prometheus metrics of the controller.
package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/retry"
	"github.com/dirien/skill-ci/controller/internal/stats"
)

var (
	evalLabels    = []string{"skill", "version", "agent", "model"}
	versionLabels = []string{"skill", "version"}

	publicationLabels = []string{"skill", "version", "model", "status"}
	// publicationStatuses are the values of the status label.
	publicationStatuses = []string{"passed", "failed", "incomplete"}

	lift = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_lift", Help: "With-skill minus without-skill score of the latest eval."}, evalLabels)
	liftCILower = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_lift_ci_lower", Help: "Lower bound of the confidence interval of the lift."}, evalLabels)
	liftCIUpper = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_lift_ci_upper", Help: "Upper bound of the confidence interval of the lift."}, evalLabels)
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
	inconclusive = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_inconclusive", Help: "1 when the skill version scores lower than the stable version by more than the tolerance, but not beyond noise."}, versionLabels)
	rejected = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_rejected", Help: "1 when a required model's run of the skill version failed Tier 1 or Tier 2."}, versionLabels)
	publication = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_publication", Help: "1 for the publication status SkillEvaluator reported for the run."}, publicationLabels)
	skillEvals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "skillci_skillevals_total", Help: "SkillEval phase transitions."}, []string{"phase"})
	skillEvalsNow = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "skillci_skillevals", Help: "SkillEvals that exist now, by phase."}, []string{"phase"})

	retries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "skillci_retries_total", Help: "Runs started again after an infrastructure failure, by failure class."}, []string{"reason"})

	phases = []v1alpha1.Phase{
		v1alpha1.PhasePending, v1alpha1.PhaseRunning, v1alpha1.PhaseSucceeded, v1alpha1.PhaseFailed, v1alpha1.PhasePlanned,
	}

	// tracked remembers the label values of every SkillEval that has score
	// series, so the series can be deleted when the object is gone.
	trackMu sync.Mutex
	tracked = map[string]evalKey{}
)

// evalKey is the label set of one SkillEval's series.
type evalKey struct{ skill, version, agent, model string }

// Register adds the metrics to reg and starts the phase gauge at zero.
func Register(reg prometheus.Registerer) {
	reg.MustRegister(lift, liftCILower, liftCIUpper, withScore, withoutScore, stable, obsolete, regressed, inconclusive, rejected, publication, skillEvals, skillEvalsNow, retries)
	SetPhaseCounts(nil)
	for _, r := range retry.Reasons() {
		retries.WithLabelValues(string(r)) // visible at zero before the first retry
	}
}

// ObserveScores records the scores of a finished SkillEval and, when the run
// has trial scores, the confidence interval of its lift. A nil interval removes
// the interval series.
func ObserveScores(se *v1alpha1.SkillEval, with, without, liftValue float64, ci *stats.Interval) {
	trackMu.Lock()
	defer trackMu.Unlock()
	key := track(se)
	withScore.WithLabelValues(key.skill, key.version, key.agent, key.model).Set(with)
	withoutScore.WithLabelValues(key.skill, key.version, key.agent, key.model).Set(without)
	lift.WithLabelValues(key.skill, key.version, key.agent, key.model).Set(liftValue)
	if ci == nil {
		liftCILower.DeleteLabelValues(key.skill, key.version, key.agent, key.model)
		liftCIUpper.DeleteLabelValues(key.skill, key.version, key.agent, key.model)
		return
	}
	liftCILower.WithLabelValues(key.skill, key.version, key.agent, key.model).Set(ci.Lower)
	liftCIUpper.WithLabelValues(key.skill, key.version, key.agent, key.model).Set(ci.Upper)
}

// ObservePublication records the publication status of a finished SkillEval:
// the series for the status is 1 and the series of the other statuses are
// removed. An empty or unknown status, as an older runner leaves, removes all.
func ObservePublication(se *v1alpha1.SkillEval) {
	trackMu.Lock()
	defer trackMu.Unlock()
	key := track(se)
	for _, status := range publicationStatuses {
		if status == se.Status.Publication {
			publication.WithLabelValues(key.skill, key.version, key.model, status).Set(1)
		} else {
			publication.DeleteLabelValues(key.skill, key.version, key.model, status)
		}
	}
}

// track remembers the label set of se and drops the series of an earlier one
// when the spec changed. The caller holds trackMu.
func track(se *v1alpha1.SkillEval) evalKey {
	key := evalKey{se.Spec.Skill, se.Spec.Version, se.Spec.Agent, se.Spec.Model}
	id := se.Namespace + "/" + se.Name
	if old, ok := tracked[id]; ok && old != key {
		deleteEval(old) // the spec changed: the old label set must not linger
		if !sharesModel(old, id) {
			deletePublication(old)
		}
	}
	tracked[id] = key
	return key
}

// sharesModel reports whether a tracked SkillEval other than id has the skill,
// version and model of k, and so the same publication series.
func sharesModel(k evalKey, id string) bool {
	for other, key := range tracked {
		if other != id && key.skill == k.skill && key.version == k.version && key.model == k.model {
			return true
		}
	}
	return false
}

func deletePublication(k evalKey) {
	for _, status := range publicationStatuses {
		publication.DeleteLabelValues(k.skill, k.version, k.model, status)
	}
}

// Forget removes the series of a deleted SkillEval, named namespace/name. When
// no other tracked SkillEval has the same skill and version, the version-level
// series (stable, obsolete, regressed, inconclusive, rejected) go too.
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
	if !sharesModel(key, id) {
		deletePublication(key)
	}
	for _, other := range tracked {
		if other.skill == key.skill && other.version == key.version {
			return
		}
	}
	stable.DeleteLabelValues(key.skill, key.version)
	obsolete.DeleteLabelValues(key.skill, key.version)
	regressed.DeleteLabelValues(key.skill, key.version)
	inconclusive.DeleteLabelValues(key.skill, key.version)
	rejected.DeleteLabelValues(key.skill, key.version)
}

func deleteEval(k evalKey) {
	lift.DeleteLabelValues(k.skill, k.version, k.agent, k.model)
	liftCILower.DeleteLabelValues(k.skill, k.version, k.agent, k.model)
	liftCIUpper.DeleteLabelValues(k.skill, k.version, k.agent, k.model)
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

// SetInconclusive records whether a skill version is held back for lack of evidence: it scores lower than the stable version by more than the tolerance, but not beyond noise.
func SetInconclusive(skill, version string, v bool) {
	inconclusive.WithLabelValues(skill, version).Set(boolToFloat(v))
}

// SetRejected records whether a required model's run of a skill version failed Tier 1 or Tier 2.
func SetRejected(skill, version string, v bool) {
	rejected.WithLabelValues(skill, version).Set(boolToFloat(v))
}

// CountRetry counts one run started again after an infrastructure failure of the given class.
func CountRetry(reason retry.Reason) {
	retries.WithLabelValues(string(reason)).Inc()
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
