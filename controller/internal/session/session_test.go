package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
)

// uiFixtures is where the UI keeps its hand-derived API fixtures.
const uiFixtures = "../../../ui/src/api/fixtures"

func loadIndex(t *testing.T, bundle string) *Index {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdata, bundle, IndexFile))
	if err != nil {
		t.Fatal(err)
	}
	idx, err := DecodeIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func loadTrial(t *testing.T, bundle, id string) *Trial {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdata, bundle, "trial-"+id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := DecodeTrial(data)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func outputHref(run, trial string) func(step, k int) string {
	return func(step, k int) string {
		return fmt.Sprintf("/api/v1/runs/%s/trials/%s/steps/%d/output/%d", run, trial, step, k)
	}
}

// build makes the session of one golden trial with the run and hrefs the UI fixtures use.
func build(t *testing.T, bundle, id string, run apiv1.RunRef, r *Redactor) apiv1.TrialSession {
	t.Helper()
	view, err := Build(Input{
		Run: run, Trigger: "push", PushedBy: "release-bot",
		Index: loadIndex(t, bundle), Trial: loadTrial(t, bundle, id), Redactor: r,
		OutputHref:     outputHref(run.Name, id),
		TrajectoryHref: "/api/v1/runs/" + run.Name + "/trials/" + id + "/trajectory.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

const (
	bundleClaude   = "trials/claude-code-doks/bundle"
	bundleUnscored = "trials/claude-code-unscored/bundle"
	bundleCodex    = "trials/codex-synthetic/bundle"
	bundleOpencode = "trials/opencode-synthetic/bundle"
	bundleCanary   = "trials-secrets/canary/bundle-unredacted"
)

func entry(t *testing.T, view apiv1.TrialSession, id string) apiv1.LogEntry {
	t.Helper()
	for _, e := range view.Log.Entries {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("no log entry %s", id)
	return apiv1.LogEntry{}
}

func entryIndex(view apiv1.TrialSession, id string) int {
	return slices.IndexFunc(view.Log.Entries, func(e apiv1.LogEntry) bool { return e.ID == id })
}

func TestStepForPointer(t *testing.T) {
	tests := []struct {
		pointer string
		want    int
		wantErr bool
	}{
		{"/steps/0", 1, false},
		{"/steps/3", 4, false},
		{"/steps/3/tool_calls/0", 4, false},
		{"/steps/12/observation/results/1", 13, false},
		{"#/steps/2", 3, false},
		{"/ground_truth", 0, true},
		{"/expected_behavior/0", 0, true},
		{"/steps/", 0, true},
		{"/steps/x", 0, true},
		{"/steps/-1", 0, true},
		{"/steps/03", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := StepForPointer(tt.pointer)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("StepForPointer(%q) = %d, %v; want %d, error %v", tt.pointer, got, err, tt.want, tt.wantErr)
		}
	}
}

// TestMatchesUIFixtures builds every golden trial and compare view and checks
// it against the hand-derived UI fixtures. The ignored paths are the places
// where the fixtures are wrong (see the comments); everything else must match.
func TestMatchesUIFixtures(t *testing.T) {
	// Judge entries of a metric that passed (or has no pass field) but scored
	// below 0.8 say "below 0.8"; the fixtures call them "failed", which
	// contradicts passed: true in the Judge tab of the same session.
	low := func(prefix string, entries ...int) []string {
		var out []string
		for _, n := range entries {
			out = append(out, fmt.Sprintf("%s/%d/title", prefix, n), fmt.Sprintf("%s/%d/label", prefix, n))
		}
		return out
	}
	tests := []struct {
		fixture, bundle, trial string
		ignore                 []string
	}{
		{"trial-claude-code-with.json", bundleClaude, "rn-basic__sryqofs", low("/log/entries", 15)},
		{"trial-claude-code-without.json", bundleClaude, "rn-basic__BQbpoR9", low("/log/entries", 10, 11, 12)},
		{"trial-codex.json", bundleCodex, "rn-basic__Cx7aQ1p", low("/log/entries", 18)},
		// The fixture leaves out the friction finding of the error_recovery correction.
		{"trial-opencode.json", bundleOpencode, "rn-basic__Op4mZ8e", []string{"/findings"}},
		// The case names the expected skill even when the trial was not scored.
		{"trial-unscored.json", bundleUnscored, "rn-basic__CbK6FQ2", []string{"/metadata/skill/expected"}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			var want apiv1.TrialSession
			readFixture(t, tt.fixture, &want)
			got := build(t, tt.bundle, tt.trial, want.Run, nil)
			compareJSON(t, want, got, tt.ignore)
		})
	}

	compares := []struct {
		fixture, bundle, with, without string
		ignore                         []string
	}{
		{"compare-claude-code.json", bundleClaude, "rn-basic__sryqofs", "rn-basic__BQbpoR9",
			append(low("/withSkill/log", 9), low("/withoutSkill/log", 4, 5, 6)...)},
		// The codex index withholds the without-skill trial (secret-scan), so the
		// side says so and carries the withheld cell; the fixture says "no trial".
		{"compare-codex-missing.json", bundleCodex, "rn-basic__Cx7aQ1p", "",
			append(low("/withSkill/log", 14), "/withoutSkill/missing", "/withoutSkill/trial")},
	}
	for _, tt := range compares {
		t.Run(tt.fixture, func(t *testing.T) {
			var want apiv1.Compare
			readFixture(t, tt.fixture, &want)
			in := CompareInput{
				Run: want.Run, Trigger: "push", PushedBy: "release-bot", Index: loadIndex(t, tt.bundle),
				Case: "rn-basic", Attempt: 1, WithSkill: loadTrial(t, tt.bundle, tt.with),
				OutputHref: func(trial string, step, k int) string { return outputHref(want.Run.Name, trial)(step, k) },
			}
			if tt.without != "" {
				in.WithoutSkill = loadTrial(t, tt.bundle, tt.without)
			}
			got, err := Compare(in)
			if err != nil {
				t.Fatal(err)
			}
			compareJSON(t, want, got, tt.ignore)
		})
	}

	grids := []struct{ fixture, bundle string }{
		{"run-claude-code.json", bundleClaude},
		{"run-codex.json", bundleCodex},
		{"run-unscored.json", bundleUnscored},
	}
	for _, tt := range grids {
		t.Run(tt.fixture, func(t *testing.T) {
			var want apiv1.RunDetail
			readFixture(t, tt.fixture, &want)
			got, err := Grid(loadIndex(t, tt.bundle), want.Trials.Job, want.Trials.BundleDigest)
			if err != nil {
				t.Fatal(err)
			}
			compareJSON(t, want.Trials, got, nil)
		})
	}
}

func readFixture(t *testing.T, name string, v any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(uiFixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

// compareJSON compares want and got as JSON, skipping paths with an ignored prefix.
func compareJSON(t *testing.T, want, got any, ignore []string) {
	t.Helper()
	var w, g any
	for _, p := range []struct {
		in  any
		out *any
	}{{want, &w}, {got, &g}} {
		data, err := json.Marshal(p.in)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, p.out); err != nil {
			t.Fatal(err)
		}
	}
	var diffs []string
	jsonDiff("", w, g, ignore, &diffs)
	for _, d := range diffs {
		t.Error(d)
	}
}

func jsonDiff(path string, want, got any, ignore []string, out *[]string) {
	for _, p := range ignore {
		if path == p || strings.HasPrefix(path, p+"/") {
			return
		}
	}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*out = append(*out, path+": not an object")
			return
		}
		for k := range w {
			if _, ok := g[k]; !ok {
				*out = append(*out, fmt.Sprintf("%s/%s: missing, fixture has %v", path, k, w[k]))
			}
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				jsonDiff(path+"/"+k, nil, g[k], ignore, out)
				continue
			}
			jsonDiff(path+"/"+k, w[k], g[k], ignore, out)
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			*out = append(*out, fmt.Sprintf("%s: list differs: fixture %d items, built %v items", path, len(w), lenOf(got)))
			return
		}
		for i := range w {
			jsonDiff(fmt.Sprintf("%s/%d", path, i), w[i], g[i], ignore, out)
		}
	case float64:
		if g, ok := got.(float64); !ok || math.Abs(g-w) > 1e-9 {
			*out = append(*out, fmt.Sprintf("%s: fixture %v, built %v", path, w, got))
		}
	default:
		if !reflect.DeepEqual(want, got) {
			*out = append(*out, fmt.Sprintf("%s: fixture %v, built %v", path, want, got))
		}
	}
}

func lenOf(v any) int {
	if l, ok := v.([]any); ok {
		return len(l)
	}
	return -1
}

func TestBuildClaudeCode(t *testing.T) {
	run := apiv1.RunRef{Name: "acme"}
	with := build(t, bundleClaude, "rn-basic__sryqofs", run, nil)
	without := build(t, bundleClaude, "rn-basic__BQbpoR9", run, nil)

	// /steps/3 of the evidence is ATIF step 4, the Write, with the anchor s-4.
	if e := entry(t, with, "s-4"); e.Step != 4 || e.Tool == nil || e.Tool.Name != "Write" {
		t.Errorf("s-4 = %+v, want the Write of step 4", e)
	}
	if !slices.Contains(with.Criteria.BehaviorSteps, 4) {
		t.Errorf("behavior steps %v, want step 4 (from /steps/3)", with.Criteria.BehaviorSteps)
	}
	tests := []struct {
		name          string
		view          apiv1.TrialSession
		passed, total int
		skill         string
	}{
		{"with skill", with, 4, 4, apiv1.SkillInvoked},
		{"without skill", without, 1, 4, apiv1.SkillNotUsed},
	}
	for _, tt := range tests {
		if tt.view.Criteria.Passed != tt.passed || tt.view.Criteria.Total != tt.total {
			t.Errorf("%s: criteria %d of %d, want %d of %d", tt.name, tt.view.Criteria.Passed, tt.view.Criteria.Total, tt.passed, tt.total)
		}
		if tt.view.Metadata.Criteria != (apiv1.Ratio{Passed: tt.passed, Total: tt.total}) {
			t.Errorf("%s: metadata criteria %+v", tt.name, tt.view.Metadata.Criteria)
		}
		if tt.view.Metadata.Skill.State != tt.skill {
			t.Errorf("%s: skill state %q, want %q", tt.name, tt.view.Metadata.Skill.State, tt.skill)
		}
	}
	if len(with.Files) != 1 || with.Files[0].Op != apiv1.FileWrite || with.Files[0].Path != "/workspace/RELEASE_NOTES.md" ||
		!strings.HasPrefix(with.Files[0].Content, "ACME-RN/2 1.4.0\n") || with.Files[0].EntryID != "s-4" {
		t.Errorf("files = %+v, want the Write of RELEASE_NOTES.md", with.Files)
	}
	if e := entry(t, with, "s-2"); e.Category != apiv1.CategorySkill || with.Log.Jumps.Skill != "s-2" {
		t.Errorf("s-2 = %s, jumps %+v; want the Skill call as the skill jump", e.Category, with.Log.Jumps)
	}
	if e := entry(t, with, "s-3"); e.Category != apiv1.CategorySkill || e.Source != "user" {
		t.Errorf("s-3 = %s from %s, want the skill base directory turn", e.Category, e.Source)
	}
	if with.Metadata.CostUSD == nil || with.Metadata.CostEstimated {
		t.Errorf("cost %v estimated %v, want the reported total", with.Metadata.CostUSD, with.Metadata.CostEstimated)
	}
	if u := entry(t, with, "s-2-2").Usage; u == nil || !u.Estimated {
		t.Errorf("usage of step 2 = %+v, want a LiteLLM estimate", u)
	}
}

func TestBuildCodex(t *testing.T) {
	view := build(t, bundleCodex, "rn-basic__Cx7aQ1p", apiv1.RunRef{Name: "codex"}, nil)

	pip := entry(t, view, "s-3-2")
	if pip.Category != apiv1.CategoryPkg || !pip.Error || pip.Tool == nil || pip.Tool.Command != "pip install markdown-it-py" ||
		pip.Tool.Output == nil || !pip.Tool.Output.Error {
		t.Errorf("pip install entry = %+v, want a failed pkg entry", pip)
	}
	if view.Log.Jumps.FirstError != "s-3-2" {
		t.Errorf("first error jump %q, want s-3-2", view.Log.Jumps.FirstError)
	}
	if len(view.Files) != 1 || view.Files[0].Op != apiv1.FilePatch || view.Files[0].Path != "RELEASE_NOTES.md" ||
		!strings.Contains(view.Files[0].Patch, "*** Add File: RELEASE_NOTES.md") {
		t.Errorf("files = %+v, want the apply_patch of RELEASE_NOTES.md", view.Files)
	}
	if view.Network == nil {
		t.Fatal("no network section")
	}
	var dropped *apiv1.FlowRow
	for i, f := range view.Network.Flows {
		if f.Verdict == "DROPPED" {
			dropped = &view.Network.Flows[i]
		}
	}
	if dropped == nil || dropped.Destination != "pypi.org" || dropped.Step != 3 || dropped.Reason != "POLICY_DENIED" {
		t.Fatalf("dropped flow = %+v, want pypi.org placed at step 3", dropped)
	}
	n2 := entryIndex(view, "n-2")
	if n2 < 0 || n2 != entryIndex(view, "s-3-3")+1 || !view.Log.Entries[n2].Error {
		t.Errorf("n-2 at %d, want right after the entries of step 3 and marked as an error", n2)
	}
	if !slices.ContainsFunc(view.Findings, func(f apiv1.Finding) bool {
		return f.Rule == "flow-dropped" && slices.Equal(f.Steps, []int{3})
	}) {
		t.Errorf("findings %+v, want a flow-dropped finding at step 3", view.Findings)
	}
	if e := entry(t, view, "s-2"); e.Category != apiv1.CategoryThink {
		t.Errorf("s-2 = %s, want the reasoning first", e.Category)
	}
	if e := entry(t, view, "s-2-2"); e.Category != apiv1.CategorySkill {
		t.Errorf("s-2-2 = %s, want the SKILL.md cat as a skill entry", e.Category)
	}
}

func TestBuildOpencode(t *testing.T) {
	tr := loadTrial(t, bundleOpencode, "rn-basic__Op4mZ8e")
	view := build(t, bundleOpencode, "rn-basic__Op4mZ8e", apiv1.RunRef{Name: "oc"}, nil)

	if len(view.Files) != 2 || view.Files[0].Op != apiv1.FileWrite || view.Files[1].Op != apiv1.FileEdit ||
		view.Files[1].Path != "/workspace/RELEASE_NOTES.md" || len(view.Files[1].Hunks) != 1 ||
		view.Files[1].Hunks[0].New != "- Fixed a login timeout [ACME-117]" {
		t.Errorf("files = %+v, want the write then the edit", view.Files)
	}
	out := entry(t, view, "s-5").Tool.Output
	if out == nil || !out.Shortened || !out.Truncated || out.Lines < OutputPreviewLines || strings.Count(out.Preview, "\n") >= OutputPreviewLines {
		t.Errorf("bash output = %+v, want a shortened, truncated preview of 40 lines", out)
	}
	if len(view.Truncations) != 1 || view.Truncations[0].Pointer != "/trajectory/steps/4/observation/results/0/content" {
		t.Errorf("truncations = %+v", view.Truncations)
	}
	full, err := Output(tr, 5, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full, "...[skill-ci: ") || len(full) != out.Bytes {
		t.Errorf("full output has %d bytes (preview says %d) and marker %v", len(full), out.Bytes, strings.Contains(full, "...[skill-ci: "))
	}
	if !slices.ContainsFunc(view.Findings, func(f apiv1.Finding) bool { return f.Rule == "correction" && slices.Equal(f.Steps, []int{4}) }) {
		t.Errorf("findings %+v, want the correction at step 4", view.Findings)
	}
	if view.Network == nil || view.Network.State != apiv1.NetworkUnavailable {
		t.Errorf("network = %+v, want unavailable", view.Network)
	}
	if view.Metadata.Workspace != "" {
		t.Errorf("workspace %q, want none (the trajectory has no cwds)", view.Metadata.Workspace)
	}
}

func TestBuildUnscored(t *testing.T) {
	for _, id := range []string{"rn-basic__CbK6FQ2", "rn-basic__59JHTSp"} {
		tr := loadTrial(t, bundleUnscored, id)
		view := build(t, bundleUnscored, id, apiv1.RunRef{Name: "u"}, nil)
		if len(view.Log.Entries) != 0 || view.Header.Error == nil || view.Header.Error.Type != "RuntimeError" ||
			view.Header.Error.Traceback == "" || view.Downloads.Trajectory != "" || len(view.Judge) != 0 {
			t.Errorf("%s: session = %+v, want no log, the exception and no trajectory download", id, view.Header)
		}
		if view.Metadata.Skill.State != apiv1.SkillUnknown || view.Criteria.Total != 4 || view.Criteria.Passed != 0 {
			t.Errorf("%s: skill %+v criteria %+v", id, view.Metadata.Skill, view.Criteria)
		}
		if !slices.ContainsFunc(view.Findings, func(f apiv1.Finding) bool { return f.Rule == "exception" && len(f.Detail) <= 300 }) {
			t.Errorf("%s: findings %+v, want the exception", id, view.Findings)
		}
		if _, err := Output(tr, 1, 0, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: Output error %v, want ErrNotFound", id, err)
		}
		if _, err := TrajectoryFile(tr, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: TrajectoryFile error %v, want ErrNotFound", id, err)
		}
	}
}

func TestOutputErrors(t *testing.T) {
	tr := loadTrial(t, bundleClaude, "rn-basic__sryqofs")
	tests := []struct{ step, k int }{{0, 0}, {9, 0}, {2, 1}, {2, -1}, {1, 0}}
	for _, tt := range tests {
		if _, err := Output(tr, tt.step, tt.k, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("Output(step %d, result %d) error %v, want ErrNotFound", tt.step, tt.k, err)
		}
	}
	got, err := Output(tr, 2, 0, nil)
	if err != nil || !strings.HasPrefix(got, "Launching skill: acme-release-notes") {
		t.Errorf("Output(2, 0) = %q, %v", got, err)
	}
}

func TestTrajectoryFileKeepsFields(t *testing.T) {
	tr := loadTrial(t, bundleClaude, "rn-basic__sryqofs")
	data, err := TrajectoryFile(tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	steps, _ := doc["steps"].([]any)
	if doc["schema_version"] != "ATIF-v1.7" || len(steps) != 5 {
		t.Errorf("trajectory file: schema %v, %d steps", doc["schema_version"], len(steps))
	}
	// is_sidechain is not in the Trajectory type, the download keeps it.
	if !strings.Contains(string(data), `"is_sidechain"`) {
		t.Error("trajectory file lost fields the Trajectory type does not declare")
	}
}

// canaryValues reads the fake values of testdata/trials-secrets/canary/secrets.env.
func canaryValues(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdata, "trials-secrets/canary/secrets.env"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok {
			out[k] = v
		}
	}
	if len(out) < 9 {
		t.Fatalf("secrets.env has %d values", len(out))
	}
	return out
}

func assertNoForms(t *testing.T, what string, data []byte, values map[string]string, names ...string) {
	t.Helper()
	for _, name := range names {
		for _, f := range secretForms(values[name]) {
			if strings.Contains(string(data), f) {
				t.Errorf("%s holds %s in the form %q", what, name, f)
			}
		}
	}
}

func TestCanaryLeaksNothing(t *testing.T) {
	values := canaryValues(t)
	all := make([]string, 0, len(values))
	names := make([]string, 0, len(values))
	for k, v := range values {
		all = append(all, v)
		names = append(names, k)
	}
	llmKeys := []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OLLAMA_API_KEY", "OPENROUTER_API_KEY", "NVIDIA_API_KEY"}
	llmValues := make([]string, 0, len(llmKeys))
	for _, k := range llmKeys {
		llmValues = append(llmValues, values[k])
	}
	tests := []struct {
		name     string
		redactor *Redactor
		// absent are the values that must not appear in any form.
		absent []string
	}{
		{"every value of secrets.env", NewRedactor(all), names},
		// With only the llm-keys values, the patterns and the env-dump rule
		// still catch the GitHub token and the session secret, and the Harbor
		// password where it is printed raw (rawAbsent). The registry auth
		// value sits in JSON ("auth": "...") that no pattern covers, and it
		// holds the base64 of the Harbor password: only their exact values
		// catch those.
		{"llm-keys values only", NewRedactor(llmValues), append(slices.Clone(llmKeys), "GITHUB_TOKEN", "SESSION_SECRET")},
	}
	idx := loadIndex(t, bundleCanary)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := loadTrial(t, bundleCanary, "canary-env__T3stK3y")
			view, err := Build(Input{Run: apiv1.RunRef{Name: "canary"}, Index: idx, Trial: tr, Redactor: tt.redactor,
				OutputHref: outputHref("canary", tr.ID), TrajectoryHref: "/t"})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(view)
			assertNoForms(t, "session", data, values, tt.absent...)
			if strings.Contains(string(data), values["HARBOR_PASSWORD"]) {
				t.Error("session holds the raw Harbor password")
			}
			if view.Redaction.Render == 0 {
				t.Error("render-time redaction count is 0")
			}
			for _, s := range tr.Trajectory.Steps {
				if s.Observation == nil {
					continue
				}
				for k := range s.Observation.Results {
					out, err := Output(tr, s.StepID, k, tt.redactor)
					if err != nil {
						t.Fatal(err)
					}
					assertNoForms(t, fmt.Sprintf("output of step %d", s.StepID), []byte(out), values, tt.absent...)
				}
			}
			file, err := TrajectoryFile(tr, tt.redactor)
			if err != nil {
				t.Fatal(err)
			}
			assertNoForms(t, "trajectory file", file, values, tt.absent...)
			if !json.Valid(file) {
				t.Error("trajectory file is not JSON")
			}
			cmp, err := Compare(CompareInput{Index: idx, Case: "canary-env", Attempt: 1, WithSkill: tr, Redactor: tt.redactor})
			if err != nil {
				t.Fatal(err)
			}
			data, _ = json.Marshal(cmp)
			assertNoForms(t, "compare", data, values, tt.absent...)
		})
	}
	// The unredacted document holds the values, so the test is not vacuous.
	raw, err := os.ReadFile(filepath.Join(testdata, bundleCanary, "trial-canary-env__T3stK3y.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "OLLAMA_API_KEY", "HARBOR_PASSWORD", "GITHUB_TOKEN", "SESSION_SECRET", "REGISTRY_AUTH"} {
		if !strings.Contains(string(raw), values[k]) {
			t.Errorf("the canary document has no raw %s", k)
		}
	}
}

