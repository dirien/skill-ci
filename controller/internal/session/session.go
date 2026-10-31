package session

import (
	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
)

// FocusCategories are the categories of the Focus preset of the log.
var FocusCategories = []string{
	apiv1.CategoryMsg, apiv1.CategoryUser, apiv1.CategoryThink, apiv1.CategoryTools,
	apiv1.CategoryWeb, apiv1.CategoryPkg, apiv1.CategorySkill, apiv1.CategoryNet, apiv1.CategoryJudge,
}

// Preview limits of the session view.
const (
	// OutputPreviewBytes and OutputPreviewLines cap a tool output preview.
	OutputPreviewBytes = 4 << 10
	OutputPreviewLines = 40
	// ThinkPreviewBytes caps a reasoning preview.
	ThinkPreviewBytes = 1 << 10
	// InputPreviewBytes caps the arguments preview of a tool call.
	InputPreviewBytes = 4 << 10
)

// DecodeIndex parses index.json and checks its schema and trial ids.
func DecodeIndex(data []byte) (*Index, error) {
	return nil, ErrNotImplemented
}

// DecodeTrial parses one decompressed trial document and checks its schema.
func DecodeTrial(data []byte) (*Trial, error) {
	return nil, ErrNotImplemented
}

// StepForPointer maps a reward.json evidence pointer into trajectory.json to an
// ATIF step_id: "/steps/3" and "/steps/3/tool_calls/0" are step 4, because the
// verifier counts steps from 0 and ATIF numbers them from 1.
func StepForPointer(pointer string) (int, error) {
	return 0, ErrNotImplemented
}

// Grid builds the trial grid of one run attempt from its bundle index.
func Grid(idx *Index, job, bundleDigest string) (apiv1.TrialGrid, error) {
	return apiv1.TrialGrid{}, ErrNotImplemented
}

// FallbackGrid builds a grid without links from SkillEvaluator's result.json
// (agents.<agent>.pass_at_k.<arm>.cases.<case>.attempts[]), for runs without a bundle.
func FallbackGrid(result []byte, agent, job string) (apiv1.TrialGrid, error) {
	return apiv1.TrialGrid{}, ErrNotImplemented
}

// Input is what Build needs to make the session view of one trial.
type Input struct {
	// Run names the run and the attempt.
	Run apiv1.RunRef
	// Trigger and PushedBy come from the SkillEval.
	Trigger, PushedBy string
	// Index is the bundle index; Trial the trial document.
	Index *Index
	Trial *Trial
	// Redactor applies render-time redaction to every string of the view; nil applies patterns only.
	Redactor *Redactor
	// OutputHref returns the site path of the full output of call k of a step.
	OutputHref func(step, k int) string
	// TrajectoryHref is the site path of the trajectory download.
	TrajectoryHref string
}

// Build makes the session view of one trial: header, metadata, log entries
// with categories and counts, criteria, findings, files, judge metrics and
// network flows.
func Build(in Input) (apiv1.TrialSession, error) {
	return apiv1.TrialSession{}, ErrNotImplemented
}

// CompareInput is what Compare needs.
type CompareInput struct {
	// Run names the run and the attempt.
	Run apiv1.RunRef
	// Index is the bundle index.
	Index *Index
	// Case is the eval case and Attempt the paired attempt.
	Case    string
	Attempt int
	// WithSkill and WithoutSkill are the trial documents; nil when the arm has none.
	WithSkill, WithoutSkill *Trial
	// Redactor applies render-time redaction.
	Redactor *Redactor
}

// Compare pairs attempt k of the with-skill arm with attempt k of the baseline for one case.
func Compare(in CompareInput) (apiv1.Compare, error) {
	return apiv1.Compare{}, ErrNotImplemented
}

// Output returns the full text of result k of a step's observation, redacted.
func Output(t *Trial, step, k int, r *Redactor) (string, error) {
	return "", ErrNotImplemented
}

// TrajectoryFile returns the trial's trajectory as an ATIF JSON file, redacted.
func TrajectoryFile(t *Trial, r *Redactor) ([]byte, error) {
	return nil, ErrNotImplemented
}

// MapNetwork turns a trial's network section into the API view and places
// every flow at the step that was running when it started.
func MapNetwork(n *Network, t *Trajectory) (*apiv1.TrialNetwork, error) {
	return nil, ErrNotImplemented
}

// Redactor replaces secrets in strings at render time: exact values (and
// their base64, URL-encoded and JSON-escaped forms) plus the patterns of the
// runner's redaction v1. It is the controller's second line of defense; the
// runner's redaction is the first.
type Redactor struct {
	values []string
}

// NewRedactor returns a Redactor for the given exact values; values shorter
// than 8 characters are ignored.
func NewRedactor(values []string) *Redactor {
	return &Redactor{values: values}
}

// String returns s with every secret replaced and the number of replacements.
// Until it is built it fails closed: it returns a fixed marker instead of s.
func (r *Redactor) String(s string) (string, int) {
	return "[redaction not implemented]", len(r.values) * 0
}
