package reportsite

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func serve(srv http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestSPAServesIndexForPagePaths(t *testing.T) {
	srv := newWorld(t).srv
	for _, path := range []string{"/", "/runs", "/skills", "/models", "/skills/acme-release-notes",
		"/runs/some-run", "/runs/some-run/trials/rn-basic__sryqofs", "/runs/some-run/cases/rn-basic/compare"} {
		rec := serve(srv, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
			continue
		}
		h := rec.Header()
		if got := h.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("GET %s Content-Type = %q", path, got)
		}
		if got := h.Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", path, got)
		}
		if got := h.Get("Content-Security-Policy"); got != SPACSP {
			t.Errorf("GET %s CSP = %q", path, got)
		}
		if !strings.Contains(rec.Body.String(), "<!doctype html>") {
			t.Errorf("GET %s did not return the app or the placeholder: %.80q", path, rec.Body.String())
		}
	}
}

func TestSPAKeepsReportFilesAndHealth(t *testing.T) {
	srv := newWorld(t).srv
	rec := serve(srv, http.MethodGet, "/runs/"+runClaude+"/report")
	if rec.Code != http.StatusOK || rec.Body.String() != reportBody {
		t.Errorf("report = %d %.60q, want the stored report", rec.Code, rec.Body.String())
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp == SPACSP {
		t.Errorf("the stored report must keep its own CSP, got the SPA one")
	}
	if rec := serve(srv, http.MethodGet, "/healthz"); rec.Body.String() != "ok" {
		t.Errorf("healthz = %q", rec.Body.String())
	}
}

func TestSPAAssets(t *testing.T) {
	a := &spa{files: fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>app</title>")},
		"assets/index-abc.js":  {Data: []byte("console.log(1)")},
		"assets/index-abc.css": {Data: []byte("body{}")},
		"favicon.svg":          {Data: []byte("<svg/>")},
	}, index: []byte("<!doctype html><title>app</title>"), built: true}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /assets/{path...}", a.asset)
	mux.HandleFunc("GET /", a.page)

	rec := serve(mux, http.MethodGet, "/assets/index-abc.js")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != cacheImmutable {
		t.Errorf("asset = %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("asset Content-Type = %q", ct)
	}
	if rec := serve(mux, http.MethodGet, "/assets/index-abc.css"); !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Errorf("css Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec := serve(mux, http.MethodGet, "/assets/missing.js"); rec.Code != http.StatusNotFound {
		t.Errorf("missing asset = %d, want 404 (never index.html)", rec.Code)
	}
	if rec := serve(mux, http.MethodGet, "/assets/../index.html"); rec.Code == http.StatusOK && rec.Header().Get("Cache-Control") == cacheImmutable {
		t.Errorf("path traversal served a file as an asset")
	}
	rec = serve(mux, http.MethodGet, "/favicon.svg")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("favicon = %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	rec = serve(mux, http.MethodGet, "/runs/x/trials/y")
	if rec.Body.String() != "<!doctype html><title>app</title>" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("history route = %q, Cache-Control %q", rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
}

// TestEmbeddedUI checks whatever the binary embeds: the placeholder says how
// to build the UI, and a built index references only embedded assets.
func TestEmbeddedUI(t *testing.T) {
	a, err := newSPA()
	if err != nil {
		t.Fatal(err)
	}
	if !a.built {
		if !strings.Contains(string(a.index), "make ui-build") {
			t.Errorf("placeholder does not say how to build the UI: %q", a.index)
		}
		return
	}
	refs := regexp.MustCompile(`(?:src|href)="/(assets/[^"]+)"`).FindAllStringSubmatch(string(a.index), -1)
	if len(refs) == 0 {
		t.Fatalf("built index.html references no assets: %q", a.index)
	}
	for _, ref := range refs {
		if _, err := fs.Stat(a.files, ref[1]); err != nil {
			t.Errorf("index.html references %s, which is not embedded: %v", ref[1], err)
		}
	}
	if regexp.MustCompile(`<script>|<script [^>]*>[^<]|<style`).MatchString(string(a.index)) {
		t.Errorf("built index.html has an inline script or style, which the CSP blocks: %q", a.index)
	}
}
