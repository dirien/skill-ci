package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

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

// StepForPointer maps a reward.json evidence pointer into trajectory.json to an
// ATIF step_id: "/steps/3" and "/steps/3/tool_calls/0" are step 4, because the
// verifier counts steps from 0 and ATIF numbers them from 1.
func StepForPointer(pointer string) (int, error) {
	rest, ok := strings.CutPrefix(strings.TrimPrefix(pointer, "#"), "/steps/")
	if !ok {
		return 0, fmt.Errorf("session: pointer %q does not point into the steps", pointer)
	}
	seg, _, _ := strings.Cut(rest, "/")
	n, err := strconv.Atoi(seg)
	if err != nil || n < 0 || seg != strconv.Itoa(n) {
		return 0, fmt.Errorf("session: pointer %q has no step index", pointer)
	}
	return n + 1, nil
}

// Grid builds the trial grid of one run attempt from its bundle index.
func Grid(idx *Index, job, bundleDigest string) (apiv1.TrialGrid, error) {
	if idx == nil {
		return apiv1.TrialGrid{}, fmt.Errorf("session: grid needs an index")
	}
	g := newGrid(apiv1.GridStored, job, idx.AttemptsPerCase)
	g.out.BundleDigest = bundleDigest
	g.out.Credentials = credentials(idx.Credentials)
	for _, c := range idx.Cases {
		g.addCase(c.ID, c.Question, c.ExpectedBehaviors)
	}
	for _, e := range idx.Trials {
		cell := indexCell(e)
		g.place(&cell)
	}
	for _, w := range idx.Withheld {
		cell := apiv1.TrialCell{ID: w.ID, Arm: w.Arm, Case: w.Case, Attempt: w.Attempt, Status: apiv1.StatusWithheld}
		g.place(&cell)
		g.out.Withheld = append(g.out.Withheld, apiv1.WithheldTrial{ID: w.ID, Arm: w.Arm, Case: w.Case, Attempt: w.Attempt, Reason: w.Reason})
	}
	if idx.Network.State != "" {
		ns := &apiv1.NetworkSummary{State: idx.Network.State, Reason: idx.Network.Reason}
		for _, e := range idx.Trials {
			if e.Network != nil {
				ns.Forwarded += e.Network.Forwarded
				ns.Dropped += e.Network.Dropped
			}
		}
		g.out.Network = ns
	}
	return g.finish(), nil
}

