package reportsite

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dirien/skill-ci/controller/internal/evals"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/reportstore"
)

func runPath(skill, version, model string) string {
	return "/runs/" + evals.Name(skill, version, model)
}

func TestPages(t *testing.T) {
	h := newHarness(t, demoSpecs())
	reported := runPath("acme-release-notes", "v1.1.0", haiku)
	tests := []struct {
		name   string
		path   string
		status int
		want   []string
		absent []string
	}{
		{
			name: "runs page counts, channels and every run", path: "/", status: http.StatusOK,
			want: []string{
				"<dt>Runs</dt><dd>17</dd>", "<dt>Succeeded</dt><dd>14</dd>", "<dt>Failed</dt><dd>1</dd>", "<dt>Skills</dt><dd>3</dd>",
				"<dt>Versions on stable</dt><dd>2</dd>", "<dt>Held back</dt><dd>1</dd>",
				"Started (UTC)", "2026-10-30 14:27", "3m", "not stored", reported + "/report", ">Regressed<", ">Helps<", "+0.58",
				`<a href="/skills/acme-release-notes">`, `aria-label="Breadcrumb"`,
			},
		},
		{
			name: "filter by skill", path: "/?skill=python-docstrings", status: http.StatusOK,
			want:   []string{"3 of 17 runs", "Clear filters", `<option value="python-docstrings" selected>`},
			absent: []string{"No runs match"},
		},
		{name: "filter by verdict", path: "/?verdict=Regressed", status: http.StatusOK, want: []string{"2 of 17 runs"}},
		{name: "filter by phase", path: "/?phase=Failed", status: http.StatusOK, want: []string{"1 of 17 runs", "ANTHROPIC_API_KEY rejected"}},
		{name: "filter by model", path: "/?model=" + opus, status: http.StatusOK, want: []string{"5 of 17 runs"}},
		{name: "filters combine", path: "/?skill=acme-release-notes&verdict=Regressed&model=" + haiku, status: http.StatusOK, want: []string{"1 of 17 runs"}},
		{name: "filter without matches", path: "/?skill=nope", status: http.StatusOK, want: []string{"No runs match these filters", "0 of 17 runs"}},
		{
			name: "skill page decisions", path: "/skills/acme-release-notes", status: http.StatusOK,
			want: []string{
				"Held back", "claude-code/claude-haiku-5-5 scores 0.69 with v1.2.0, stable v1.1.0 scored 0.95",
				"On stable", "Lift is +0.58 or more on every required model (claude-haiku-5-5 lowest).",
				"Meets threshold", "v1.1.0 holds stable", "Waiting", "0 of 2 required models have finished.",
				">stable<", ">beta<", "with 0.95 · without 0.37", "optional", "needs lift +0.10 on every required model and no regression",
			},
		},
		{
			name: "skill page below threshold", path: "/skills/python-docstrings", status: http.StatusOK,
			want: []string{"Not promoted", "Stable needs +0.10 on every required model; below it: claude-haiku-5-5 +0.03."},
		},
		{name: "unknown skill", path: "/skills/nope", status: http.StatusNotFound, want: []string{"Skill not found", `aria-label="Breadcrumb"`}},
		{
			name: "run with report shows the six dimensions", path: reported, status: http.StatusOK,
			want: []string{
				"Accuracy", "Goal accuracy", "Behavior check", "Skill execution", "Skill efficiency", "Security", "Overall",
				"0.08", "+0.92", "+1.00", "Open full SkillEvaluator report", "Download result.json", reported + "/result.json",
				"activated (3 of 3 trials)", "2026-10-30 13:25:00 UTC", "6m 20s", "sha256:", ">stable<",
			},
			absent: []string{"No report stored"},
		},
		{
			name: "run without report", path: runPath("python-docstrings", "v1.0.0", haiku), status: http.StatusOK,
			want:   []string{"No report stored for this run. Runs on runner versions before 0.3.0 did not attach one."},
			absent: []string{"Open full SkillEvaluator report"},
		},
		{
			name: "failed run", path: runPath("k8s-resource-naming", "v1.0.0", opus), status: http.StatusOK,
			want: []string{"ANTHROPIC_API_KEY rejected", "Failed", "the run failed before one was attached"},
		},
		{
			name: "regressed run shows its condition", path: runPath("acme-release-notes", "v1.2.0", haiku), status: http.StatusOK,
			want: []string{"WorseThanStable", "stable v1.1.0 scored 0.95"},
		},
		{
			name: "running run", path: runPath("acme-release-notes", "v1.3.0", haiku), status: http.StatusOK,
			want: []string{"Running", "not yet", "The report is attached when the run finishes."},
		},
		{name: "unknown run", path: "/runs/nope", status: http.StatusNotFound, want: []string{"Run not found", `aria-label="Breadcrumb"`}},
		{name: "report of unknown run", path: "/runs/nope/report", status: http.StatusNotFound, want: []string{"Run not found"}},
		{name: "report of run without one", path: runPath("python-docstrings", "v1.0.0", haiku) + "/report", status: http.StatusNotFound, want: []string{"No report stored"}},
		{name: "unknown path", path: "/nowhere", status: http.StatusNotFound, want: []string{"Page not found", "/nowhere"}},
		{name: "health", path: "/healthz", status: http.StatusOK, want: []string{"ok"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.get(tt.path)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			body := html.UnescapeString(rec.Body.String()) // html/template writes + as &#43;
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

func TestReportPassthrough(t *testing.T) {
	h := newHarness(t, demoSpecs())
	path := runPath("acme-release-notes", "v1.1.0", haiku)

	rec := h.get(path + "/report")
	if rec.Code != http.StatusOK {
		t.Fatalf("report status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("report content type = %q", ct)
	}
	if rec.Body.String() != reportBody {
		t.Errorf("report body was changed: %q", rec.Body.String())
	}

	cached := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path+"/report", nil)
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	h.srv.ServeHTTP(cached, req)
	if cached.Code != http.StatusNotModified {
		t.Errorf("conditional report request = %d, want 304", cached.Code)
	}

	rec = h.get(path + "/result.json")
	if rec.Code != http.StatusOK {
		t.Fatalf("result status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("result content type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment; filename=") || !strings.Contains(cd, "result.json") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Errorf("result.json is not valid JSON: %v", err)
	}
}

func TestReportStoreFailures(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		runStatus    int
		runWant      string
		reportStatus int
	}{
		{"report gone from Harbor", reportstore.ErrNotFound, http.StatusOK, "The report is no longer in Harbor", http.StatusNotFound},
		{"Harbor unreachable", errors.New("dial tcp: connection refused"), http.StatusOK, "Could not read the report from Harbor: dial tcp", http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, demoSpecs())
			h.reports.err = tt.err
			path := runPath("acme-release-notes", "v1.1.0", sonnet)
			if rec := h.get(path); rec.Code != tt.runStatus || !strings.Contains(rec.Body.String(), tt.runWant) {
				t.Errorf("run page: status %d, body has %q = %v", rec.Code, tt.runWant, strings.Contains(rec.Body.String(), tt.runWant))
			}
			for _, suffix := range []string{"/report", "/result.json"} {
				if rec := h.get(path + suffix); rec.Code != tt.reportStatus {
					t.Errorf("%s status = %d, want %d", suffix, rec.Code, tt.reportStatus)
				}
			}
		})
	}
}

func TestMalformedResultIsQuiet(t *testing.T) {
	specs := demoSpecs()
	h := newHarness(t, specs)
	for key := range h.reports.files {
		if strings.HasSuffix(key, "/"+reportstore.FileResult) {
			h.reports.files[key] = []byte("{not json")
		}
	}
	rec := h.get(runPath("acme-release-notes", "v1.1.0", haiku))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Could not read the report from Harbor") {
		t.Errorf("status %d, body: %s", rec.Code, rec.Body.String())
	}
}

func TestHarborDown(t *testing.T) {
	h := newHarness(t, demoSpecs())
	h.harbor.Err = errors.New("harbor: HTTP 503")
	for _, path := range []string{"/", "/skills/acme-release-notes"} {
		rec := h.get(path)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, "Release channels unavailable") {
			t.Errorf("%s: status %d, notice shown = %v", path, rec.Code, strings.Contains(body, "Release channels unavailable"))
		}
		if strings.Contains(body, "On stable") || strings.Contains(body, "holds stable") {
			t.Errorf("%s claims a channel state without Harbor", path)
		}
	}
	if body := h.get("/skills/acme-release-notes").Body.String(); !strings.Contains(body, "Release channels are unavailable") {
		t.Error("a version that meets the threshold must say the tag is unknown")
	}
	if body := h.get("/").Body.String(); !strings.Contains(body, "<dt>Versions on stable</dt><dd>–</dd>") {
		t.Error("stable count should be a dash without Harbor")
	}
}

