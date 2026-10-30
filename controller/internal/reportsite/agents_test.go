package reportsite

import (
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/dirien/skill-ci/controller/internal/evals"
)

// agentSpecs run one skill version on the same model under three harnesses:
// Claude Code twice (two releases), and OpenCode, whose release is not known.
func agentSpecs() []spec {
	mk := func(agent, version, with, lift string, required bool) spec {
		s := ok("acme", "v1.0.0", opus, required, "push", at(13, 2), with, "0.36", lift, "Helps")
		s.agent, s.agentVersion = agent, version
		return s
	}
	return []spec{
		mk("claude-code", "2.1.292", "0.95", "+0.59", true),
		mk("claude-code", "2.1.300", "0.80", "+0.44", true),
		mk("opencode", "", "0.70", "+0.34", false),
	}
}

func TestAgentVersionShownEverywhere(t *testing.T) {
	h := newHarness(t, agentSpecs())
	run := "/runs/" + evals.Name("acme", "v1.0.0", "claude-code", "2.1.300", opus)
	tests := []struct {
		name   string
		path   string
		want   []string
		absent []string
	}{
		{name: "runs table", path: "/",
			want: []string{"claude-code@2.1.292", "claude-code@2.1.300", ">opencode<"}},
		{name: "matrix has one column per agent version", path: "/skills/acme",
			want: []string{"claude-code@2.1.292 · required", "claude-code@2.1.300 · required", "opencode · optional"}},
		{name: "run page", path: run,
			want: []string{"claude-code@2.1.300", "Agent version", "2.1.300"}},
		{name: "filter by harness", path: "/?agent=claude-code@2.1.300",
			want: []string{"1 of 3 runs", "claude-code@2.1.300"}, absent: []string{"claude-code@2.1.292</td>"}},
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

func TestRunNamesTheVersionTheTrialsRan(t *testing.T) {
	s := agentSpecs()[0]
	se := s.object()
	se.Status.AgentVersion = "2.1.299"
	if got := newRun(se, "95%"); !got.VersionDiffers() || got.RanVersion != "2.1.299" || got.AgentVersion != "2.1.292" {
		t.Errorf("run = %+v, want the pinned 2.1.292 and the reported 2.1.299", got)
	}
	se.Status.AgentVersion = "2.1.292"
	if newRun(se, "95%").VersionDiffers() {
		t.Error("the same version twice is no difference")
	}
}
