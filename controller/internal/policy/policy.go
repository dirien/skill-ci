// Package policy turns scores into verdicts and verdicts into tag changes.
// It is pure: no clients, no I/O.
//
// A fixed number is a bad gate when the same skill version scores 0.95 on one
// run and 0.87 on the next. When a run carries its per-trial scores, the gate
// asks for evidence instead: a lift counts when its confidence interval clears
// zero, a drop below the stable version counts when its interval stays below
// zero. Runs without trial scores keep the fixed-number rules.
package policy

import (
	"math"
	"slices"
	"strconv"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/stats"
)

// Tags managed by the controller.
const (
	// TagBeta marks the newest evaluated version of a skill.
	TagBeta = "beta"
	// TagStable marks a version that helps on every required model.
	TagStable = "stable"
)

// epsilon absorbs float noise when comparing two-decimal scores to thresholds.
const epsilon = 1e-9

// Thresholds are the lift bands that decide verdicts and tag changes.
type Thresholds struct {
	// Helps is the lift at or above which a result is Helps.
	Helps float64
	// Hurts is the lift at or below which a result is Hurts.
	Hurts float64
	// Promote is the lift every required model must reach to earn stable.
	Promote float64
	// Demote is the lift on any required model that removes stable.
	Demote float64
	// Obsolete is the without-skill score at or above which a Neutral result is Obsolete.
	Obsolete float64
	// Regression is the with-skill drop below the stable version, per required
	// model, that is still tolerated. A bigger drop blocks promotion.
	Regression float64
	// Confidence is the level of the confidence intervals, for example 0.95.
	Confidence float64
}

// DefaultThresholds returns the bands from CONTRACT.md.
func DefaultThresholds() Thresholds {
	return Thresholds{Helps: 0.05, Hurts: -0.10, Promote: 0.10, Demote: -0.10, Obsolete: 0.80, Regression: 0.05, Confidence: 0.95}
}

// Percent renders the confidence level as text, 0.95 as "95%".
func (t Thresholds) Percent() string {
	return strconv.FormatFloat(math.Round(t.Confidence*1000)/10, 'f', -1, 64) + "%"
}

// CI returns the confidence interval of mean(a) minus mean(b) at the
// configured confidence: the lift when a and b are the with and without-skill
// trial scores of one run, the difference between two versions when they are
// the with-skill scores of both. It is nil when either sample has fewer than
// two scores, which sends the caller to the fixed-number rules.
func (t Thresholds) CI(a, b []float64) *stats.Interval {
	ci, ok := stats.WelchDiff(a, b, t.Confidence)
	if !ok {
		return nil
	}
	return &ci
}

// Verdict classifies one result. Scores are compared at two decimals. With a
// lift interval, Helps also needs the interval's lower bound above zero and
// Hurts its upper bound below zero; a lift that big but not beyond noise is
// Neutral. A nil interval (no trial scores) uses the lift bands alone.
func (t Thresholds) Verdict(lift, without float64, ci *stats.Interval) v1alpha1.Verdict {
	lift, without = round2(lift), round2(without)
	switch {
	case lift >= t.Helps-epsilon && (ci == nil || ci.Lower > 0):
		return v1alpha1.VerdictHelps
	case lift <= t.Hurts+epsilon && (ci == nil || ci.Upper < 0):
		return v1alpha1.VerdictHurts
	case without >= t.Obsolete-epsilon:
		return v1alpha1.VerdictObsolete
	default:
		return v1alpha1.VerdictNeutral
	}
}

// Result is the part of a SkillEval the digest policy looks at.
type Result struct {
	// Required marks results that gate promotion.
	Required bool
	// Succeeded is false while the eval is pending, running or failed.
	Succeeded bool
	// Lift is the with-skill minus without-skill score.
	Lift float64
	// Verdict is the classification of this result.
	Verdict v1alpha1.Verdict
	// LiftCI is the confidence interval of Lift; nil without trial scores.
	LiftCI *stats.Interval
	// Rejected is true when Tier 1 or Tier 2 failed. A rejected result is
	// finished (Succeeded is true) but has no usable lift.
	Rejected bool
}

// Outcome is what the results of one digest add up to.
type Outcome struct {
	// Complete is true once every required model has a result.
	Complete bool
	// Promote means the digest earns the stable tag.
	Promote bool
	// Demote means the digest loses the stable tag.
	Demote bool
	// Obsolete means every required model does fine without the skill.
	Obsolete bool
	// Regressed means the digest scores worse than the stable version, beyond
	// noise, and must not replace it.
	Regressed bool
	// Inconclusive means the digest scores lower than the stable version by
	// more than the tolerance, but the interval of the difference includes
	// zero. It must not replace the stable version either.
	Inconclusive bool
	// Unproven means a required model reached the promote lift, but its
	// confidence interval does not exclude zero.
	Unproven bool
	// Rejected means a required model's run failed Tier 1 or Tier 2. The digest
	// never gets beta or stable and loses stable if it has it.
	Rejected bool
}