func TestTemplatesRenderEmptyAndFailed(t *testing.T) {
	failedOnly := []spec{
		{skill: "acme", version: "v1.0.0", model: haiku, required: true, trigger: "push", started: at(13, 0), took: 30e9, phase: "Failed", message: "boom <script>alert(1)</script>"},
		{skill: "acme", version: "v1.0.0", model: sonnet, required: true, trigger: "push", started: at(13, 0), took: 30e9, phase: "Failed", message: "boom"},
	}
	tests := []struct {
		name  string
		specs []spec
		want  map[string]string // path to expected text
	}{
		{"no runs", nil, map[string]string{"/": "No SkillEvals in", "/skills/acme-release-notes": "No evaluations for"}},
		{"only failed runs", failedOnly, map[string]string{
			"/": "boom", "/skills/acme": "An eval failed on claude-haiku-5-5, claude-sonnet-5-5, so the policy did not decide.",
			runPath("acme", "v1.0.0", haiku): "No report stored: the run failed",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.specs)
			for path, want := range tt.want {
				rec := h.get(path)
				if rec.Code != http.StatusOK {
					t.Errorf("%s: status %d", path, rec.Code)
				}
				if !strings.Contains(rec.Body.String(), want) {
					t.Errorf("%s lacks %q", path, want)
				}
				if strings.Contains(rec.Body.String(), "<script>alert") {
					t.Errorf("%s did not escape the run message", path)
				}
			}
		})
	}
}