func TestMapNetwork(t *testing.T) {
	traj := &Trajectory{Steps: []Step{
		{StepID: 1, Timestamp: "2026-10-30T15:00:00Z"},
		{StepID: 2, Timestamp: "2026-10-30T15:00:10Z"},
		{StepID: 3, Timestamp: "2026-10-30T15:00:20Z"},
	}}
	at := func(s string) Flow {
		f := Flow{Verdict: "FORWARDED", Destination: "example.test", Port: 443, Protocol: "TCP", Count: 1}
		if err := f.FirstAt.UnmarshalText([]byte(s)); err != nil {
			t.Fatal(err)
		}
		f.LastAt = f.FirstAt
		return f
	}
	n := &Network{State: "collected", DNS: []DNSQuery{}, Flows: []Flow{
		at("2026-10-30T14:59:59Z"), at("2026-10-30T15:00:00Z"), at("2026-10-30T15:00:15Z"), at("2026-10-30T15:05:00Z"),
	}}
	got, err := MapNetwork(n, traj)
	if err != nil {
		t.Fatal(err)
	}
	var steps []int
	for _, f := range got.Flows {
		steps = append(steps, f.Step)
	}
	if !slices.Equal(steps, []int{0, 1, 2, 3}) {
		t.Errorf("flows placed at %v, want [0 1 2 3]", steps)
	}
	if got, err := MapNetwork(nil, traj); got != nil || err != nil {
		t.Errorf("MapNetwork(nil) = %v, %v", got, err)
	}
	none, _ := MapNetwork(n, nil)
	if none.Flows[3].Step != 0 || none.DNS == nil {
		t.Errorf("without a trajectory: %+v", none)
	}
}