// Evaluate applies the promote, demote and obsolete rules to one digest.
func (t Thresholds) Evaluate(results []Result) Outcome {
	var out Outcome
	required := 0
	allPromote, allObsolete := true, true
	for _, r := range results {
		if !r.Required {
			continue
		}
		required++
		if !r.Succeeded {
			return Outcome{}
		}
		if r.Rejected {
			out.Rejected, allPromote, allObsolete = true, false, false
			continue
		}
		lift := round2(r.Lift)
		out.Demote = out.Demote || (lift <= t.Demote+epsilon && (r.LiftCI == nil || r.LiftCI.Upper < 0))
		beyondNoise := r.LiftCI == nil || r.LiftCI.Lower > 0
		out.Unproven = out.Unproven || (lift >= t.Promote-epsilon && !beyondNoise)
		allPromote = allPromote && lift >= t.Promote-epsilon && beyondNoise
		allObsolete = allObsolete && r.Verdict == v1alpha1.VerdictObsolete
	}
	if required == 0 {
		return Outcome{}
	}
	out.Complete = true
	out.Promote = allPromote && !out.Demote && !out.Rejected
	out.Obsolete = allObsolete
	return out
}

// Score is the with-skill score of one digest on one model.
type Score struct {
	// Model identifies the model, for example claude-code/claude-haiku-5-5.
	Model string
	// Required marks models that gate promotion.
	Required bool
	// With is the with-skill score.
	With float64
	// Samples are the with-skill trial scores; empty when the runner sent none.
	Samples []float64
}

// Regression is a required model on which a candidate scores lower than the
// stable version by more than the tolerance.
type Regression struct {
	// Model identifies the model.
	Model string
	// Candidate is the with-skill score of the candidate digest: the mean of
	// its trial scores, or the run's score without trial scores.
	Candidate float64
	// Stable is the same for the current stable digest.
	Stable float64
	// CI is the confidence interval of Candidate minus Stable; nil when either
	// side has no trial scores, and the drop is taken at face value.
	CI *stats.Interval
	// Inconclusive is true when CI includes zero: the drop may be noise.
	Inconclusive bool
}

// Diff returns Candidate minus Stable.
func (r Regression) Diff() float64 { return r.Candidate - r.Stable }

// Regressions compares a candidate with the current stable digest. It returns
// the required models where the candidate scores lower by more than the
// tolerance. A drop whose confidence interval reaches zero or above is marked
// Inconclusive; without trial scores on both sides every drop counts as real.
// Models only one side has a score for, and optional models, are not compared.
func (t Thresholds) Regressions(candidate, stable []Score) []Regression {
	base := make(map[string]Score, len(stable))
	for _, s := range stable {
		base[s.Model] = s
	}
	var out []Regression
	for _, c := range candidate {
		s, ok := base[c.Model]
		if !ok || !c.Required {
			continue
		}
		reg := Regression{Model: c.Model, Candidate: c.With, Stable: s.With}
		if ci := t.CI(c.Samples, s.Samples); ci != nil {
			reg.CI, reg.Candidate, reg.Stable = ci, stats.Mean(c.Samples), stats.Mean(s.Samples)
			reg.Inconclusive = ci.Upper >= 0
		}
		if round2(reg.Diff()) < -t.Regression-epsilon {
			out = append(out, reg)
		}
	}
	return out
}

// Confirmed returns the regressions that hold beyond noise.
func Confirmed(regs []Regression) []Regression {
	return slices.DeleteFunc(slices.Clone(regs), func(r Regression) bool { return r.Inconclusive })
}

// Block withholds promotion when regs is not empty. A confirmed regression
// marks the outcome Regressed; a drop that may be noise marks it Inconclusive.
func (o Outcome) Block(regs []Regression) Outcome {
	for _, r := range regs {
		if r.Inconclusive {
			o.Inconclusive = true
		} else {
			o.Regressed = true
		}
		o.Promote = false
	}
	return o
}

// Op is one tag change.
type Op struct {
	// Add is true to add the tag, false to remove it.
	Add bool
	// Digest is the artifact to change.
	Digest string
	// Tag is the tag name.
	Tag string
}

// Plan returns the tag changes for the digest under evaluation. Newest says
// whether it is the newest fully evaluated version of its skill. Removals come
// before additions so a tag never sits on two digests. A rejected digest never
// gets beta or stable, and loses stable.
func Plan(out Outcome, digest string, newest bool, arts []harbor.Artifact) []Op {
	var ops []Op
	if newest && !out.Rejected {
		ops = append(ops, moveTag(TagBeta, digest, arts)...)
	}
	switch {
	case out.Demote || out.Rejected:
		if harbor.Tagged(arts, digest, TagStable) {
			ops = append(ops, Op{Digest: digest, Tag: TagStable})
		}
	case out.Promote:
		ops = append(ops, moveTag(TagStable, digest, arts)...)
	}
	return ops
}

// moveTag puts tag on digest and takes it off every other artifact.
func moveTag(tag, digest string, arts []harbor.Artifact) []Op {
	var ops []Op
	for _, a := range arts {
		if a.Digest != digest && a.HasTag(tag) {
			ops = append(ops, Op{Digest: a.Digest, Tag: tag})
		}
	}
	if !harbor.Tagged(arts, digest, tag) {
		ops = append(ops, Op{Add: true, Digest: digest, Tag: tag})
	}
	return ops
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
