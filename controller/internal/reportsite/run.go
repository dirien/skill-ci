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

// conditionRegressed is the SkillEval condition the tag policy sets on a
// version that scores worse than the stable one.
const conditionRegressed = "Regressed"

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

// Condition is one status condition of a SkillEval.
type Condition struct {
	// Type, Status, Reason and Message are copied from the SkillEval.
	Type, Status, Reason, Message string
	// Changed is when the condition last changed.
	Changed time.Time
}

// Run is one SkillEval, flattened for the pages.
type Run struct {
	// Name is the SkillEval name, the key of /runs/{name}.
	Name string
	// Skill, Version and Digest identify the evaluated skill version.
	Skill, Version, Digest string
	// Agent and Model say what ran the eval.
	Agent, Model string
	// Required marks models that gate promotion.
	Required bool
	// Attempts is the number of trials per arm.
	Attempts int
	// Trigger says why the run exists: push, model-change or manual.
	Trigger string
	// Phase is the lifecycle phase; a SkillEval without status counts as Pending.
	Phase string
	// Verdict is the stored verdict, empty before a result exists.
	Verdict string
	// With, Without and Lift are the stored scores.
	With, Without, Lift Score
	// JobName is the runner Job.
	JobName string
	// Message is the failure reason or other detail.
	Message string
	// ReportDigest is the report referrer in the registry; empty when none was stored.
	ReportDigest string
	// Created is when the SkillEval was created, the start of the run.
	Created time.Time
	// Completed is when the run finished; zero while it has not.
	Completed time.Time
	// Conditions are the status conditions.
	Conditions []Condition
	// Regressed is the message of the Regressed condition, empty when absent.
	Regressed string
}

func newRun(se *v1alpha1.SkillEval) Run {
	r := Run{
		Name: se.Name, Skill: se.Spec.Skill, Version: se.Spec.Version, Digest: se.Spec.Digest,
		Agent: se.Spec.Agent, Model: se.Spec.Model, Required: se.Spec.Required, Attempts: se.Spec.Attempts,
		Trigger: string(se.Spec.Trigger), Phase: string(se.Status.Phase), Verdict: string(se.Status.Verdict),
		With: newScore(se.Status.WithSkill), Without: newScore(se.Status.WithoutSkill), Lift: newScore(se.Status.Lift),
		JobName: se.Status.JobName, Message: se.Status.Message, ReportDigest: se.Status.ReportDigest,
		Created: se.CreationTimestamp.UTC(),
	}
	if r.Phase == "" {
		r.Phase = string(v1alpha1.PhasePending)
	}
	if se.Status.CompletedAt != nil {
		r.Completed = se.Status.CompletedAt.UTC()
	}
	for _, c := range se.Status.Conditions {
		r.Conditions = append(r.Conditions, condition(c))
		if c.Type == conditionRegressed && c.Status == metav1.ConditionTrue {
			r.Regressed = c.Message
		}
	}
	return r
}

func condition(c metav1.Condition) Condition {
	return Condition{Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message, Changed: c.LastTransitionTime.UTC()}
}

// Duration is the time from creation to completion, zero while the run is open.
func (r Run) Duration() time.Duration {
	if r.Completed.IsZero() || r.Completed.Before(r.Created) {
		return 0
	}
	return r.Completed.Sub(r.Created)
}

// Finished reports whether the run reached Succeeded or Failed.
func (r Run) Finished() bool {
	return r.Phase == string(v1alpha1.PhaseSucceeded) || r.Phase == string(v1alpha1.PhaseFailed)
}

// Active reports whether the run is still waiting for or using a Job.
func (r Run) Active() bool {
	return r.Phase == string(v1alpha1.PhasePending) || r.Phase == string(v1alpha1.PhaseRunning)
}

// Status is a label with the semantic kind that colors it.
type Status struct {
	// Kind is one of pass, done, neutral, regressed, failed or active; empty renders a dash.
	Kind string
	// Label is the text shown next to the marker.
	Label string
}

// PhaseStatus maps the phase to a status. A finished run is quiet, so the
// verdict column carries the color; only a failed run is colored.
func (r Run) PhaseStatus() Status {
	switch v1alpha1.Phase(r.Phase) {
	case v1alpha1.PhaseFailed:
		return Status{"failed", r.Phase}
	case v1alpha1.PhaseRunning:
		return Status{"active", r.Phase}
	case v1alpha1.PhaseSucceeded:
		return Status{"done", r.Phase}
	default:
		return Status{"neutral", r.Phase}
	}
}

// VerdictStatus maps the verdict to a status; no verdict yet gives an empty status.
func (r Run) VerdictStatus() Status {
	switch v1alpha1.Verdict(r.Verdict) {
	case v1alpha1.VerdictHelps:
		return Status{"pass", r.Verdict}
	case v1alpha1.VerdictHurts:
		return Status{"failed", r.Verdict}
	case v1alpha1.VerdictRegressed:
		return Status{"regressed", r.Verdict}
	case v1alpha1.VerdictNeutral, v1alpha1.VerdictObsolete:
		return Status{"neutral", r.Verdict}
	default:
		return Status{}
	}
}

// HasReport reports whether the runner stored a report for this run.
func (r Run) HasReport() bool { return r.ReportDigest != "" }

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

// NoReportReason says why no report is stored, in a sentence.
func (r Run) NoReportReason() string {
	switch {
	case r.Phase == string(v1alpha1.PhaseFailed):
		return "No report stored: the run failed before one was attached."
	case r.Active():
		return "The report is attached when the run finishes."
	case r.Phase == string(v1alpha1.PhasePlanned):
		return "No report: a dry run creates no Job."
	default:
		return "No report stored for this run. Runs on runner versions before 0.3.0 did not attach one."
	}
}
