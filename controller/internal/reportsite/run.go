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

// Condition types the controller sets that the pages read.
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
	// Agent, AgentVersion and Model say what ran the eval. AgentVersion is the
	// release the spec pins, else the one the runner reported; empty when neither is known.
	Agent, AgentVersion, Model string
	// RanVersion is the release the runner reported for the trials; empty
	// before the run finishes.
	RanVersion string
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
	// Inconclusive is the message of the Inconclusive condition, empty when absent.
	Inconclusive string
	// N is the number of trials behind the scores, with-skill/without-skill
	// (9/9); CI is the lift interval as the controller stores it
	// ([+0.41, +0.73]). Both are empty for a runner that sent no trial scores.
	N, CI string
	// CILower is the lower bound of CI; zero when CI is empty.
	CILower float64
	// Confidence is the level of CI as text, for example 95%.
	Confidence string
	// Retries is how often the controller started the run again after an infrastructure failure.
	Retries int
	// Retried is the message of the Retried condition: the failure that caused the last retry.
	Retried string
	// Tier1, Tier2 and Tier3 are the tier statuses the runner reported;
	// empty when it did not (runs before the tiers existed).
	Tier1, Tier2, Tier3 string
	// Publication is SkillEvaluator's publication assessment, empty when not reported.
	Publication string
	// Findings are the short strings naming the failing tier and check.
	Findings []string
	// Network is the network the trials got; see NetworkView.
	Network NetworkView
	// SandboxProfile is the SandboxProfile the run used; empty for a bare Sandbox per trial.
	SandboxProfile string
	// SpecProfile is the SandboxProfile the spec names; empty means the controller default.
	SpecProfile string
	// WarmPool is the SandboxWarmPool the trials were claimed from; empty for a bare Sandbox.
	WarmPool string
}

// NetworkView is the network of a run, for the run page.
type NetworkView struct {
	// Mode is none, allowlist or full; empty when the run predates network modes.
	Mode string
	// Source is skill or default.
	Source string
	// Declared are the hosts and @bundles the skill declared.
	Declared []string
	// Hosts are the hosts the trials could reach on TCP 443, bundles expanded.
	Hosts []string
	// Policy is the CiliumNetworkPolicy that enforced the mode, empty when none did.
	Policy string
	// Enforced reports whether a policy enforced the mode.
	Enforced bool
	// Reason and Message are those of the NetworkPolicy condition.
	Reason, Message string
}

// Legacy reports whether the run predates network modes: it ran with full and no policy.
func (n NetworkView) Legacy() bool { return n.Mode == "" }

// SourceText says where the mode comes from, in words.
func (n NetworkView) SourceText() string {
	switch n.Source {
	case v1alpha1.NetworkSourceSkill:
		return "declared by the skill"
	case v1alpha1.NetworkSourceDefault:
		return "platform default, the skill declares none"
	default:
		return ""
	}
}

// Reach says what the trials could reach, in words.
func (n NetworkView) Reach() string {
	switch n.Mode {
	case "", v1alpha1.NetworkFull:
		return "The internet, not the cluster."
	case v1alpha1.NetworkNone:
		return "Only the model API hosts and the judge."
	default:
		return "The model API hosts, the judge and the hosts the skill declares."
	}
}

