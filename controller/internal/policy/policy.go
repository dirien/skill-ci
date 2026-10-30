// Package policy turns scores into verdicts and verdicts into tag changes.
// It is pure: no clients, no I/O.
package policy

import (
	"math"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
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
}

// DefaultThresholds returns the bands from CONTRACT.md.
func DefaultThresholds() Thresholds {
	return Thresholds{Helps: 0.05, Hurts: -0.10, Promote: 0.10, Demote: -0.10, Obsolete: 0.80, Regression: 0.05}
}

// Verdict classifies one result. Scores are compared at two decimals.
func (t Thresholds) Verdict(lift, without float64) v1alpha1.Verdict {
	lift, without = round2(lift), round2(without)
	switch {
	case lift >= t.Helps-epsilon:
		return v1alpha1.VerdictHelps
	case lift <= t.Hurts+epsilon:
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
	// Regressed means the digest scores worse than the stable version and
	// must not replace it.
	Regressed bool
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
		lift := round2(r.Lift)
		out.Demote = out.Demote || lift <= t.Demote+epsilon
		allPromote = allPromote && lift >= t.Promote-epsilon
		allObsolete = allObsolete && r.Verdict == v1alpha1.VerdictObsolete
	}
	if required == 0 {
		return Outcome{}
	}
	out.Complete = true
	out.Promote = allPromote && !out.Demote
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
}

// Regression is a required model on which a candidate scores worse than the stable version.
type Regression struct {
	// Model identifies the model.
	Model string
	// Candidate is the with-skill score of the candidate digest.
	Candidate float64
	// Stable is the with-skill score of the current stable digest.
	Stable float64
}

// Regressions compares a candidate with the current stable digest. It returns
// the required models where the candidate scores lower by more than the
// tolerance. Models only one side has a score for, and optional models, are
// not compared.
func (t Thresholds) Regressions(candidate, stable []Score) []Regression {
	base := make(map[string]float64, len(stable))
	for _, s := range stable {
		base[s.Model] = s.With
	}
	var out []Regression
	for _, c := range candidate {
		with, ok := base[c.Model]
		if ok && c.Required && round2(with-c.With) > t.Regression+epsilon {
			out = append(out, Regression{Model: c.Model, Candidate: c.With, Stable: with})
		}
	}
	return out
}

// Block marks the outcome Regressed and withholds promotion when regs is not empty.
func (o Outcome) Block(regs []Regression) Outcome {
	if len(regs) > 0 {
		o.Regressed, o.Promote = true, false
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
// before additions so a tag never sits on two digests.
func Plan(out Outcome, digest string, newest bool, arts []harbor.Artifact) []Op {
	var ops []Op
	if newest {
		ops = append(ops, moveTag(TagBeta, digest, arts)...)
	}
	switch {
	case out.Demote:
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