// FallbackGrid builds a grid without links from SkillEvaluator's result.json
// (agents.<agent>.pass_at_k.<arm>.cases.<case>.attempts[], plus
// agents.<agent>.trial_failures for unscored trials), for runs without a bundle.
// Case questions and behaviors are not in result.json, so they are empty.
func FallbackGrid(result []byte, agent, job string) (apiv1.TrialGrid, error) {
	var doc struct {
		Agents map[string]json.RawMessage `json:"agents"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		return apiv1.TrialGrid{}, fmt.Errorf("session: decode result.json: %w", err)
	}
	a, ok := doc.Agents[agent]
	if !ok {
		return apiv1.TrialGrid{}, fmt.Errorf("session: result.json has no agent %q", agent)
	}
	g := newGrid(apiv1.GridFallback, job, 0)
	g.out.Reason = "This run has no trial bundle: scores from result.json, no sessions."
	arms := []struct{ key, arm string }{{"with_skill", apiv1.ArmWithSkill}, {"without_skill", apiv1.ArmWithoutSkill}}
	seen := map[string]bool{}
	for _, arm := range arms {
		for _, cm := range objectMembers(dig(a, "pass_at_k", arm.key, "cases")) {
			g.addCase(cm.Key, "", nil)
			for i, at := range array(dig(cm.Val, "attempts")) {
				cell := apiv1.TrialCell{Arm: arm.arm, Case: cm.Key, Attempt: i + 1, ID: jsonString(dig(at, "trial"))}
				if n := jsonNumber(dig(at, "attempt")); n != nil && *n >= 1 && *n < 1<<16 {
					cell.Attempt = int(*n)
				}
				cell.Score = jsonNumber(dig(at, "score"))
				cell.Passed = jsonBool(dig(at, "passed"))
				cell.Status = apiv1.StatusScored
				if cell.Score == nil {
					cell.Status = apiv1.StatusUnscored
				}
				seen[arm.arm+"/"+cell.ID] = cell.ID != ""
				g.place(&cell)
			}
		}
	}
	for _, arm := range arms {
		for _, f := range array(dig(a, "trial_failures", arm.key)) {
			id := jsonString(dig(f, "trial"))
			if id == "" || seen[arm.arm+"/"+id] {
				continue
			}
			caseID, _, _ := strings.Cut(id, "__")
			g.addCase(caseID, "", nil)
			reason := jsonString(dig(f, "reason"))
			errType, _, _ := strings.Cut(reason, ":")
			cell := apiv1.TrialCell{ID: id, Arm: arm.arm, Case: caseID, Status: apiv1.StatusUnscored, ErrorType: strings.TrimSpace(errType)}
			cell.Attempt = g.nextAttempt(caseID, arm.arm)
			g.place(&cell)
		}
	}
	for _, c := range g.out.Cases {
		for _, list := range [][]*apiv1.TrialCell{c.WithSkill, c.WithoutSkill} {
			for _, cell := range list {
				if cell != nil {
					cell.HasSession = false
				}
			}
		}
	}
	return g.finish(), nil
}

// gridBuilder places cells into case rows.
type gridBuilder struct {
	out   apiv1.TrialGrid
	cases map[string]int
}

func newGrid(state, job string, attempts int) *gridBuilder {
	return &gridBuilder{
		out: apiv1.TrialGrid{
			State: state, Job: job, AttemptsPerCase: attempts, Cases: []apiv1.GridCase{},
			Credentials: []apiv1.Credential{}, Withheld: []apiv1.WithheldTrial{},
		},
		cases: map[string]int{},
	}
}

func (g *gridBuilder) addCase(id, question string, behaviors []string) int {
	if i, ok := g.cases[id]; ok {
		return i
	}
	if behaviors == nil {
		behaviors = []string{}
	}
	g.out.Cases = append(g.out.Cases, apiv1.GridCase{
		ID: id, Question: question, ExpectedBehaviors: behaviors,
		WithSkill: []*apiv1.TrialCell{}, WithoutSkill: []*apiv1.TrialCell{},
	})
	g.cases[id] = len(g.out.Cases) - 1
	return len(g.out.Cases) - 1
}

// place puts cell at its attempt in its case row; a taken or invalid slot goes to the next free one.
func (g *gridBuilder) place(cell *apiv1.TrialCell) {
	i := g.addCase(cell.Case, "", nil)
	row := &g.out.Cases[i].WithSkill
	if cell.Arm == apiv1.ArmWithoutSkill {
		row = &g.out.Cases[i].WithoutSkill
	}
	if cell.Attempt < 1 {
		cell.Attempt = g.nextAttempt(cell.Case, cell.Arm)
	}
	for len(*row) < cell.Attempt {
		*row = append(*row, nil)
	}
	if (*row)[cell.Attempt-1] != nil {
		cell.Attempt = g.nextAttempt(cell.Case, cell.Arm)
		for len(*row) < cell.Attempt {
			*row = append(*row, nil)
		}
	}
	(*row)[cell.Attempt-1] = cell
	if cell.Attempt > g.out.AttemptsPerCase {
		g.out.AttemptsPerCase = cell.Attempt
	}
}

func (g *gridBuilder) nextAttempt(caseID, arm string) int {
	i := g.addCase(caseID, "", nil)
	row := g.out.Cases[i].WithSkill
	if arm == apiv1.ArmWithoutSkill {
		row = g.out.Cases[i].WithoutSkill
	}
	for n, c := range row {
		if c == nil {
			return n + 1
		}
	}
	return len(row) + 1
}

// finish pads every row to AttemptsPerCase and sums the totals.
func (g *gridBuilder) finish() apiv1.TrialGrid {
	t := apiv1.TrialTotals{}
	for i := range g.out.Cases {
		c := &g.out.Cases[i]
		for _, row := range []*[]*apiv1.TrialCell{&c.WithSkill, &c.WithoutSkill} {
			for len(*row) < g.out.AttemptsPerCase {
				*row = append(*row, nil)
			}
			for _, cell := range *row {
				if cell == nil {
					continue
				}
				t.Trials++
				switch cell.Status {
				case apiv1.StatusScored:
					t.Scored++
				case apiv1.StatusWithheld:
					t.Withheld++
				default:
					t.Unscored++
				}
				t.TokensIn += deref(cell.TokensIn)
				t.TokensOut += deref(cell.TokensOut)
				t.TokensCached += deref(cell.TokensCached)
				if cell.CostUSD != nil {
					t.CostUSD += *cell.CostUSD
				}
				t.CostEstimated = t.CostEstimated || cell.CostEstimated
				if cell.AgentSec != nil {
					t.AgentSec += *cell.AgentSec
				}
				if cell.DurationSec != nil {
					t.DurationSec += *cell.DurationSec
				}
			}
		}
	}
	t.CostUSD = round(t.CostUSD, 6)
	t.AgentSec = round(t.AgentSec, 1)
	t.DurationSec = round(t.DurationSec, 1)
	g.out.Totals = t
	return g.out
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
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
	// OutputHref returns the site path of the full output of result k of a step.
	OutputHref func(step, k int) string
	// TrajectoryHref is the site path of the trajectory download.
	TrajectoryHref string
	// ArmMeans are the means of the trial's arm per metric key, for example
	// from the report's dimensions. A metric without one has no arm mean,
	// except in an arm with a single scored trial, where the mean is the trial's own score.
	ArmMeans map[string]float64
	// Suggestions are the run-level suggestions of the report (suggestions_v2);
	// each becomes an opportunity finding without step links.
	Suggestions []string
}

// CompareInput is what Compare needs.
type CompareInput struct {
	// Run names the run and the attempt.
	Run apiv1.RunRef
	// Trigger and PushedBy come from the SkillEval.
	Trigger, PushedBy string
	// Index is the bundle index.
	Index *Index
	// Case is the eval case and Attempt the paired attempt.
	Case    string
	Attempt int
	// WithSkill and WithoutSkill are the trial documents; nil when the arm has none.
	WithSkill, WithoutSkill *Trial
	// Redactor applies render-time redaction.
	Redactor *Redactor
	// OutputHref returns the site path of the full output of result k of a step of a trial.
	OutputHref func(trial string, step, k int) string
	// ArmMeans are the per-metric means of each arm (keyed by arm), as in Input.
	ArmMeans map[string]map[string]float64
}

// Compare pairs attempt k of the with-skill arm with attempt k of the baseline for one case.
func Compare(in CompareInput) (apiv1.Compare, error) {
	if in.Index == nil {
		return apiv1.Compare{}, fmt.Errorf("session: compare needs an index")
	}
	cse := findCase(in.Index, in.Case)
	if cse == nil {
		return apiv1.Compare{}, fmt.Errorf("session: case %q: %w", in.Case, ErrNotFound)
	}
	out := apiv1.Compare{
		Run:             in.Run,
		Attempt:         in.Attempt,
		AttemptsPerCase: in.Index.AttemptsPerCase,
	}
	render := 0
	str := func(s string) string {
		r, n := in.Redactor.String(s)
		render += n
		return r
	}
	out.Case = apiv1.CaseInfo{ID: cse.ID, Question: str(cse.Question), ExpectedBehaviors: make([]string, 0, len(cse.ExpectedBehaviors))}
	for _, b := range cse.ExpectedBehaviors {
		out.Case.ExpectedBehaviors = append(out.Case.ExpectedBehaviors, str(b))
	}
	var err error
	if out.WithSkill, err = compareSide(in, apiv1.ArmWithSkill, in.WithSkill); err != nil {
		return apiv1.Compare{}, err
	}
	if out.WithoutSkill, err = compareSide(in, apiv1.ArmWithoutSkill, in.WithoutSkill); err != nil {
		return apiv1.Compare{}, err
	}
	if err := checkLeak(in.Redactor, out); err != nil {
		return apiv1.Compare{}, err
	}
	return out, nil
}

func compareSide(in CompareInput, arm string, doc *Trial) (apiv1.CompareSide, error) {
	side := apiv1.CompareSide{Log: []apiv1.LogEntry{}, Files: []apiv1.FileChange{}}
	if doc == nil {
		for _, w := range in.Index.Withheld {
			if w.Arm == arm && w.Case == in.Case && w.Attempt == in.Attempt {
				side.Trial = &apiv1.TrialCell{ID: w.ID, Arm: w.Arm, Case: w.Case, Attempt: w.Attempt, Status: apiv1.StatusWithheld}
				side.Missing = fmt.Sprintf("The %s trial of attempt %d was withheld (%s).", arm, in.Attempt, w.Reason)
				return side, nil
			}
		}
		for _, e := range in.Index.Trials {
			if e.Arm == arm && e.Case == in.Case && e.Attempt == in.Attempt {
				cell := indexCell(e)
				side.Trial = &cell
				side.Missing = fmt.Sprintf("The %s trial document of attempt %d could not be read.", arm, in.Attempt)
				return side, nil
			}
		}
		side.Missing = fmt.Sprintf("No %s trial for attempt %d in the bundle.", arm, in.Attempt)
		return side, nil
	}
	var href func(step, k int) string
	if in.OutputHref != nil {
		id := doc.ID
		href = func(step, k int) string { return in.OutputHref(id, step, k) }
	}
	view, err := Build(Input{
		Run: in.Run, Trigger: in.Trigger, PushedBy: in.PushedBy, Index: in.Index, Trial: doc,
		Redactor: in.Redactor, OutputHref: href, ArmMeans: in.ArmMeans[arm],
	})
	if err != nil {
		return apiv1.CompareSide{}, err
	}
	side.Trial = &view.Trial
	side.Header = &view.Header
	side.Metadata = &view.Metadata
	side.Criteria = &view.Criteria
	side.Files = view.Files
	for _, e := range view.Log.Entries {
		if slices.Contains(FocusCategories, e.Category) {
			side.Log = append(side.Log, e)
		}
	}
	return side, nil
}

// findStep returns the step with the given step_id.
func findStep(t *Trial, step int) (int, *Step, error) {
	if t == nil || t.Trajectory == nil {
		return 0, nil, fmt.Errorf("session: trial has no trajectory: %w", ErrNotFound)
	}
	for i := range t.Trajectory.Steps {
		if stepID(i, t.Trajectory.Steps[i]) == step {
			return i, &t.Trajectory.Steps[i], nil
		}
	}
	return 0, nil, fmt.Errorf("session: step %d: %w", step, ErrNotFound)
}

// Output returns the full text of result k of a step's observation, redacted.
func Output(t *Trial, step, k int, r *Redactor) (string, error) {
	_, s, err := findStep(t, step)
	if err != nil {
		return "", err
	}
	if s.Observation == nil || k < 0 || k >= len(s.Observation.Results) {
		return "", fmt.Errorf("session: step %d has no result %d: %w", step, k, ErrNotFound)
	}
	text, _ := r.String(textOf(s.Observation.Results[k].Content))
	if r.Leaks([]byte(text)) {
		return "", ErrLeak
	}
	return text, nil
}

// TrajectoryFile returns the trial's trajectory as an ATIF JSON file, redacted.
// It keeps the fields of the stored trajectory that the Trajectory type leaves out.
func TrajectoryFile(t *Trial, r *Redactor) ([]byte, error) {
	if t == nil || t.Trajectory == nil {
		return nil, fmt.Errorf("session: trial has no trajectory: %w", ErrNotFound)
	}
	raw := t.rawTrajectory
	if len(raw) == 0 {
		var err error
		if raw, err = json.Marshal(t.Trajectory); err != nil {
			return nil, fmt.Errorf("session: encode trajectory: %w", err)
		}
	}
	red, _, err := r.redactJSON(raw)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, red, "", "  "); err != nil {
		return nil, fmt.Errorf("session: indent trajectory: %w", err)
	}
	buf.WriteByte('\n')
	if r.Leaks(buf.Bytes()) {
		return nil, ErrLeak
	}
	return buf.Bytes(), nil
}

// MapNetwork turns a trial's network section into the API view and places
// every flow at the step that was running when it started: the last step whose
// timestamp is at or before the flow's first time (0 before the first step).
func MapNetwork(n *Network, t *Trajectory) (*apiv1.TrialNetwork, error) {
	if n == nil {
		return nil, nil
	}
	out := &apiv1.TrialNetwork{
		State: n.State, Reason: n.Reason, From: utc(n.From), To: utc(n.To),
		DNS: make([]apiv1.DNSRow, 0, len(n.DNS)), Flows: make([]apiv1.FlowRow, 0, len(n.Flows)),
		Truncated: n.Truncated, Lost: n.Lost,
	}
	for _, d := range n.DNS {
		out.DNS = append(out.DNS, apiv1.DNSRow{Name: d.Name, Count: d.Count, FirstAt: d.FirstAt.UTC(), LastAt: d.LastAt.UTC(), Rcode: d.Rcode})
	}
	type stepTime struct {
		id int
		at time.Time
	}
	var times []stepTime
	if t != nil {
		for i, s := range t.Steps {
			if at, ok := parseTime(s.Timestamp); ok {
				times = append(times, stepTime{stepID(i, s), at})
			}
		}
		sort.SliceStable(times, func(i, j int) bool { return times[i].at.Before(times[j].at) })
	}
	for _, f := range n.Flows {
		row := apiv1.FlowRow{
			Verdict: f.Verdict, Destination: f.Destination, IP: f.IP, Port: f.Port, Protocol: f.Protocol,
			Count: f.Count, FirstAt: f.FirstAt.UTC(), LastAt: f.LastAt.UTC(), Reason: f.Reason,
		}
		for _, st := range times {
			if st.at.After(row.FirstAt) {
				break
			}
			row.Step = st.id
		}
		out.Flows = append(out.Flows, row)
	}
	return out, nil
}