func newRun(se *v1alpha1.SkillEval, confidence string) Run {
	r := Run{
		N: se.Status.N, CI: se.Status.LiftCI, Confidence: confidence,
		Name: se.Name, Skill: se.Spec.Skill, Version: se.Spec.Version, Digest: se.Spec.Digest,
		Agent: se.Spec.Agent, AgentVersion: se.EffectiveAgentVersion(), RanVersion: se.Status.AgentVersion, Model: se.Spec.Model, Required: se.Spec.Required, Attempts: se.Spec.Attempts,
		Trigger: string(se.Spec.Trigger), Phase: string(se.Status.Phase), Verdict: string(se.Status.Verdict),
		With: newScore(se.Status.WithSkill), Without: newScore(se.Status.WithoutSkill), Lift: newScore(se.Status.Lift),
		JobName: se.Status.JobName, Message: se.Status.Message, ReportDigest: se.Status.ReportDigest, Retries: se.Status.Retries,
		Created: se.CreationTimestamp.UTC(),
	}
	if t := se.Status.Tiers; t != nil {
		r.Tier1, r.Tier2, r.Tier3 = t.Tier1, t.Tier2, t.Tier3
	}
	if lower, _, ok := parseCI(r.CI); ok {
		r.CILower = lower
	} else {
		r.CI, r.N = "", ""
	}
	r.Publication, r.Findings = se.Status.Publication, se.Status.Findings
	r.SandboxProfile, r.SpecProfile, r.WarmPool = se.Status.SandboxProfile, se.Spec.SandboxProfile, se.Status.WarmPool
	if n := se.Spec.Network; n != nil {
		r.Network.Mode, r.Network.Source, r.Network.Declared = n.Mode, n.Source, n.Allow
	}
	if n := se.Status.Network; n != nil {
		if n.Mode != "" {
			r.Network.Mode = n.Mode
		}
		r.Network.Hosts, r.Network.Policy, r.Network.Enforced = n.Hosts, n.Policy, n.Enforced
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
		if c.Type == conditionInconclusive && c.Status == metav1.ConditionTrue {
			r.Inconclusive = c.Message
		}
		if c.Type == conditionRetried && c.Status == metav1.ConditionTrue {
			r.Retried = c.Message
		}
		if c.Type == conditionNetworkPolicy {
			r.Network.Reason, r.Network.Message = c.Reason, c.Message
		}
	}
	return r
}

func condition(c metav1.Condition) Condition {
	return Condition{Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message, Changed: c.LastTransitionTime.UTC()}
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

// VersionDiffers reports whether the runner saw a different agent release than
// the spec pins, which the run page points out.
func (r Run) VersionDiffers() bool { return r.RanVersion != "" && r.RanVersion != r.AgentVersion }

// Evidence says how sure the lift is, for example "95% CI +0.41 to +0.73, n=9/9";
// empty for a run without trial scores.
func (r Run) Evidence() string {
	if r.CI == "" {
		return ""
	}
	lo, hi, _ := strings.Cut(strings.Trim(r.CI, "[]"), ", ")
	return r.Confidence + " CI " + lo + " to " + hi + ", n=" + r.N
}

// LiftEvidence is the lift with its evidence, for example
// "+0.59 (95% CI +0.41 to +0.73, n=9/9)"; just the lift without trial scores.
func (r Run) LiftEvidence() string {
	if e := r.Evidence(); e != "" {
		return r.Lift.Text + " (" + e + ")"
	}
	return r.Lift.Text
}

// BeyondNoise reports whether the lift interval exists and lies above zero.
func (r Run) BeyondNoise() bool { return r.CI != "" && r.CILower > 0 }

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
	case v1alpha1.VerdictHurts, v1alpha1.VerdictRejected:
		return Status{"failed", r.Verdict}
	case v1alpha1.VerdictRegressed:
		return Status{"regressed", r.Verdict}
	case v1alpha1.VerdictNeutral, v1alpha1.VerdictObsolete, v1alpha1.VerdictInconclusive:
		return Status{"neutral", r.Verdict}
	default:
		return Status{}
	}
}

// tierInfo names the three tiers and says what each one covers.
var tierInfo = [3]struct{ Short, Name, Covers string }{
	{"T1", "Tier 1", "Static checks: schema, security, PII, license, code integrity, unicode, version, quality and lint. Runs before any live trial."},
	{"T2", "Tier 2", "Deduplication against the other skills in Harbor. Runs before any live trial."},
	{"T3", "Tier 3", "Live trials with and without the skill: the scores, lift and the six dimensions. Runs only when Tier 1 and Tier 2 pass."},
}

// tierShort says in a few words what a tier is, for tooltips.
var tierShort = [3]string{"static checks", "deduplication", "live trials"}

// TierMarker is one tier of a run, ready to render.
type TierMarker struct {
	// Short is the compact label, T1 to T3.
	Short string
	// Name is the full name, Tier 1 to Tier 3.
	Name string
	// Covers says what the tier checks.
	Covers string
	// Value is the status the runner reported; empty when unknown.
	Value string
	// Status is the marker kind and its capitalised text; empty when unknown.
	Status Status
	// Text is the accessible description, for example "Tier 1 (static checks): failed".
	Text string
}

