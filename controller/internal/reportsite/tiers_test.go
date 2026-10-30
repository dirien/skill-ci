package reportsite

import (
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/dirien/skill-ci/controller/internal/policy"
)

// rowOf returns the table row of body that links to href, or "".
func rowOf(body, href string) string {
	i := strings.Index(body, `href="`+href+`"`)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(body[:i], "<tr")
	end := strings.Index(body[i:], "</tr>")
	if start < 0 || end < 0 {
		return ""
	}
	return body[start : i+end]
}

func TestTierPages(t *testing.T) {
	h := newHarnessWith(t, tierSpecs(), tierHarbor())
	sloppyHaiku := runPath(sloppy, "v1.0.0", haiku)
	sloppySonnet := runPath(sloppy, "v1.0.0", sonnet)
	const noTrials = " rejected this version, so no live trials were spent."
	tests := []struct {
		name   string
		path   string
		want   []string
		absent []string
	}{
		{
			name: "runs page counts rejected versions and shows the new columns", path: "/",
			want: []string{
				"<dt>Rejected</dt><dd>2</dd>", `<th scope="col">Tiers</th>`, `<th scope="col">Publication</th>`,
				`<th scope="col">Rejected</th>`, `<option value="Rejected">`, `<option value="failed">`, `<option value="incomplete">`,
				`aria-label="Tier 1 (static checks): failed"`, `aria-label="Tier 3 (live trials): skipped"`,
				`aria-label="Tier 1 (static checks): incomplete"`, `aria-label="Tier 3 (live trials): passed"`,
				`title="Tier 2 (deduplication): failed"`, "T1</span>", "T3</span>",
			},
		},
		{name: "filter by Rejected verdict", path: "/?verdict=Rejected", want: []string{"5 of 24 runs", `<option value="Rejected" selected>`}},
		{name: "filter by failed publication", path: "/?publication=failed", want: []string{"5 of 24 runs", `<option value="failed" selected>`}},
		{name: "filter by incomplete publication", path: "/?publication=incomplete", want: []string{"2 of 24 runs"}},
		{name: "publication filter combines", path: "/?publication=failed&skill=" + dup, want: []string{"2 of 24 runs"}},
		{
			name: "skill page explains a Tier 1 rejection", path: "/skills/" + sloppy,
			want: []string{
				"Rejected by Tier 1: tier1/security: hardcoded credential in scripts/deploy.sh", ">Rejected<",
				"Tier 1 failed · no trials run", "needs lift +0.10",
			},
			absent: []string{"On stable", "Not promoted", "Meets threshold"},
		},
		{name: "skill page explains a Tier 2 rejection", path: "/skills/" + dup, want: []string{"Rejected by Tier 2: tier2/dedup: 91% similar to acme-release-notes"}},
		{
			name: "run page of a Tier 1 rejection without scores", path: sloppyHaiku,
			want: []string{
				"SkillEvaluator tiers", "Tier 3 not run: Tier 1" + noTrials, finding1, finding2, "<h3 id=\"findings-h\">Findings</h3>",
				"Static checks: schema, security, PII, license", "Deduplication against the other skills in Harbor", "Live trials with and without the skill",
				">Failed<", ">Skipped<", ">Passed<", "<dt>Publication</dt>", "Rejected", "Tier1Failed",
				"Open full SkillEvaluator report", "Download result.json", "No dimension scores: Tier 3 did not run",
			},
			absent: []string{"<dt>With skill</dt>", "<dt>Without skill</dt>", "<dt>Lift</dt>", "Accuracy", "Could not read the report", "No findings"},
		},
		{
			name: "run page of a rejection without a stored report", path: sloppySonnet,
			want:   []string{"Tier 3 not run: Tier 1" + noTrials, "No report stored for this run."},
			absent: []string{"<dt>With skill</dt>", "before 0.3.0", "Open full SkillEvaluator report"},
		},
		{
			name: "run page of a Tier 2 rejection", path: runPath(dup, "v1.0.0", haiku),
			want: []string{"Tier 3 not run: Tier 2" + noTrials, "tier2/dedup: 91% similar"},
		},
		{
			name: "run page with every tier passed keeps scores and dimensions", path: runPath("acme-release-notes", "v1.1.0", haiku),
			want: []string{
				"SkillEvaluator tiers", "<dt>With skill</dt>", "<dt>Lift</dt>", "Accuracy", "Overall", "No findings", `aria-label="Tier 3 (live trials): passed"`,
			},
			absent: []string{"Tier 3 not run"},
		},
		{
			name: "run page with an incomplete tier", path: runPath(gap, "v1.0.0", haiku),
			want:   []string{">Incomplete<", "<dt>Publication</dt>"},
			absent: []string{"Tier 3 not run"},
		},
		{
			name: "run page of an older runner has no tier results", path: runPath("python-docstrings", "v1.0.0", haiku),
			want:   []string{"SkillEvaluator tiers", "This run has no tier results: the runner that produced it did not report them.", "<dt>With skill</dt>"},
			absent: []string{"Tier 3 not run", "Findings"},
		},
		{
			name: "pending run waits for tier results", path: runPath("acme-release-notes", "v1.3.0", sonnet),
			want: []string{"Tier results appear when the run finishes."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.get(tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			body := html.UnescapeString(rec.Body.String())
			for _, w := range tt.want {
				if !strings.Contains(body, w) {
					t.Errorf("body lacks %q", w)
				}
			}
			for _, a := range tt.absent {
				if strings.Contains(body, a) {
					t.Errorf("body contains %q", a)
				}
			}
		})
	}
}

func TestTierColumnsOfRuns(t *testing.T) {
	h := newHarnessWith(t, tierSpecs(), tierHarbor())
	body := html.UnescapeString(h.get("/").Body.String())
	tests := []struct {
		name   string
		href   string
		want   []string
		absent []string
	}{
		{"older run shows a dash", runPath("python-docstrings", "v1.0.0", haiku),
			[]string{`title="The runner did not report tiers">–`}, []string{`class="tiers"`, "Tier 1 (static checks)"}},
		{"rejected run", runPath(sloppy, "v1.0.0", haiku),
			[]string{`status tier failed`, `status tier pass`, `status tier neutral`, ">Rejected<", ">Failed<"}, []string{`did not report tiers`}},
		{"passed run", runPath("acme-release-notes", "v1.1.0", haiku),
			[]string{`status tier pass`, ">Passed<"}, []string{`status tier failed`, `did not report tiers`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := rowOf(body, tt.href)
			if row == "" {
				t.Fatalf("no row links to %s", tt.href)
			}
			for _, w := range tt.want {
				if !strings.Contains(row, w) {
					t.Errorf("row lacks %q:\n%s", w, row)
				}
			}
			for _, a := range tt.absent {
				if strings.Contains(row, a) {
					t.Errorf("row contains %q", a)
				}
			}
		})
	}
}

func TestChannelsShowRejectedVersions(t *testing.T) {
	h := newHarnessWith(t, tierSpecs(), tierHarbor())
	body := html.UnescapeString(h.get("/").Body.String())
	for _, skill := range []string{sloppy, dup} {
		row := rowOf(body, "/skills/"+skill)
		if !strings.Contains(row, `class="status failed" title="Tier 1 or Tier 2 failed: never beta or stable"><i aria-hidden="true"></i>v1.0.0`) {
			t.Errorf("channel row of %s lacks the rejected version:\n%s", skill, row)
		}
	}
	if row := rowOf(body, "/skills/"+gap); strings.Contains(row, "never beta or stable") {
		t.Errorf("channel row of a skill that was not rejected has a rejected marker:\n%s", row)
	}
}

func TestRunTierHelpers(t *testing.T) {
	tests := []struct {
		name       string
		run        Run
		rejected   bool
		rejectedBy string
		note       string
		noScores   bool
		known      bool
	}{
		{"older runner", Run{Phase: "Succeeded", With: newScore("0.9"), Without: newScore("0.4")}, false, "Tier 1 or Tier 2", "", false, false},
		{"tier 1 failed", Run{Phase: "Succeeded", Tier1: "failed", Tier2: "passed", Tier3: "skipped"}, true, "Tier 1",
			"Tier 3 not run: Tier 1 rejected this version, so no live trials were spent.", true, true},
		{"tier 2 failed", Run{Phase: "Succeeded", Tier1: "passed", Tier2: "failed", Tier3: "skipped"}, true, "Tier 2",
			"Tier 3 not run: Tier 2 rejected this version, so no live trials were spent.", true, true},
		{"both failed", Run{Phase: "Succeeded", Tier1: "failed", Tier2: "failed", Tier3: "skipped"}, true, "Tier 1 and Tier 2",
			"Tier 3 not run: Tier 1 and Tier 2 rejected this version, so no live trials were spent.", true, true},
		{"skipped without a failure", Run{Phase: "Succeeded", Tier1: "incomplete", Tier2: "passed", Tier3: "skipped"}, false, "Tier 1 or Tier 2",
			"Tier 3 not run: the runner skipped the live trials.", true, true},
		{"all passed", Run{Phase: "Succeeded", Tier1: "passed", Tier2: "passed", Tier3: "passed", With: newScore("0.9"), Without: newScore("0.4")},
			false, "Tier 1 or Tier 2", "", false, true},
		{"rejected by verdict only", Run{Phase: "Succeeded", Verdict: "Rejected"}, true, "Tier 1 or Tier 2",
			"Tier 3 not run: Tier 1 or Tier 2 rejected this version, so no live trials were spent.", true, false},
		{"pending", Run{Phase: "Pending"}, false, "Tier 1 or Tier 2", "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.run
			if r.Rejected() != tt.rejected || r.RejectedBy() != tt.rejectedBy || r.Tier3Note() != tt.note ||
				r.NoScores() != tt.noScores || r.TiersKnown() != tt.known {
				t.Errorf("Rejected %v, RejectedBy %q, Tier3Note %q, NoScores %v, TiersKnown %v", r.Rejected(), r.RejectedBy(), r.Tier3Note(), r.NoScores(), r.TiersKnown())
			}
		})
	}
}

