package reportsite

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/evals"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
	"github.com/dirien/skill-ci/controller/internal/session"
)

// canaryDir holds a trial whose agent printed fake keys in every form, with no redaction.
var canaryDir = filepath.Join(testdata, "trials-secrets", "canary")

// canaryValues reads the fake values of secrets.env.
func canaryValues(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(canaryDir, "secrets.env"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if name, value, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			out[name] = value
		}
	}
	if len(out) < 8 {
		t.Fatalf("secrets.env has %d values", len(out))
	}
	return out
}

// forms are the encodings a value must not appear in.
func forms(v string) []string {
	j, _ := json.Marshal(v)
	return []string{
		v, base64.StdEncoding.EncodeToString([]byte(v)), base64.RawStdEncoding.EncodeToString([]byte(v)),
		base64.URLEncoding.EncodeToString([]byte(v)), url.QueryEscape(v), string(j[1 : len(j)-1]),
	}
}

// TestCanaryLeaksNothing serves the unredacted canary bundle with the llm-keys
// values and the Harbor credentials of secrets.env, and checks that no form of
// any value reaches any response: the lists, the run, the session, every
// tool output, the trajectory and the comparison.
func TestCanaryLeaksNothing(t *testing.T) {
	values := canaryValues(t)
	idx, err := session.DecodeIndex(readFile(t, filepath.Join(canaryDir, "bundle-unredacted", session.IndexFile)))
	if err != nil {
		t.Fatal(err)
	}
	name := evals.Name("canary", "v0.0.1", idx.Agent, idx.AgentVersion, idx.Model)
	report, bundle := sum("report/canary"), sum("bundle/canary")
	store := newFakeStore()
	store.report(report, minimalResult(idx.Agent), map[string]string{session.AnnotationTrials: bundle})
	store.bundle(t, bundle, filepath.Join(canaryDir, "bundle-unredacted"))
	se := &v1alpha1.SkillEval{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "skill-ci", CreationTimestamp: metav1.NewTime(testNow.Add(-3600e9))},
		Spec: v1alpha1.SkillEvalSpec{Skill: "canary", Version: "v0.0.1", Digest: idx.Digest, Agent: idx.Agent, AgentVersion: idx.AgentVersion,
			Model: idx.Model, Required: true, Attempts: 1, Trigger: v1alpha1.TriggerPush},
		Status: v1alpha1.SkillEvalStatus{Phase: v1alpha1.PhaseFailed, JobName: name, ReportDigest: report,
			// A runner error that quotes a key: the run views redact it too.
			Message: "runner exited: " + values["ANTHROPIC_API_KEY"] + " rejected"},
	}
	keys := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "llm-keys", Namespace: "skill-ci"}, Data: map[string][]byte{
		"anthropic": []byte(values["ANTHROPIC_API_KEY"]), "openai": []byte(values["OPENAI_API_KEY"]),
		"ollama": []byte(values["OLLAMA_API_KEY"]), "openrouter": []byte(values["OPENROUTER_API_KEY"]),
		"nvidia": []byte(values["NVIDIA_API_KEY"]),
	}}
	h := newHarness(t, []client.Object{se, keys}, harbor.NewFake(nil), store)
	h.srv.secrets.harborUser, h.srv.secrets.harborPw = "robot$skillci+runner", values["HARBOR_PASSWORD"]

	targets := []string{
		apiv1.Prefix + "/overview", apiv1.Prefix + "/runs", runPath(name), apiv1.Prefix + "/skills/canary",
		runPath(name) + "/cases/" + idx.Cases[0].ID + "/compare",
	}
	for _, e := range idx.Trials {
		base := trialPath(name, e.ID)
		targets = append(targets, base, base+"/trajectory.json")
		rec := h.get(base)
		if rec.Code != http.StatusOK {
			t.Fatalf("session %s = %d: %s", e.ID, rec.Code, rec.Body.String())
		}
		var view apiv1.TrialSession
		decode(t, rec, &view)
		if view.Redaction.Render == 0 {
			t.Errorf("session %s: render-time redaction counted nothing on an unredacted bundle", e.ID)
		}
		for _, entry := range view.Log.Entries {
			if entry.Tool != nil && entry.Tool.Output != nil {
				targets = append(targets, entry.Tool.Output.Href)
			}
		}
	}
	if len(targets) < 10 {
		t.Fatalf("only %d targets; the canary should have tool outputs", len(targets))
	}
	for _, target := range targets {
		rec := h.get(target)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d: %.200s", target, rec.Code, rec.Body.String())
			continue
		}
		body := rec.Body.String()
		for secret, v := range values {
			for i, f := range forms(v) {
				if len(f) >= 8 && strings.Contains(body, f) {
					t.Errorf("GET %s holds %s (form %s)", target, secret, strconv.Itoa(i))
				}
			}
		}
	}
}