func TestDecodeErrors(t *testing.T) {
	idx := `{"schema":"skillci.trials.index.v1","trials":[{"id":"a__1","layer":"trial-a__1.json.gz"}]}`
	tests := []struct {
		name string
		fn   func() error
	}{
		{"index schema", func() error { _, err := DecodeIndex([]byte(`{"schema":"x"}`)); return err }},
		{"index not JSON", func() error { _, err := DecodeIndex([]byte(`{`)); return err }},
		{"index too large", func() error { _, err := DecodeIndex(make([]byte, MaxIndexBytes+1)); return err }},
		{"index bad id", func() error {
			_, err := DecodeIndex([]byte(strings.Replace(idx, `"id":"a__1"`, `"id":"../x"`, 1)))
			return err
		}},
		{"index bad layer", func() error {
			_, err := DecodeIndex([]byte(strings.Replace(idx, "trial-a__1", "trial-b", 1)))
			return err
		}},
		{"index duplicate", func() error {
			_, err := DecodeIndex([]byte(strings.Replace(idx, `]}`, `,{"id":"a__1","layer":"trial-a__1.json.gz"}]}`, 1)))
			return err
		}},
		{"trial schema", func() error { _, err := DecodeTrial([]byte(`{"schema":"x","id":"a"}`)); return err }},
		{"trial bad id", func() error { _, err := DecodeTrial([]byte(`{"schema":"skillci.trial.v1","id":"/etc"}`)); return err }},
		{"trial too large", func() error { _, err := DecodeTrial(make([]byte, MaxTrialBytes+1)); return err }},
	}
	for _, tt := range tests {
		if err := tt.fn(); err == nil {
			t.Errorf("%s: no error", tt.name)
		}
	}
	if _, err := DecodeIndex([]byte(idx)); err != nil {
		t.Errorf("valid index: %v", err)
	}
}