func TestTierStatus(t *testing.T) {
	tests := []struct {
		value string
		want  Status
	}{
		{"passed", Status{"pass", "Passed"}},
		{"failed", Status{"failed", "Failed"}},
		{"incomplete", Status{"regressed", "Incomplete"}},
		{"skipped", Status{"neutral", "Skipped"}},
		{"", Status{}},
		{"surprise", Status{}},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := tierStatus(tt.value); got != tt.want {
				t.Errorf("tierStatus(%q) = %+v, want %+v", tt.value, got, tt.want)
			}
		})
	}
	r := Run{Tier1: "failed", Tier2: "", Tier3: "skipped"}
	got := r.TierMarkers()
	if len(got) != 3 || got[0].Text != "Tier 1 (static checks): failed" || got[1].Text != "Tier 2 (deduplication): not reported" ||
		got[2].Text != "Tier 3 (live trials): skipped" {
		t.Errorf("TierMarkers() = %+v", got)
	}
}

func TestDecideRejected(t *testing.T) {
	rej := func(model string, required bool, tier1, tier2 string, findings ...string) Run {
		return Run{Model: model, Required: required, Phase: "Succeeded", Verdict: "Rejected", Tier1: tier1, Tier2: tier2, Tier3: "skipped", Findings: findings}
	}
	ok := required("sonnet", "+0.60", "Succeeded", "")
	tests := []struct {
		name   string
		runs   []Run
		stable stableInfo
		want   Decision
	}{
		{"tier 1 with a finding", []Run{rej("haiku", true, "failed", "passed", "tier1/security: hardcoded credential"), ok}, stableInfo{Known: true},
			Decision{Kind: "failed", Headline: "Rejected", Detail: "Rejected by Tier 1: tier1/security: hardcoded credential"}},
		{"tier 2 without findings", []Run{rej("haiku", true, "passed", "failed")}, stableInfo{Known: true},
			Decision{Kind: "failed", Headline: "Rejected", Detail: "Rejected by Tier 2"}},
		{"even when it still holds stable", []Run{rej("haiku", true, "failed", "passed", "tier1/pii: email")}, stableInfo{Known: true, This: true, Holder: "v1.0.0"},
			Decision{Kind: "failed", Headline: "Rejected", Detail: "Rejected by Tier 1: tier1/pii: email"}},
		{"an optional model rejecting does not decide", []Run{required("haiku", "+0.60", "Succeeded", ""), rej("opus", false, "failed", "passed", "x")}, stableInfo{Known: true},
			Decision{Kind: "neutral", Headline: "Meets threshold", Detail: "Lift is +0.10 or more on every required model; stable is not assigned yet."}},
		{"a running required model still waits", []Run{rej("haiku", true, "failed", "passed", "x"), required("sonnet", "", "Running", "")}, stableInfo{Known: true},
			Decision{Kind: "neutral", Headline: "Waiting", Detail: "1 of 2 required models have finished."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decide(tt.runs, tt.stable, policy.DefaultThresholds()); got != tt.want {
				t.Errorf("decide = %+v\nwant     %+v", got, tt.want)
			}
		})
	}
}
