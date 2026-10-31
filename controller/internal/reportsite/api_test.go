package reportsite

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
	"github.com/dirien/skill-ci/controller/internal/session"
)

func runPath(name string) string      { return apiv1.Prefix + "/runs/" + name }
func trialPath(run, id string) string { return runPath(run) + "/trials/" + id }

// checkAPIError checks an Error body with its status and code, and the API headers.
func checkAPIError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != apiv1.CSP {
		t.Errorf("CSP = %q", csp)
	}
	var body apiv1.Error
	decode(t, rec, &body)
	if body.Error.Code != code || body.Error.Status != rec.Code || body.Error.Message == "" {
		t.Errorf("error body = %+v, want code %s", body.Error, code)
	}
}

func TestAPIStatusCodes(t *testing.T) {
	h := newWorld(t)
	codexFirst := runCodex
	tests := []struct {
		name, target string
		status       int
		code         string
	}{
		{"unknown run", runPath("nope"), 404, apiv1.CodeNotFound},
		{"malformed run name", runPath("Not_A_Name"), 400, apiv1.CodeBadRequest},
		{"unknown job", runPath(runCodex) + "?job=other-job", 404, apiv1.CodeNotFound},
		{"malformed job", runPath(runCodex) + "?job=Bad_Job", 400, apiv1.CodeBadRequest},
		{"unknown query parameter", runPath(runCodex) + "?foo=1", 400, apiv1.CodeBadRequest},
		{"repeated query parameter", apiv1.Prefix + "/runs?skill=a&skill=b", 400, apiv1.CodeBadRequest},
		{"site takes no parameters", apiv1.Prefix + "/site?x=1", 400, apiv1.CodeBadRequest},
		{"unknown sort", apiv1.Prefix + "/runs?sort=random", 400, apiv1.CodeBadRequest},
		{"unknown verdict", apiv1.Prefix + "/runs?verdict=Great", 400, apiv1.CodeBadRequest},
		{"unknown phase", apiv1.Prefix + "/runs?phase=Done", 400, apiv1.CodeBadRequest},
		{"unknown trigger", apiv1.Prefix + "/runs?trigger=cron", 400, apiv1.CodeBadRequest},
		{"page size zero", apiv1.Prefix + "/runs?pageSize=0", 400, apiv1.CodeBadRequest},
		{"page size too large", apiv1.Prefix + "/runs?pageSize=201", 400, apiv1.CodeBadRequest},
		{"page not a number", apiv1.Prefix + "/runs?page=two", 400, apiv1.CodeBadRequest},
		{"page with a sign", apiv1.Prefix + "/runs?page=+2", 400, apiv1.CodeBadRequest},
		{"control character", apiv1.Prefix + "/runs?q=" + url.QueryEscape("a\x00b"), 400, apiv1.CodeBadRequest},
		{"long filter", apiv1.Prefix + "/runs?model=" + strings.Repeat("m", 300), 400, apiv1.CodeBadRequest},
		{"unknown skill", apiv1.Prefix + "/skills/nope", 404, apiv1.CodeNotFound},
		{"malformed skill", apiv1.Prefix + "/skills/Bad%20Skill", 400, apiv1.CodeBadRequest},
		{"malformed trial id", trialPath(runClaude, "-dash-first"), 400, apiv1.CodeBadRequest},
		{"trial id too long", trialPath(runClaude, strings.Repeat("a", 129)), 400, apiv1.CodeBadRequest},
		{"unknown trial", trialPath(runClaude, "nope"), 404, apiv1.CodeNotFound},
		{"withheld trial", trialPath(runCodex, "rn-basic__Cx2nW9d"), 404, apiv1.CodeWithheld},
		{"run without a bundle", trialPath(runFallback, "rn-basic__Fw1qa2b"), 404, apiv1.CodeNotFound},
		{"rejected run has no trials", trialPath(runRejected, "x1"), 404, apiv1.CodeNotFound},
		{"running run has no report", trialPath(runRunning, "x1"), 404, apiv1.CodeNotFound},
		{"earlier attempt without a report", trialPath(runCodex, "rn-basic__Cx7aQ1p") + "?job=" + codexFirst, 404, apiv1.CodeNotFound},
		{"trial takes only job", trialPath(runClaude, "rn-basic__sryqofs") + "?attempt=1", 400, apiv1.CodeBadRequest},
		{"step zero", trialPath(runClaude, "rn-basic__sryqofs") + "/steps/0/output/0", 400, apiv1.CodeBadRequest},
		{"step not a number", trialPath(runClaude, "rn-basic__sryqofs") + "/steps/two/output/0", 400, apiv1.CodeBadRequest},
		{"result negative", trialPath(runClaude, "rn-basic__sryqofs") + "/steps/2/output/-1", 400, apiv1.CodeBadRequest},
		{"unknown step", trialPath(runClaude, "rn-basic__sryqofs") + "/steps/99/output/0", 404, apiv1.CodeNotFound},
		{"unknown result", trialPath(runClaude, "rn-basic__sryqofs") + "/steps/2/output/5", 404, apiv1.CodeNotFound},
		{"output of a withheld trial", trialPath(runCodex, "rn-basic__Cx2nW9d") + "/steps/1/output/0", 404, apiv1.CodeWithheld},
		{"trajectory of an unscored trial", trialPath(runUnscored, "rn-basic__CbK6FQ2") + "/trajectory.json", 404, apiv1.CodeNotFound},
		{"unknown case", runPath(runClaude) + "/cases/nope/compare", 404, apiv1.CodeNotFound},
		{"malformed case", runPath(runClaude) + "/cases/.x/compare", 400, apiv1.CodeBadRequest},
		{"attempt zero", runPath(runClaude) + "/cases/rn-basic/compare?attempt=0", 400, apiv1.CodeBadRequest},
		{"with of the wrong arm", runPath(runClaude) + "/cases/rn-basic/compare?with=rn-basic__BQbpoR9", 404, apiv1.CodeNotFound},
		{"with malformed", runPath(runClaude) + "/cases/rn-basic/compare?with=a/b", 400, apiv1.CodeBadRequest},
		{"with and another attempt", runPath(runClaude) + "/cases/rn-basic/compare?with=rn-basic__sryqofs&attempt=2", 400, apiv1.CodeBadRequest},
		{"unknown API path", apiv1.Prefix + "/nope", 404, apiv1.CodeNotFound},
		{"unknown API path outside v1", "/api/v2/runs", 404, apiv1.CodeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkAPIError(t, h.get(tt.target), tt.status, tt.code)
		})
	}
}