// TestLenientATIF decodes ATIF variants: v1.5 with list content parts,
// reasoning as parts, redacted token counts, unknown fields, and a
// trajectory that cannot be read at all.
func TestLenientATIF(t *testing.T) {
	doc := `{"schema":"skillci.trial.v1","id":"x__1","arm":"with-skill","case":"x","attempt":1,"status":"scored",
	"meta":{"phases":[],"agent":{"name":"a"}},"prompt":"p","truncations":[],"redaction":{"version":"v1","exact":0,"pattern":0,"envDump":0},
	"future_field":{"a":1},
	"trajectory":{"schema_version":"ATIF-v1.5","agent":{"name":"a","extra":{"cwds":["/w"]}},"new_top":true,
	"steps":[
	 {"step_id":1,"source":"user","message":[{"type":"text","text":"hello"},{"type":"image","source":{"path":"x.png"}}]},
	 {"step_id":"2","source":"agent","reasoning_content":[{"type":"text","text":"think"}],"message":{"type":"text","text":"answer"},
	  "tool_calls":[{"tool_call_id":"c1","function_name":"Bash","arguments":"{\"command\":\"echo hi > out.txt\"}"}],
	  "observation":{"results":[{"source_call_id":"c1","content":[{"type":"text","text":"done"}],"extra":{"tool_result_metadata":{"is_error":true}}}]},
	  "metrics":{"prompt_tokens":"<redacted>","completion_tokens":5.0,"cost_usd":"0.5"},"unknown":1}
	],
	"final_metrics":{"total_prompt_tokens":"<redacted>","total_steps":2}}}`
	tr, err := DecodeTrial([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	s := tr.Trajectory.Steps
	if textOf(s[0].Message) != "hello\n[image]" || s[1].StepID != 2 || s[1].ReasoningContent != "think" || textOf(s[1].Message) != "answer" {
		t.Errorf("steps decoded as %+v", s)
	}
	if m := s[1].Metrics; m.PromptTokens != nil || m.CompletionTokens == nil || *m.CompletionTokens != 5 || m.CostUSD == nil || *m.CostUSD != 0.5 {
		t.Errorf("metrics %+v", m)
	}
	idx := &Index{Schema: SchemaIndex, Cases: []Case{{ID: "x"}}, Trials: []IndexTrial{{ID: "x__1", Arm: "with-skill", Case: "x", Attempt: 1, Status: "scored"}}}
	view, err := Build(Input{Index: idx, Trial: tr})
	if err != nil {
		t.Fatal(err)
	}
	if e := entry(t, view, "s-2-3"); e.Tool == nil || !e.Error || e.Tool.Command != "echo hi > out.txt" || e.Tool.Output.Preview != "done" {
		t.Errorf("tool entry %+v", e)
	}
	if len(view.Files) != 1 || view.Files[0].Op != apiv1.FileCommand || view.Files[0].Path != "out.txt" || view.Files[0].Captured {
		t.Errorf("files %+v, want out.txt written by a command", view.Files)
	}
	if view.Metadata.Workspace != "/w" || view.Metadata.SchemaVersion != "ATIF-v1.5" {
		t.Errorf("metadata %+v", view.Metadata)
	}

	broken := strings.Replace(doc, `"steps":[`, `"steps":{"not":"a list"},"x":[`, 1)
	tr, err = DecodeTrial([]byte(broken))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Trajectory != nil || !strings.HasPrefix(tr.TrajectoryOmitted, "the trajectory could not be read") {
		t.Errorf("broken trajectory: %v, %q", tr.Trajectory, tr.TrajectoryOmitted)
	}
}

func TestToolsAndFiles(t *testing.T) {
	call := func(name, args string) ToolCall {
		return ToolCall{ToolCallID: "c", FunctionName: name, Arguments: json.RawMessage(args)}
	}
	tests := []struct {
		name             string
		tc               ToolCall
		kind, cat, title string
		files            []string
		ops              []string
		hunks            int
	}{
		{"claude bash", call("Bash", `{"command":"ls -la /workspace"}`), apiv1.ToolShell, apiv1.CategoryTools, "$ ls -la /workspace", nil, nil, 0},
		{"curl", call("Bash", `{"command":"curl -s https://example.test"}`), apiv1.ToolShell, apiv1.CategoryWeb, "$ curl -s https://example.test", nil, nil, 0},
		{"npm", call("exec_command", `{"cmd":"cd app && npm install left-pad"}`), apiv1.ToolShell, apiv1.CategoryPkg, "$ cd app && npm install left-pad", nil, nil, 0},
		{"argv shell", call("shell", `{"command":["bash","-lc","uv pip install x"]}`), apiv1.ToolShell, apiv1.CategoryPkg, "$ uv pip install x", nil, nil, 0},
		{"tee and redirect", call("Bash", `{"command":"echo a | tee -a log.txt; echo b >> /tmp/b.txt 2>/dev/null"}`), apiv1.ToolShell, apiv1.CategoryTools, "", []string{"/tmp/b.txt", "log.txt"}, []string{"command", "command"}, 0},
		{"read skill", call("Read", `{"file_path":"/root/.claude/skills/x/SKILL.md"}`), apiv1.ToolRead, apiv1.CategorySkill, "Read /root/.claude/skills/x/SKILL.md", nil, nil, 0},
		{"grep", call("Grep", `{"pattern":"TODO","path":"src"}`), apiv1.ToolRead, apiv1.CategoryTools, "Grep TODO in src", nil, nil, 0},
		{"multiedit", call("MultiEdit", `{"file_path":"a.go","edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":"d"}]}`), apiv1.ToolEdit, apiv1.CategoryTools, "MultiEdit a.go", []string{"a.go"}, []string{"edit"}, 2},
		{"str_replace create", call("str_replace_editor", `{"command":"create","path":"n.txt","file_text":"x"}`), apiv1.ToolWrite, apiv1.CategoryTools, "str_replace_editor n.txt", []string{"n.txt"}, []string{"write"}, 0},
		{"two-file patch", call("apply_patch", `{"input":"*** Begin Patch\n*** Add File: a.txt\n+a\n*** Update File: b.txt\n@@\n-x\n+y\n*** End Patch\n"}`), apiv1.ToolPatch, apiv1.CategoryTools, "apply_patch a.txt and 1 more", []string{"a.txt", "b.txt"}, []string{"patch", "patch"}, 0},
		{"freeform patch", call("apply_patch", `"*** Begin Patch\n*** Delete File: old.txt\n*** End Patch\n"`), apiv1.ToolPatch, apiv1.CategoryTools, "apply_patch old.txt", []string{"old.txt"}, []string{"patch"}, 0},
		{"web fetch", call("WebFetch", `{"url":"https://example.test/doc"}`), apiv1.ToolWeb, apiv1.CategoryWeb, "WebFetch https://example.test/doc", nil, nil, 0},
		{"skill", call("Skill", `{"skill":"acme"}`), apiv1.ToolSkill, apiv1.CategorySkill, "Skill acme", nil, nil, 0},
		{"mcp", call("mcp__github__create_issue", `{}`), apiv1.ToolOther, apiv1.CategoryTools, "mcp__github__create_issue", nil, nil, 0},
	}
	for _, tt := range tests {
		ti := classify(tt.tc)
		if ti.kind != tt.kind || ti.category != tt.cat || tt.title != "" && ti.title != tt.title {
			t.Errorf("%s: kind %s category %s title %q; want %s %s %q", tt.name, ti.kind, ti.category, ti.title, tt.kind, tt.cat, tt.title)
		}
		c := &buildCtx{t: &Trial{}}
		files := c.fileChanges(0, 0, 1, "s-1", tt.tc, ti)
		var paths, ops []string
		hunks := 0
		for _, f := range files {
			paths = append(paths, f.Path)
			ops = append(ops, f.Op)
			hunks += len(f.Hunks)
		}
		slices.Sort(paths)
		if !slices.Equal(paths, tt.files) || !slices.Equal(ops, tt.ops) || hunks != tt.hunks {
			t.Errorf("%s: files %v ops %v hunks %d; want %v %v %d", tt.name, paths, ops, hunks, tt.files, tt.ops, tt.hunks)
		}
	}
}

func TestGrids(t *testing.T) {
	codex, err := Grid(loadIndex(t, bundleCodex), "job", "sha256:x")
	if err != nil {
		t.Fatal(err)
	}
	cell := codex.Cases[0].WithoutSkill[0]
	if cell == nil || cell.Status != apiv1.StatusWithheld || cell.HasSession || codex.Totals.Withheld != 1 || len(codex.Withheld) != 1 {
		t.Errorf("codex grid: %+v %+v", cell, codex.Totals)
	}
	if codex.Network == nil || codex.Network.Forwarded != 2 || codex.Network.Dropped != 1 {
		t.Errorf("codex network summary %+v", codex.Network)
	}

	tests := []struct {
		dir       string
		with, wo  string
		status    string
		errorType string
		scored    int
	}{
		{"trials/claude-code-doks/run-dir", "rn-basic__sryqofs", "rn-basic__BQbpoR9", apiv1.StatusScored, "", 2},
		{"trials/claude-code-unscored/run-dir", "rn-basic__CbK6FQ2", "rn-basic__59JHTSp", apiv1.StatusUnscored, "RuntimeError", 0},
	}
	for _, tt := range tests {
		data, err := os.ReadFile(filepath.Join(testdata, tt.dir, "result.json"))
		if err != nil {
			t.Fatal(err)
		}
		g, err := FallbackGrid(data, "claude-code", "job")
		if err != nil {
			t.Fatal(err)
		}
		if g.State != apiv1.GridFallback || len(g.Cases) != 1 || g.AttemptsPerCase != 1 || g.Totals.Scored != tt.scored {
			t.Fatalf("%s: grid %+v", tt.dir, g)
		}
		w, wo := g.Cases[0].WithSkill[0], g.Cases[0].WithoutSkill[0]
		if w.ID != tt.with || wo.ID != tt.wo || w.Status != tt.status || w.ErrorType != tt.errorType || w.HasSession || wo.HasSession {
			t.Errorf("%s: cells %+v %+v", tt.dir, w, wo)
		}
	}
	if _, err := FallbackGrid([]byte(`{"agents":{}}`), "codex", "job"); err == nil {
		t.Error("FallbackGrid without the agent: no error")
	}
	if _, err := Grid(nil, "", ""); err == nil {
		t.Error("Grid(nil): no error")
	}
}

func TestCompareSides(t *testing.T) {
	idx := loadIndex(t, bundleClaude)
	tr := loadTrial(t, bundleClaude, "rn-basic__sryqofs")
	got, err := Compare(CompareInput{Index: idx, Case: "rn-basic", Attempt: 1, WithSkill: tr})
	if err != nil {
		t.Fatal(err)
	}
	// The baseline is in the index but its document was not handed over.
	if got.WithoutSkill.Trial == nil || got.WithoutSkill.Missing == "" || got.WithoutSkill.Header != nil {
		t.Errorf("without side %+v", got.WithoutSkill)
	}
	for _, e := range got.WithSkill.Log {
		if !slices.Contains(FocusCategories, e.Category) {
			t.Errorf("compare log has %s entry %s outside the Focus preset", e.Category, e.ID)
		}
	}
	got, err = Compare(CompareInput{Index: idx, Case: "rn-basic", Attempt: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got.WithSkill.Trial != nil || got.WithSkill.Missing != "No with-skill trial for attempt 2 in the bundle." {
		t.Errorf("attempt 2: %+v", got.WithSkill)
	}
	if _, err := Compare(CompareInput{Index: idx, Case: "nope", Attempt: 1}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown case: %v", err)
	}
}