func TestHeadersAndAutoRefresh(t *testing.T) {
	h := newHarness(t, demoSpecs())
	rec := h.get("/")
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff", "Content-Type": "text/html; charset=utf-8",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") {
		t.Errorf("Content-Security-Policy = %q", csp)
	}
	if !strings.Contains(rec.Body.String(), `<meta http-equiv="refresh" content="15">`) {
		t.Error("a page with active runs should reload itself")
	}
	idle := newHarness(t, demoSpecs()[:3])
	if strings.Contains(idle.get("/").Body.String(), "http-equiv") {
		t.Error("a page without active runs should not reload itself")
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := newHarness(t, nil)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", rec.Code)
	}
}

// countingHarbor counts listing calls.
type countingHarbor struct {
	harbor.Client
	lists atomic.Int32
}

func (c *countingHarbor) ListRepositories(ctx context.Context) ([]string, error) {
	c.lists.Add(1)
	return c.Client.ListRepositories(ctx)
}

func TestChannelsAreCachedBriefly(t *testing.T) {
	h := newHarness(t, demoSpecs())
	counter := &countingHarbor{Client: demoHarbor()}
	h.srv.channels.client = counter
	now := testNow
	h.srv.channels.now = func() time.Time { return now }
	for range 3 {
		h.get("/")
	}
	if n := counter.lists.Load(); n != 1 {
		t.Errorf("Harbor listed %d times within the TTL, want 1", n)
	}
	now = now.Add(channelTTL + 1)
	h.get("/")
	if n := counter.lists.Load(); n != 2 {
		t.Errorf("Harbor listed %d times after the TTL, want 2", n)
	}
}

func TestStartServesAndShutsDown(t *testing.T) {
	h := newHarness(t, nil)
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
