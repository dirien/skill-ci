package reportsite

import (
	"slices"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/semver"
)

// Condition types the controller sets that the site reads.
const (
	// conditionRegressed is set by the tag policy on a version that scores
	// worse than the stable one.
	conditionRegressed = "Regressed"
	// conditionInconclusive is set on a version that scores lower than the
	// stable one, but not beyond noise.
	conditionInconclusive = "Inconclusive"
	// conditionRetried is set when a run was started again after an
	// infrastructure failure; its message is the failure of the earlier Job.
	conditionRetried = "Retried"
	// conditionNetworkPolicy mirrors controller.ConditionNetworkPolicy.
	conditionNetworkPolicy = "NetworkPolicy"
)

// Score is a score as the controller stores it, a string with two decimals,
// plus its numeric value when it parses.
type Score struct {
	// Text is the stored value, empty while the eval has no result.
	Text string
	// Value is the parsed value; meaningful only when OK.
	Value float64
	// OK reports whether Text parsed.
	OK bool
}

func newScore(text string) Score {
	v, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	return Score{Text: text, Value: v, OK: err == nil}
}

// ptr returns a pointer to the score's value, nil when it did not parse.
func (s Score) ptr() *float64 {
	if !s.OK {
		return nil
	}
	v := s.Value
	return &v
}

// Run is one SkillEval, flattened for the API and the tag policy wording.
type Run struct {
	// Name is the SkillEval name, the key of /runs/{name}.
	Name string
	// Skill, Version and Digest identify the evaluated skill version.
	Skill, Version, Digest string
	// Agent, AgentVersion and Model say what ran the eval. AgentVersion is the
	// release the spec pins, else the one the runner reported; empty when neither is known.
	Agent, AgentVersion, Model string
	// Required marks models that gate promotion.
	Required bool
	// Trigger says why the run exists: push, model-change or manual.
	Trigger string
	// PushedBy is the Harbor user who pushed the version, when the webhook said.
	PushedBy string
	// Phase is the lifecycle phase; a SkillEval without status counts as Pending.
	Phase string
	// Verdict is the stored verdict, empty before a result exists.
	Verdict string
	// With, Without and Lift are the stored scores.
	With, Without, Lift Score
	// ReportDigest is the report referrer of the current Job; empty when none was stored.
	ReportDigest string
	// Created is when the SkillEval was created, the start of the run.
	Created time.Time
	// Completed is when the run finished; zero while it has not.
	Completed time.Time
	// Regressed, Inconclusive and Retried are the messages of those conditions, empty when absent.
	Regressed, Inconclusive, Retried string
	// N is the number of trials behind the scores, with-skill/without-skill
	// (9/9); CI is the lift interval as the controller stores it
	// ([+0.41, +0.73]). Both are empty for a runner that sent no trial scores.
	N, CI string
	// CILower and CIUpper are the bounds of CI; zero when CI is empty.
	CILower, CIUpper float64
	// Confidence is the level of CI as text, for example 95%.
	Confidence string
	// Tier1, Tier2 and Tier3 are the tier statuses the runner reported;
	// empty when it did not (runs before the tiers existed).
	Tier1, Tier2, Tier3 string
	// Findings are the short strings naming the failing tier and check.
	Findings []string

	// se is the SkillEval the run was made from.
	se *v1alpha1.SkillEval
}

func newRun(se *v1alpha1.SkillEval, confidence string) Run {
	r := Run{
		N: se.Status.N, CI: se.Status.LiftCI, Confidence: confidence,
		Name: se.Name, Skill: se.Spec.Skill, Version: se.Spec.Version, Digest: se.Spec.Digest,
		Agent: se.Spec.Agent, AgentVersion: se.EffectiveAgentVersion(), Model: se.Spec.Model, Required: se.Spec.Required,
		Trigger: string(se.Spec.Trigger), PushedBy: se.Annotations[v1alpha1.AnnotationPushedBy],
		Phase: string(se.Status.Phase), Verdict: string(se.Status.Verdict),
		With: newScore(se.Status.WithSkill), Without: newScore(se.Status.WithoutSkill), Lift: newScore(se.Status.Lift),
		ReportDigest: se.Status.ReportDigest, Created: se.CreationTimestamp.UTC(), Findings: se.Status.Findings,
		se: se,
	}
	if t := se.Status.Tiers; t != nil {
		r.Tier1, r.Tier2, r.Tier3 = t.Tier1, t.Tier2, t.Tier3
	}
	if lower, upper, ok := parseCI(r.CI); ok {
		r.CILower, r.CIUpper = lower, upper
	} else {
		r.CI, r.N = "", ""
	}
	if r.Phase == "" {
		r.Phase = string(v1alpha1.PhasePending)
	}
	if se.Status.CompletedAt != nil {
		r.Completed = se.Status.CompletedAt.UTC()
	}
	for _, c := range se.Status.Conditions {
		if c.Status != metav1.ConditionTrue {
			continue
		}
		switch c.Type {
		case conditionRegressed:
			r.Regressed = c.Message
		case conditionInconclusive:
			r.Inconclusive = c.Message
		case conditionRetried:
			r.Retried = c.Message
		}
	}
	return r
}

