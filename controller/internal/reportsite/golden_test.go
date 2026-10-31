package reportsite

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// update rewrites the UI fixtures from the API: go test ./internal/reportsite/... -update
var update = flag.Bool("update", false, "rewrite ui/src/api/fixtures from the API responses")

// uiFixtures is where the UI reads its API fixtures.
const uiFixtures = "../../../ui/src/api/fixtures"

// goldens maps every UI fixture to the request that answers it in the test world.
var goldens = []struct {
	file, target string
	status       int
}{
	{"site.json", "/api/v1/site", http.StatusOK},
	{"overview.json", "/api/v1/overview", http.StatusOK},
	{"runs.json", "/api/v1/runs", http.StatusOK},
	{"runs-acme.json", "/api/v1/runs?skill=" + acme, http.StatusOK},
	{"run-claude-code.json", "/api/v1/runs/" + runClaude, http.StatusOK},
	{"run-codex.json", "/api/v1/runs/" + runCodex, http.StatusOK},
	{"run-unscored.json", "/api/v1/runs/" + runUnscored, http.StatusOK},
	{"run-fallback.json", "/api/v1/runs/" + runFallback, http.StatusOK},
	{"run-pending.json", "/api/v1/runs/" + runPending, http.StatusOK},
	{"skills.json", "/api/v1/skills", http.StatusOK},
	{"skill-acme-release-notes.json", "/api/v1/skills/" + acme, http.StatusOK},
	{"models.json", "/api/v1/models", http.StatusOK},
	{"trial-claude-code-with.json", "/api/v1/runs/" + runClaude + "/trials/rn-basic__sryqofs", http.StatusOK},
	{"trial-claude-code-without.json", "/api/v1/runs/" + runClaude + "/trials/rn-basic__BQbpoR9", http.StatusOK},
	{"trial-codex.json", "/api/v1/runs/" + runCodex + "/trials/rn-basic__Cx7aQ1p", http.StatusOK},
	{"trial-opencode.json", "/api/v1/runs/" + runOpencode + "/trials/rn-basic__Op4mZ8e", http.StatusOK},
	{"trial-unscored.json", "/api/v1/runs/" + runUnscored + "/trials/rn-basic__CbK6FQ2", http.StatusOK},
	{"compare-claude-code.json", "/api/v1/runs/" + runClaude + "/cases/rn-basic/compare", http.StatusOK},
	{"compare-codex-missing.json", "/api/v1/runs/" + runCodex + "/cases/rn-basic/compare", http.StatusOK},
	{"error-not-found.json", "/api/v1/runs/nope", http.StatusNotFound},
}

// TestGoldenFixtures keeps the UI fixtures equal to what the API answers in
// the test world. Run with -update to rewrite them after a change.
func TestGoldenFixtures(t *testing.T) {
	h := newWorld(t)
	var names []string
	for _, g := range goldens {
		names = append(names, g.file)
		rec := h.get(g.target)
		if rec.Code != g.status {
			t.Errorf("%s: GET %s = %d, want %d: %s", g.file, g.target, rec.Code, g.status, rec.Body.String())
			continue
		}
		var got bytes.Buffer
		if err := json.Indent(&got, rec.Body.Bytes(), "", "  "); err != nil {
			t.Errorf("%s: %v", g.file, err)
			continue
		}
		path := filepath.Join(uiFixtures, g.file)
		if *update {
			if err := os.WriteFile(path, got.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v (run go test ./internal/reportsite/... -update)", g.file, err)
			continue
		}
		if !bytes.Equal(want, got.Bytes()) {
			t.Errorf("%s is out of date; run go test ./internal/reportsite/... -update and check the diff", g.file)
		}
	}
	files, err := filepath.Glob(filepath.Join(uiFixtures, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !slices.Contains(names, filepath.Base(f)) {
			t.Errorf("%s is not made by the API; add it to goldens or remove it", filepath.Base(f))
		}
	}
}