// tierStatus maps a reported tier or publication value to a status. Passed
// is green, failed red, incomplete amber and skipped quiet; anything else is
// unknown and gives an empty status, which renders a dash.
func tierStatus(value string) Status {
	switch value {
	case v1alpha1.TierPassed:
		return Status{"pass", "Passed"}
	case v1alpha1.TierFailed:
		return Status{"failed", "Failed"}
	case v1alpha1.TierIncomplete:
		return Status{"regressed", "Incomplete"}
	case v1alpha1.TierSkipped:
		return Status{"neutral", "Skipped"}
	default:
		return Status{}
	}
}

// TierMarkers returns the three tiers of the run in order.
func (r Run) TierMarkers() []TierMarker {
	values := [3]string{r.Tier1, r.Tier2, r.Tier3}
	out := make([]TierMarker, len(values))
	for i, v := range values {
		st := tierStatus(v)
		text := tierInfo[i].Name + " (" + tierShort[i] + "): not reported"
		if st.Kind != "" {
			text = tierInfo[i].Name + " (" + tierShort[i] + "): " + strings.ToLower(st.Label)
		}
		out[i] = TierMarker{Short: tierInfo[i].Short, Name: tierInfo[i].Name, Covers: tierInfo[i].Covers, Value: v, Status: st, Text: text}
	}
	return out
}

// TiersKnown reports whether the runner reported any tier.
func (r Run) TiersKnown() bool { return r.Tier1 != "" || r.Tier2 != "" || r.Tier3 != "" }

// PublicationStatus maps SkillEvaluator's publication assessment to a status.
func (r Run) PublicationStatus() Status { return tierStatus(r.Publication) }

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

// FirstFinding returns the first finding, empty when there is none.
func (r Run) FirstFinding() string {
	if len(r.Findings) == 0 {
		return ""
	}
	return r.Findings[0]
}

// NoScores reports whether the run finished without scores, which is what
// happens when Tier 3 did not run.
func (r Run) NoScores() bool {
	return r.Phase == string(v1alpha1.PhaseSucceeded) && (!r.With.OK || !r.Without.OK)
}

// Tier3Note explains in a sentence why the live trials did not run; empty
// when they ran or the runner did not say.
func (r Run) Tier3Note() string {
	switch {
	case r.Tier3 != v1alpha1.TierSkipped && (!r.NoScores() || !r.Rejected()):
		return ""
	case r.Rejected():
		return "Tier 3 not run: " + r.RejectedBy() + " rejected this version, so no live trials were spent."
	default:
		return "Tier 3 not run: the runner skipped the live trials."
	}
}

// RetryNote says in a sentence that the run was started again after an
// infrastructure failure, and what failed; empty when it was not retried.
func (r Run) RetryNote() string {
	var note string
	switch {
	case r.Retries <= 0:
		return ""
	case r.Retries == 1:
		note = "Retried once after an infrastructure failure"
	default:
		note = "Retried " + strconv.Itoa(r.Retries) + " times after infrastructure failures; the last failure"
	}
	if r.Retried == "" {
		return note + "."
	}
	return note + ": " + r.Retried
}

// SandboxText says what the trials ran in, in words.
func (r Run) SandboxText() string {
	switch {
	case r.WarmPool != "":
		return "SandboxProfile " + r.SandboxProfile + ", trial pods claimed from warm pool " + r.WarmPool
	case r.SandboxProfile != "":
		return "SandboxProfile " + r.SandboxProfile + ", a bare Sandbox per trial (the Agent Sandbox extensions are not installed)"
	case r.Phase == string(v1alpha1.PhasePending) || r.Phase == string(v1alpha1.PhasePlanned):
		if r.SpecProfile != "" {
			return "SandboxProfile " + r.SpecProfile
		}
		return "the default SandboxProfile"
	default:
		return "a bare Sandbox per trial"
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
	case r.TiersKnown():
		return "No report stored for this run."
	default:
		return "No report stored for this run. Runs on runner versions before 0.3.0 did not attach one."
	}
}