// parseCI reads an interval as the controller stores it, "[+0.41, +0.73]".
func parseCI(text string) (lower, upper float64, ok bool) {
	lo, hi, found := strings.Cut(strings.Trim(text, "[]"), ", ")
	var err1, err2 error
	lower, err1 = strconv.ParseFloat(lo, 64)
	upper, err2 = strconv.ParseFloat(hi, 64)
	return lower, upper, found && err1 == nil && err2 == nil
}

// Harness is agent@version, or the agent alone while its version is unknown.
func (r Run) Harness() string {
	if r.AgentVersion == "" {
		return r.Agent
	}
	return r.Agent + "@" + r.AgentVersion
}

// Entry is the model list entry of the run, agent@version/model: the unit the
// tag policy compares and counts.
func (r Run) Entry() string {
	if r.Agent == "" {
		return r.Model
	}
	return r.Harness() + "/" + r.Model
}

// Evidence says how sure the lift is, for example "95% CI +0.41 to +0.73, n=9/9";
// empty for a run without trial scores.
func (r Run) Evidence() string {
	if r.CI == "" {
		return ""
	}
	lo, hi, _ := strings.Cut(strings.Trim(r.CI, "[]"), ", ")
	return r.Confidence + " CI " + lo + " to " + hi + ", n=" + r.N
}

// BeyondNoise reports whether the lift interval exists and lies above zero.
func (r Run) BeyondNoise() bool { return r.CI != "" && r.CILower > 0 }

// Finished reports whether the run reached Succeeded or Failed.
func (r Run) Finished() bool {
	return r.Phase == string(v1alpha1.PhaseSucceeded) || r.Phase == string(v1alpha1.PhaseFailed)
}

// Active reports whether the run is still waiting for or using a Job.
func (r Run) Active() bool {
	return r.Phase == string(v1alpha1.PhasePending) || r.Phase == string(v1alpha1.PhaseRunning)
}

// Rejected reports whether Tier 1 or Tier 2 failed for this run.
func (r Run) Rejected() bool {
	return r.Tier1 == v1alpha1.TierFailed || r.Tier2 == v1alpha1.TierFailed || r.Verdict == string(v1alpha1.VerdictRejected)
}

// RejectedBy names the tiers that failed, for example "Tier 1" or "Tier 1 and Tier 2".
func (r Run) RejectedBy() string {
	switch {
	case r.Tier1 == v1alpha1.TierFailed && r.Tier2 == v1alpha1.TierFailed:
		return "Tier 1 and Tier 2"
	case r.Tier1 == v1alpha1.TierFailed:
		return "Tier 1"
	case r.Tier2 == v1alpha1.TierFailed:
		return "Tier 2"
	default:
		return "Tier 1 or Tier 2"
	}
}

// RejectedText is the policy's sentence for a rejected run:
// "Rejected by Tier 1: <first finding>".
func (r Run) RejectedText() string {
	text := "Rejected by " + r.RejectedBy()
	if len(r.Findings) > 0 {
		text += ": " + r.Findings[0]
	}
	return text
}

// NoScores reports whether the run finished without scores, which is what
// happens when Tier 3 did not run.
func (r Run) NoScores() bool {
	return r.Phase == string(v1alpha1.PhaseSucceeded) && (!r.With.OK || !r.Without.OK)
}

// Tier3Skipped reports whether the live trials did not run: the runner said
// so, or a finished run was rejected without scores.
func (r Run) Tier3Skipped() bool {
	return r.Tier3 == v1alpha1.TierSkipped || (r.NoScores() && r.Rejected())
}

// noReportReason says why an attempt has no report, in a sentence.
func (r Run) noReportReason(current bool) string {
	switch {
	case !current:
		return "This attempt attached no report."
	case r.Phase == string(v1alpha1.PhaseFailed):
		return "No report stored: the run failed before one was attached."
	case r.Active():
		return "The report is attached when the run finishes."
	case r.Phase == string(v1alpha1.PhasePlanned):
		return "No report: a dry run creates no Job."
	default:
		return "No report stored for this run."
	}
}

// sortRuns orders runs newest first; equal times fall back to the name.
func sortRuns(runs []Run) {
	slices.SortFunc(runs, func(a, b Run) int {
		if c := b.Created.Compare(a.Created); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// newerFirst orders versions newest first.
func newerFirst(a, b string) int { return semver.Compare(b, a) }