func TestAPIIsReadOnly(t *testing.T) {
	h := newWorld(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if rec := h.do(method, apiv1.Prefix+"/runs", nil); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/v1/runs = %d, want 405", method, rec.Code)
		}
	}
	if rec := h.do(http.MethodHead, apiv1.Prefix+"/overview", nil); rec.Code != http.StatusOK {
		t.Errorf("HEAD /api/v1/overview = %d, want 200", rec.Code)
	}
	if rec := h.do(http.MethodHead, trialPath(runClaude, "rn-basic__sryqofs"), nil); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD of a session = %d with %d bytes, want 200 and no body", rec.Code, rec.Body.Len())
	}
}

func TestAPIHeaders(t *testing.T) {
	h := newWorld(t)
	for _, target := range []string{"/site", "/overview", "/runs", "/runs/" + runClaude, "/skills", "/skills/" + acme, "/models"} {
		rec := h.get(apiv1.Prefix + target)
		hd := rec.Header()
		if rec.Code != 200 || hd.Get("Cache-Control") != "no-store" || hd.Get("Content-Security-Policy") != apiv1.CSP ||
			hd.Get("Content-Type") != "application/json; charset=utf-8" || hd.Get("ETag") != "" {
			t.Errorf("%s: %d, headers %v", target, rec.Code, hd)
		}
		if hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: missing nosniff or Referrer-Policy: %v", target, hd)
		}
	}
	session := []struct{ target, contentType, csp string }{
		{trialPath(runClaude, "rn-basic__sryqofs"), "application/json; charset=utf-8", apiv1.CSP},
		{runPath(runClaude) + "/cases/rn-basic/compare", "application/json; charset=utf-8", apiv1.CSP},
		{trialPath(runClaude, "rn-basic__sryqofs") + "/steps/2/output/0", "text/plain; charset=utf-8", apiv1.CSP + "; sandbox"},
		{trialPath(runClaude, "rn-basic__sryqofs") + "/trajectory.json", "application/json", apiv1.CSP},
	}
	etags := map[string]bool{}
	for _, s := range session {
		rec := h.get(s.target)
		hd := rec.Header()
		if rec.Code != 200 || hd.Get("Cache-Control") != "private, max-age=300" || hd.Get("Content-Type") != s.contentType ||
			hd.Get("Content-Security-Policy") != s.csp || hd.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: %d, headers %v", s.target, rec.Code, hd)
		}
		etag := hd.Get("ETag")
		if !strings.HasPrefix(etag, `"`) || etags[etag] {
			t.Errorf("%s: ETag %q is missing or not unique", s.target, etag)
		}
		etags[etag] = true
		again := h.do(http.MethodGet, s.target, http.Header{"If-None-Match": {etag}})
		if again.Code != http.StatusNotModified {
			t.Errorf("%s with If-None-Match = %d, want 304", s.target, again.Code)
		}
	}
	rec := h.get(trialPath(runClaude, "rn-basic__sryqofs") + "/trajectory.json")
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="rn-basic__sryqofs-trajectory.json"` {
		t.Errorf("trajectory Content-Disposition = %q", cd)
	}
	if !strings.Contains(rec.Body.String(), `"schema_version"`) {
		t.Errorf("trajectory is not ATIF: %.100q", rec.Body.String())
	}
	// ?job= naming the newest attempt answers the same body as no ?job=, so it has the same ETag.
	a := h.get(trialPath(runCodex, "rn-basic__Cx7aQ1p"))
	b := h.get(trialPath(runCodex, "rn-basic__Cx7aQ1p") + "?job=" + runCodex + "-r1")
	if a.Header().Get("ETag") != b.Header().Get("ETag") || a.Body.String() != b.Body.String() {
		t.Errorf("?job= of the newest attempt changed the answer")
	}
}

func TestOutputIsTheFullText(t *testing.T) {
	h := newWorld(t)
	rec := h.get(trialPath(runOpencode, "rn-basic__Op4mZ8e") + "/steps/5/output/0")
	if rec.Code != 200 {
		t.Fatalf("output = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bytes cut]") {
		t.Errorf("the shortened output should keep the runner's marker: %.200q", rec.Body.String())
	}
}

func TestSessionsAreCached(t *testing.T) {
	h := newWorld(t)
	target := trialPath(runClaude, "rn-basic__sryqofs")
	first := h.get(target)
	n := h.store.gunzips
	second := h.get(target)
	if first.Code != 200 || second.Body.String() != first.Body.String() {
		t.Fatalf("second answer differs: %d", second.Code)
	}
	if h.store.gunzips != n {
		t.Errorf("the second request read the trial document again (%d reads, want %d)", h.store.gunzips, n)
	}
}

func TestRunsFilterSortAndPages(t *testing.T) {
	h := newWorld(t)
	names := func(target string) []string {
		t.Helper()
		rec := h.get(target)
		if rec.Code != 200 {
			t.Fatalf("%s = %d: %s", target, rec.Code, rec.Body.String())
		}
		var list apiv1.RunList
		decode(t, rec, &list)
		var out []string
		for _, r := range list.Items {
			out = append(out, r.Name)
		}
		return out
	}
	runs := apiv1.Prefix + "/runs"
	if got := names(runs + "?agent=codex@0.161.0"); len(got) != 2 || got[0] != runPending || got[1] != runCodex {
		t.Errorf("agent filter = %v", got)
	}
	if got := names(runs + "?q=OPENCODE"); len(got) != 1 || got[0] != runOpencode {
		t.Errorf("q filter = %v", got)
	}
	if got := names(runs + "?trigger=model-change"); len(got) != 1 || got[0] != runRejected {
		t.Errorf("trigger filter = %v", got)
	}
	if got := names(runs + "?publication=failed&verdict=Rejected"); len(got) != 1 || got[0] != runRejected {
		t.Errorf("publication and verdict filter = %v", got)
	}
	if got := names(runs + "?sort=lift"); len(got) != 8 || got[0] != runClaude || got[1] != runFallback {
		t.Errorf("sort=lift = %v", got)
	}
	if got := names(runs + "?sort=oldest"); got[0] != runFallback || got[7] != runRunning {
		t.Errorf("sort=oldest = %v", got)
	}
	if got := names(runs + "?sort=skill"); got[0] != runUnscored || got[7] != runRejected {
		t.Errorf("sort=skill = %v", got)
	}
	rec := h.get(runs + "?pageSize=3&page=3")
	var list apiv1.RunList
	decode(t, rec, &list)
	if len(list.Items) != 2 || list.Pagination != (apiv1.Pagination{Page: 3, PageSize: 3, Total: 8, Pages: 3}) {
		t.Errorf("page 3 of 3 = %d items, %+v", len(list.Items), list.Pagination)
	}
	if got := names(runs + "?pageSize=3&page=9"); len(got) != 0 {
		t.Errorf("a page past the end = %v, want none", got)
	}
	rec = h.get(runs + "?model=claude-opus-9")
	decode(t, rec, &list)
	if len(list.Items) != 0 || list.Filter.Model != "claude-opus-9" || !contains(list.Options.Models, "claude-opus-9") {
		t.Errorf("a filter value no run has stays selectable: %+v", list.Options.Models)
	}
}

func TestRunDetailStates(t *testing.T) {
	h := newWorld(t)
	get := func(target string) apiv1.RunDetail {
		t.Helper()
		rec := h.get(target)
		if rec.Code != 200 {
			t.Fatalf("%s = %d: %s", target, rec.Code, rec.Body.String())
		}
		var v apiv1.RunDetail
		decode(t, rec, &v)
		return v
	}
	// The earlier attempt of the retried run attached no report.
	v := get(runPath(runCodex) + "?job=" + runCodex)
	if v.Report.State != apiv1.ReportNone || v.Trials.State != apiv1.GridNone || v.Trials.Job != runCodex ||
		v.Trials.Reason != "No sessions: this attempt stored no report." {
		t.Errorf("earlier attempt: report %q, trials %+v", v.Report.State, v.Trials)
	}
	if len(v.Attempts) != 2 || v.Attempts[0].Current || !v.Attempts[1].Current || v.Attempts[0].Trials != apiv1.TrialsNone {
		t.Errorf("attempts = %+v", v.Attempts)
	}
	// Tier 3 did not run: the report files only, and no grid.
	v = get(runPath(runRejected))
	if v.Report.State != apiv1.ReportFiles || v.Trials.State != apiv1.GridNone || !strings.HasPrefix(v.Trials.Reason, "Tier 3 not run: Tier 1 rejected") {
		t.Errorf("rejected run: report %q, trials %+v", v.Report.State, v.Trials)
	}
	// A run that has not finished.
	v = get(runPath(runRunning))
	if v.Report.State != apiv1.ReportNone || v.RefreshSeconds != refreshSeconds || v.Trials.Reason != "Trial sessions are stored when the run finishes." {
		t.Errorf("running run: %+v, refresh %d", v.Trials, v.RefreshSeconds)
	}
	// The registry fails: states, not a status code.
	h.store.err = errors.New("connection refused")
	v = get(runPath(runFallback))
	if v.Report.State != apiv1.ReportError || v.Trials.State != apiv1.GridError || v.Run.Trials != apiv1.TrialsUnknown {
		t.Errorf("registry down: report %q, trials %q, run trials %q", v.Report.State, v.Trials.State, v.Run.Trials)
	}
	h.store.err = nil
	// The report is gone from the registry.
	delete(h.store.annotations, reportFallback)
	delete(h.store.files, reportFallback+"/result.json")
	h.srv.bundles = newBundleMemo()
	v = get(runPath(runFallback))
	if v.Report.State != apiv1.ReportMissing || v.Trials.State != apiv1.GridMissing {
		t.Errorf("report gone: report %q, trials %q", v.Report.State, v.Trials.State)
	}
}

func TestWithheldAndDisabledBundles(t *testing.T) {
	for _, tt := range []struct {
		reason, state, trials string
	}{
		{"secret-scan", apiv1.GridWithheld, apiv1.TrialsWithheld},
		{"size", apiv1.GridWithheld, apiv1.TrialsWithheld},
		{"disabled", apiv1.GridFallback, apiv1.TrialsNone},
		{"no-trials", apiv1.GridNone, apiv1.TrialsNone},
	} {
		t.Run(tt.reason, func(t *testing.T) {
			h := newWorld(t)
			h.store.annotations[reportFallback] = map[string]string{session.AnnotationTrialsWithheld: tt.reason}
			var v apiv1.RunDetail
			decode(t, h.get(runPath(runFallback)), &v)
			if v.Trials.State != tt.state || v.Run.Trials != tt.trials || !strings.Contains(v.Trials.Reason, withheldText(tt.reason)) {
				t.Errorf("grid %q (%s), run trials %q", v.Trials.State, v.Trials.Reason, v.Run.Trials)
			}
			if v.Run.TrialsReason != tt.reason {
				t.Errorf("run trials reason = %q, want %q", v.Run.TrialsReason, tt.reason)
			}
			if tt.state != apiv1.GridNone && (len(v.Trials.Cases) != 1 || v.Trials.Cases[0].WithSkill[0].HasSession) {
				t.Errorf("the grid should show the result.json scores without links: %+v", v.Trials.Cases)
			}
			rec := h.get(trialPath(runFallback, "rn-basic__Fw1qa2b"))
			want := apiv1.CodeNotFound
			if tt.trials == apiv1.TrialsWithheld {
				want = apiv1.CodeWithheld
			}
			checkAPIError(t, rec, http.StatusNotFound, want)
		})
	}
}

func TestRegistryErrorsOnSessions(t *testing.T) {
	h := newWorld(t)
	h.store.err = errors.New("connection refused")
	checkAPIError(t, h.get(trialPath(runClaude, "rn-basic__sryqofs")), http.StatusBadGateway, apiv1.CodeRegistry)
	h.store.err = nil
	// The trial document is gone while the index still lists it.
	delete(h.store.layers, bundleClaude+"/"+session.TrialLayer("rn-basic__sryqofs"))
	checkAPIError(t, h.get(trialPath(runClaude, "rn-basic__sryqofs")), http.StatusNotFound, apiv1.CodeNotFound)
	// A comparison still renders the other side and says why one is missing.
	rec := h.get(runPath(runClaude) + "/cases/rn-basic/compare")
	var c apiv1.Compare
	decode(t, rec, &c)
	if rec.Code != 200 || c.WithSkill.Header != nil || c.WithSkill.Missing == "" || c.WithoutSkill.Header == nil {
		t.Errorf("compare with a missing document = %d, %+v", rec.Code, c.WithSkill)
	}
	// A layer over the decompression limit.
	h.store.layers[bundleClaude+"/"+session.TrialLayer("rn-basic__BQbpoR9")] = make([]byte, session.MaxTrialBytes+1)
	checkAPIError(t, h.get(trialPath(runClaude, "rn-basic__BQbpoR9")), http.StatusBadGateway, apiv1.CodeRegistry)
}

func TestCompareByTrialIDs(t *testing.T) {
	h := newWorld(t)
	rec := h.get(runPath(runClaude) + "/cases/rn-basic/compare?with=rn-basic__sryqofs&without=rn-basic__BQbpoR9")
	var c apiv1.Compare
	decode(t, rec, &c)
	if rec.Code != 200 || c.Attempt != 1 || c.WithSkill.Trial.ID != "rn-basic__sryqofs" || c.WithoutSkill.Trial.ID != "rn-basic__BQbpoR9" {
		t.Errorf("compare by ids = %d, %+v", rec.Code, c)
	}
	rec = h.get(runPath(runClaude) + "/cases/rn-basic/compare?attempt=2")
	decode(t, rec, &c)
	if rec.Code != 200 || c.WithSkill.Missing == "" || c.WithoutSkill.Missing == "" {
		t.Errorf("an attempt without trials should say so on both sides: %+v", c)
	}
}

func TestOverviewWhenHarborIsDown(t *testing.T) {
	h := newWorld(t)
	h.harbor.Err = errors.New("harbor unreachable")
	var o apiv1.Overview
	decode(t, h.get(apiv1.Prefix+"/overview"), &o)
	if o.ChannelsError == "" || o.Counts.OnStable != 0 || len(o.Channels) != 3 || o.Channels[0].Stable != "" {
		t.Errorf("overview with Harbor down: %+v", o)
	}
	var s apiv1.SkillList
	decode(t, h.get(apiv1.Prefix+"/skills"), &s)
	if s.ChannelsError == "" || len(s.Items) != 3 {
		t.Errorf("skills with Harbor down: %+v", s)
	}
	var d apiv1.SkillDetail
	decode(t, h.get(apiv1.Prefix+"/skills/"+acme), &d)
	if d.ChannelsError == "" || !strings.Contains(d.Rows[1].Decision.Detail, "Release channels are unavailable") {
		t.Errorf("skill with Harbor down: %+v", d.Rows[1].Decision)
	}
}

func TestOverviewAttention(t *testing.T) {
	h := newWorld(t)
	var o apiv1.Overview
	decode(t, h.get(apiv1.Prefix+"/overview"), &o)
	var kinds []string
	for _, a := range o.Attention {
		kinds = append(kinds, a.Kind+" "+a.Run)
	}
	want := []string{"failed " + runUnscored, "rejected " + runRejected, "running " + runRunning, "pending " + runPending}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("attention = %v, want %v", kinds, want)
	}
	if o.Counts != (apiv1.Counts{Runs: 8, Pending: 1, Running: 1, Succeeded: 5, Failed: 1, Skills: 3, OnStable: 2, Rejected: 1}) {
		t.Errorf("counts = %+v", o.Counts)
	}
	if o.Network.Source != v1alpha1.ConfigSourceSkillCIConfig || o.Network.EnforcedBy != "cilium" || o.Network.HubbleRelay == "" {
		t.Errorf("network policy = %+v", o.Network)
	}
}

func TestBuiltInNetworkPolicy(t *testing.T) {
	h := newHarness(t, nil, worldHarbor(), newFakeStore())
	var o apiv1.Overview
	decode(t, h.get(apiv1.Prefix+"/overview"), &o)
	if o.Network.Source != v1alpha1.ConfigSourceBuiltIn || o.Network.SourceText != "built-in defaults" || o.Network.Default != "full" ||
		o.Network.EnforcedBy != "" || o.Network.HubbleRelay != "" || len(o.Recent) != 0 || o.RefreshSeconds != 0 {
		t.Errorf("overview without SkillCIConfig = %+v", o)
	}
}

func TestModelsWithoutTheConfigMap(t *testing.T) {
	var objs []client.Object
	for _, s := range worldSpecs() {
		objs = append(objs, s.object())
	}
	h := newHarness(t, objs, worldHarbor(), worldStore(t))
	var m apiv1.ModelList
	decode(t, h.get(apiv1.Prefix+"/models"), &m)
	if m.Error == "" || len(m.Items) != 3 {
		t.Fatalf("models without the ConfigMap = %+v", m)
	}
	for _, e := range m.Items {
		if e.Listed {
			t.Errorf("%s is listed without a model list", e.Entry)
		}
	}
	if m.Items[0].Entry != "claude-code@2.1.292/claude-haiku-5-5" || m.Items[0].Runs != 5 || !m.Items[0].Required {
		t.Errorf("retired entry = %+v", m.Items[0])
	}
}

func TestClusterUnavailable(t *testing.T) {
	scheme := newScheme(t)
	broken := errors.New("apiserver down")
	c := fakeClient(scheme, interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error { return broken },
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return broken
		},
	})
	h := newHarness(t, nil, worldHarbor(), worldStore(t))
	h.srv.cfg.Reader = c
	for _, target := range []string{"/overview", "/runs", "/runs/" + runClaude, "/trials", "/skills", "/skills/" + acme, "/models"} {
		if target == "/trials" {
			target = "/runs/" + runClaude + "/trials/rn-basic__sryqofs"
		}
		checkAPIError(t, h.get(apiv1.Prefix+target), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	}
	if rec := h.get(apiv1.Prefix + "/site"); rec.Code != 200 {
		t.Errorf("site needs no cluster read, got %d", rec.Code)
	}
}

func TestSessionsNeedTheSecret(t *testing.T) {
	h := newWorld(t)
	h.srv.secrets.reader = fakeClient(newScheme(t), interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("forbidden")
		},
	})
	checkAPIError(t, h.get(trialPath(runClaude, "rn-basic__sryqofs")), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	if rec := h.get(apiv1.Prefix + "/runs"); rec.Code != 200 {
		t.Errorf("lists still render with the Harbor credentials and patterns, got %d", rec.Code)
	}
}

func TestRenderTimeRedactionOfLists(t *testing.T) {
	h := newWorld(t)
	var se v1alpha1.SkillEval
	if err := h.client.Get(t.Context(), client.ObjectKey{Namespace: "skill-ci", Name: runUnscored}, &se); err != nil {
		t.Fatal(err)
	}
	se.Status.Message = "runner exited: key " + llmAnthropic + " rejected, registry password " + harborPassword
	if err := h.client.Status().Update(t.Context(), &se); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/overview", "/runs", "/runs/" + runUnscored} {
		body := h.get(apiv1.Prefix + target).Body.String()
		if strings.Contains(body, llmAnthropic) || strings.Contains(body, harborPassword) || !strings.Contains(body, session.RedactedMarker) {
			t.Errorf("%s does not redact the run message: %.300s", target, body)
		}
	}
	// A rotated key is picked up after the TTL.
	var sec corev1.Secret
	if err := h.client.Get(t.Context(), client.ObjectKey{Namespace: "skill-ci", Name: "llm-keys"}, &sec); err != nil {
		t.Fatal(err)
	}
	rotated := "sk-ant-test-rotated-00000000000000000000"
	sec.Data["anthropic"] = []byte(rotated)
	if err := h.client.Update(t.Context(), &sec); err != nil {
		t.Fatal(err)
	}
	se.Status.Message = "key " + rotated
	if err := h.client.Status().Update(t.Context(), &se); err != nil {
		t.Fatal(err)
	}
	now := testNow.Add(secretTTL + time.Second)
	h.srv.secrets.now = func() time.Time { return now }
	if body := h.get(runPath(runUnscored)).Body.String(); strings.Contains(body, rotated) {
		t.Errorf("the rotated key is not redacted")
	}
}

func TestReportFiles(t *testing.T) {
	h := newWorld(t)
	rec := h.get("/runs/" + runClaude + "/report")
	if rec.Code != 200 || rec.Body.String() != reportBody || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("report = %d %q", rec.Code, rec.Body.String())
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != "connect-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'" {
		t.Errorf("report CSP = %q", csp)
	}
	etag := rec.Header().Get("ETag")
	if again := h.do(http.MethodGet, "/runs/"+runClaude+"/report", http.Header{"If-None-Match": {etag}}); again.Code != http.StatusNotModified {
		t.Errorf("conditional report request = %d, want 304", again.Code)
	}
	rec = h.get("/runs/" + runClaude + "/result.json")
	if rec.Code != 200 || rec.Header().Get("Content-Disposition") != `attachment; filename="`+runClaude+`-result.json"` {
		t.Errorf("result.json = %d, %v", rec.Code, rec.Header())
	}
	for _, tt := range []struct {
		target string
		status int
	}{
		{"/runs/nope/report", 404},
		{"/runs/" + runRunning + "/report", 404},
		{"/runs/" + runCodex + "/report?job=" + runCodex, 404},
		{"/runs/" + runCodex + "/report?job=nope", 404},
		{"/runs/" + runCodex + "/report?job=" + runCodex + "-r1", 200},
		{"/runs/" + runCodex + "/report?other=1", 400},
	} {
		if rec := h.get(tt.target); rec.Code != tt.status {
			t.Errorf("%s = %d, want %d", tt.target, rec.Code, tt.status)
		}
	}
	h.store.err = errors.New("connection refused")
	if rec := h.get("/runs/" + runClaude + "/report"); rec.Code != http.StatusBadGateway {
		t.Errorf("report with the registry down = %d, want 502", rec.Code)
	}
}

func TestChannelsAreCachedBriefly(t *testing.T) {
	h := newWorld(t)
	counter := &countingHarbor{Client: worldHarbor()}
	h.srv.channels.client = counter
	now := testNow
	h.srv.channels.now = func() time.Time { return now }
	for range 3 {
		h.get(apiv1.Prefix + "/overview")
	}
	if n := counter.lists.Load(); n != 1 {
		t.Errorf("Harbor listed %d times within the TTL, want 1", n)
	}
	now = now.Add(channelTTL + 1)
	h.get(apiv1.Prefix + "/overview")
	if n := counter.lists.Load(); n != 2 {
		t.Errorf("Harbor listed %d times after the TTL, want 2", n)
	}
}

type countingHarbor struct {
	harbor.Client
	lists atomic.Int32
}

func (c *countingHarbor) ListRepositories(ctx context.Context) ([]string, error) {
	c.lists.Add(1)
	return c.Client.ListRepositories(ctx)
}

func TestStartServesAndShutsDown(t *testing.T) {
	h := newWorld(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	h.srv.cfg.Addr = addr
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- h.srv.Start(ctx) }()
	var resp *http.Response
	for range 50 { // wait for the listener
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err = http.DefaultClient.Do(req); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if resp == nil {
		t.Fatal("server did not start")
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz = %d", resp.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Start returned %v after shutdown", err)
	}
	if h.srv.NeedLeaderElection() {
		t.Error("every replica should serve the site")
	}
}

func TestByteCacheEvicts(t *testing.T) {
	c := newByteCache(100)
	for _, k := range []string{"a", "b", "c", "d"} {
		c.put(k, make([]byte, 22)) // 23 bytes each with the key
	}
	c.get("a")
	c.put("e", make([]byte, 22)) // over the limit: b goes, a was used more recently
	if _, ok := c.get("b"); ok {
		t.Error("the least recently used entry was kept")
	}
	if _, ok := c.get("a"); !ok {
		t.Error("a recently used entry was evicted")
	}
	c.put("big", make([]byte, 30))
	if _, ok := c.get("big"); ok {
		t.Error("an entry over a quarter of the limit was kept")
	}
}

func fakeClient(scheme *runtime.Scheme, funcs interceptor.Funcs) client.WithWatch {
	return fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(funcs).Build()
}
