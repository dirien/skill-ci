package reportsite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/evals"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/platform"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/reportstore"
	"github.com/dirien/skill-ci/controller/internal/session"
)

// testdata is the repository's shared fixture directory.
const testdata = "../../../testdata"

// testNow is the clock of all tests: 31 Oct 2026, 09:00 UTC.
var testNow = time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)

// Skills, models and agents of the test world.
const (
	acme   = "acme-release-notes"
	k8s    = "k8s-resource-naming"
	pydoc  = "python-docstrings"
	haiku  = "claude-haiku-5-5"
	gpt    = "openai/gpt-6.1-sol"
	oss    = "ollama-cloud/gpt-oss:120b"
	claude = "claude-code"
	codex  = "codex"
	opencd = "opencode"
)

// Skill digests. The acme ones are those of the golden bundles in testdata/trials.
const (
	acmeV100 = "sha256:0fb7e3f4ceabce11e194bc4ba99225d48d368511c4f3e32d02701d965bc0d890"
	acmeV110 = "sha256:e2083e2538efcb137f3375686cca135d50b93c5c9494f73dca707621ba05b490"
	acmeV120 = "sha256:bdda6c2ddfaa0b9489e7d77dd3432c630545c7588aa4bc445f8b56caf0e3df1d"
	k8sV100  = "sha256:c1a8151ffefab1c0039287867675f25d13cc6f7bd060575f98c4bad82a3ae8fe"
	pyV110   = "sha256:9b9e4d4dd1b9f55c6b4a7ac38482d0473398d39fe73358ffc34e08cbf6c95886"
)

// Fake credentials of the test world; none is real.
const (
	harborUser     = "robot$skillci+controller"
	harborPassword = "Harbor-golden-pass-0000"
	llmAnthropic   = "sk-ant-test-golden-000000000000000000000000"
	llmOpenAI      = "sk-test-golden-openai-0000000000000000"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

// Runs of the test world.
var (
	runClaude   = evals.Name(acme, "v1.1.0", claude, "2.1.292", haiku)
	runCodex    = evals.Name(acme, "v1.1.0", codex, "0.161.0", gpt)
	runOpencode = evals.Name(acme, "v1.1.0", opencd, "1.19.1", oss)
	runUnscored = evals.Name(acme, "v1.2.0", claude, "2.1.292", haiku)
	runFallback = evals.Name(acme, "v1.0.0", claude, "2.1.292", haiku)
	runRejected = evals.Name(pydoc, "v1.1.0", claude, "2.1.292", haiku)
	runRunning  = evals.Name(k8s, "v1.0.0", claude, "2.1.292", haiku)
	runPending  = evals.Name(k8s, "v1.0.0", codex, "0.161.0", gpt)
)

// Report and bundle digests. The first ones are those of the hand-made UI fixtures.
var (
	reportClaude   = "sha256:93c33c7ad6450b53bcbf2b20f7a45c3ee3849744fbfaba4b2f9a85d52c58e697"
	bundleClaude   = "sha256:66e6dc529d1583acc413cf0d44bcd465bdb01fa0cb520fced896112016e377c4"
	reportCodex    = "sha256:effa499cae50e035768d99c08b16c268d987316cf6f3ab0ab26b74dac33befcb"
	bundleCodex    = "sha256:b397d50c62b1a2f608990d862a21285d0782c0fd346de235895c84214fbdad2d"
	reportUnscored = "sha256:aa7db62b391210b83ecacab22acfdf2c218b8524671c13efda1f52d5babf36dd"
	bundleUnscored = "sha256:a33872450416d675449128837155d3515dec9e149fdd31b70432ecd25f6adf59"
	reportFallback = "sha256:c6e95d09b3eae8101f9feecbf0c24657dff05c0f1d45e6f51de21ed4ad8d94cd"
	reportOpencode = sum("report/" + runOpencode)
	bundleOpencode = sum("bundle/" + runOpencode)
	reportRejected = sum("report/" + runRejected)
)

const reportBody = "<!DOCTYPE html><html><head><title>SkillEvaluator Validation Report</title></head><body>report</body></html>"

// fakeStore is the registry of the tests: report files, manifest annotations
// and gzip layers (stored decompressed), keyed by manifest digest. It
// implements ReportStore and BundleStore.
type fakeStore struct {
	mu          sync.Mutex
	files       map[string][]byte
	annotations map[string]map[string]string
	layers      map[string][]byte
	// err fails every call when set.
	err error
	// gunzips counts Gunzip calls, to see the response cache work.
	gunzips int
}

func newFakeStore() *fakeStore {
	return &fakeStore{files: map[string][]byte{}, annotations: map[string]map[string]string{}, layers: map[string][]byte{}}
}

func (f *fakeStore) File(_ context.Context, _, digest, name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if data, ok := f.files[digest+"/"+name]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("fake store %s/%s: %w", digest, name, reportstore.ErrNotFound)
}

func (f *fakeStore) Annotations(_ context.Context, _, digest string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if a, ok := f.annotations[digest]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("fake store manifest %s: %w", digest, reportstore.ErrNotFound)
}

func (f *fakeStore) Gunzip(_ context.Context, _, digest, name string, limit int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gunzips++
	if f.err != nil {
		return nil, f.err
	}
	data, ok := f.layers[digest+"/"+name]
	if !ok {
		return nil, fmt.Errorf("fake store %s/%s: %w", digest, name, reportstore.ErrNotFound)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("fake store %s/%s: %w", digest, name, reportstore.ErrTooLarge)
	}
	return data, nil
}

// report stores a report referrer: its files and annotations.
func (f *fakeStore) report(digest string, result []byte, ann map[string]string) {
	f.files[digest+"/"+reportstore.FileReport] = []byte(reportBody)
	if result != nil {
		f.files[digest+"/"+reportstore.FileResult] = result
	}
	if ann == nil {
		ann = map[string]string{}
	}
	f.annotations[digest] = ann
}

// bundle stores a bundle from a testdata directory: index.json and one layer
// per trial-<id>.json, compacted as the runner serializes it.
func (f *fakeStore) bundle(t *testing.T, digest, dir string) {
	t.Helper()
	f.files[digest+"/"+session.IndexFile] = readFile(t, filepath.Join(dir, session.IndexFile))
	f.annotations[digest] = map[string]string{session.AnnotationTrialsSchema: "1", session.AnnotationRedaction: "v1"}
	docs, err := filepath.Glob(filepath.Join(dir, "trial-*.json"))
	if err != nil || len(docs) == 0 {
		t.Fatalf("no trial documents in %s: %v", dir, err)
	}
	for _, doc := range docs {
		var buf bytes.Buffer
		if err := json.Compact(&buf, readFile(t, doc)); err != nil {
			t.Fatal(err)
		}
		f.layers[digest+"/"+filepath.Base(doc)+".gz"] = buf.Bytes()
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// minimalResult is a result.json of an agent without dimension scores.
func minimalResult(agent string) []byte {
	return []byte(`{"agents":{"` + agent + `":{"lift":{},"skill_activation":{"status":"activated","positive_trials":1,"activated_trials":1}}}}`)
}

// fallbackResult is a result.json of a runner before trial bundles: three
// attempts per arm in pass_at_k and the six dimensions.
func fallbackResult() []byte {
	names := []string{"accuracy", "goal_accuracy", "behavior_check", "skill_execution", "skill_efficiency", "security"}
	with := []float64{1, 1, 1, 1, 0.75, 1}
	without := []float64{0.4, 0.08, 0.28, 0, 0.25, 1}
	var lift []string
	for i, n := range names {
		lift = append(lift, fmt.Sprintf(`%q:{"with_skill":%v,"without_skill":%v,"delta":%.2f,"direction":"up"}`, n, with[i], without[i], with[i]-without[i]))
	}
	lift = append(lift, `"overall":{"with_skill":0.95,"without_skill":0.37,"delta":0.58}`)
	attempts := func(ids []string, scores []float64, threshold float64) string {
		var out []string
		for i, id := range ids {
			out = append(out, fmt.Sprintf(`{"attempt":%d,"trial":%q,"score":%v,"passed":%v}`, i+1, id, scores[i], scores[i] >= threshold))
		}
		return `{"pass_threshold":0.5,"cases":{"rn-basic":{"attempts":[` + strings.Join(out, ",") + `]}}}`
	}
	return []byte(`{"agents":{"claude-code":{"lift":{` + strings.Join(lift, ",") + `},` +
		`"skill_activation":{"status":"activated","positive_trials":3,"activated_trials":3},` +
		`"pass_at_k":{"with_skill":` + attempts([]string{"rn-basic__Fw1qa2b", "rn-basic__Fw2kd7e", "rn-basic__Fw3pz5c"}, []float64{0.95, 0.9, 1}, 0.5) +
		`,"without_skill":` + attempts([]string{"rn-basic__Fo1mx4r", "rn-basic__Fo2vb8n", "rn-basic__Fo3ty6s"}, []float64{0.36, 0.38, 0.37}, 0.5) + `},` +
		`"trial_failures":{"with_skill":[],"without_skill":[]}}}}`)
}

// evalSpec describes one SkillEval of the test world.
type evalSpec struct {
	skill, version, digest, agent, agentVersion, model string
	required                                           bool
	trigger                                            v1alpha1.Trigger
	attempts                                           int
	created, completed                                 time.Time
	phase                                              v1alpha1.Phase
	verdict                                            v1alpha1.Verdict
	with, without, lift                                string
	n, ci, withErr, withoutErr                         string
	samples                                            [2][]string
	tiers                                              *v1alpha1.TierStatuses
	publication                                        string
	findings                                           []string
	message                                            string
	reportDigest                                       string
	pushedBy                                           string
	network                                            *v1alpha1.NetworkSpec
	netStatus                                          *v1alpha1.NetworkStatus
	conditions                                         []metav1.Condition
	retries                                            int
	earlier                                            []v1alpha1.RunAttempt
}

func (s evalSpec) name() string {
	return evals.Name(s.skill, s.version, s.agent, s.agentVersion, s.model)
}

func (s evalSpec) object() *v1alpha1.SkillEval {
	name := s.name()
	se := &v1alpha1.SkillEval{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "skill-ci", CreationTimestamp: metav1.NewTime(s.created)},
		Spec: v1alpha1.SkillEvalSpec{
			Skill: s.skill, Version: s.version, Digest: s.digest, Agent: s.agent, AgentVersion: s.agentVersion,
			Model: s.model, Required: s.required, Attempts: max(s.attempts, 1), Trigger: s.trigger, Network: s.network,
		},
		Status: v1alpha1.SkillEvalStatus{
			Phase: s.phase, JobName: name, WithSkill: s.with, WithoutSkill: s.without, Lift: s.lift, Verdict: s.verdict,
			N: s.n, LiftCI: s.ci, WithSkillStdErr: s.withErr, WithoutSkillStdErr: s.withoutErr,
			Tiers: s.tiers, Publication: s.publication, Findings: s.findings, Message: s.message,
			ReportDigest: s.reportDigest, Network: s.netStatus, Conditions: s.conditions,
			Retries: s.retries, Attempts: s.earlier,
		},
	}
	if s.pushedBy != "" {
		se.Annotations = map[string]string{v1alpha1.AnnotationPushedBy: s.pushedBy}
	}
	if s.retries > 0 {
		se.Status.JobName = fmt.Sprintf("%s-r%d", name, s.retries)
	}
	if s.phase != v1alpha1.PhasePending && s.phase != v1alpha1.PhaseRunning {
		se.Status.AgentVersion = s.agentVersion
	}
	se.Spec.SandboxProfile, se.Status.SandboxProfile, se.Status.WarmPool = "default", "default", "default"
	if !s.completed.IsZero() {
		done := metav1.NewTime(s.completed)
		se.Status.CompletedAt = &done
	}
	if s.samples[0] != nil {
		se.Status.Samples = &v1alpha1.Samples{WithSkill: s.samples[0], WithoutSkill: s.samples[1]}
	}
	return se
}

func cond(typ, reason, message string, at time.Time) metav1.Condition {
	return metav1.Condition{Type: typ, Status: metav1.ConditionTrue, Reason: reason, Message: message, LastTransitionTime: metav1.NewTime(at)}
}

func enforced(name, mode string, at time.Time) metav1.Condition {
	return cond(conditionNetworkPolicy, "Enforced", "network "+mode+" enforced by CiliumNetworkPolicy skillci-net-"+name, at)
}

var (
	passedTiers = &v1alpha1.TierStatuses{Tier1: "passed", Tier2: "passed", Tier3: "passed"}
	pyFinding   = "tier1/security: hardcoded credential in scripts/deploy.sh"
	retryReason = "EnvironmentStartTimeoutError: sandbox rn-basic__Hx3kLm9 did not become ready in 600s"
	unscoredMsg = "no scores in result.json: Harbor job did not complete successfully: 1 errored; " +
		"Unscoreable reward in rn-basic__CbK6FQ2: RuntimeError: Build job img-build-skillevaluator-rn-basic-with-skill failed."
	pythonHosts = []string{"api.anthropic.com", "api.openai.com", "files.pythonhosted.org", "pypi.org"}
)

func ts(month time.Month, day, hour, minute, second int) time.Time {
	return time.Date(2026, month, day, hour, minute, second, 0, time.UTC)
}

// worldSpecs are the eight runs of the UI fixtures: three agents on acme
// v1.1.0 (claude-code scored, codex retried with a withheld trial, opencode),
// a failed run with unscored trials, a run from before trial bundles, a run
// rejected by Tier 1, one that is still running and one waiting for a slot.
func worldSpecs() []evalSpec {
	full := &v1alpha1.NetworkSpec{Mode: "full", Source: "default"}
	python := &v1alpha1.NetworkSpec{Mode: "allowlist", Source: "skill", Allow: []string{"@python"}}
	start := ts(10, 30, 14, 8, 2)
	return []evalSpec{
		{
			skill: acme, version: "v1.1.0", digest: acmeV110, agent: claude, agentVersion: "2.1.292", model: haiku, required: true,
			trigger: v1alpha1.TriggerPush, created: start, completed: ts(10, 30, 14, 21, 31), phase: v1alpha1.PhaseSucceeded,
			verdict: v1alpha1.VerdictHelps, with: "0.95", without: "0.36", lift: "+0.59", samples: [2][]string{{"0.95"}, {"0.36"}},
			tiers: passedTiers, publication: "passed", reportDigest: reportClaude, pushedBy: "release-bot", network: full,
			netStatus:  &v1alpha1.NetworkStatus{Mode: "full", Policy: "skillci-net-" + runClaude, Enforced: true},
			conditions: []metav1.Condition{enforced(runClaude, "full", start.Add(time.Second))},
		},
		{
			skill: acme, version: "v1.1.0", digest: acmeV110, agent: codex, agentVersion: "0.161.0", model: gpt,
			trigger: v1alpha1.TriggerPush, created: start, completed: ts(10, 30, 15, 3, 40), phase: v1alpha1.PhaseSucceeded,
			verdict: v1alpha1.VerdictNeutral, with: "0.92", without: "0.88", lift: "+0.04", samples: [2][]string{{"0.92"}, {"0.88"}},
			tiers: passedTiers, publication: "passed", reportDigest: reportCodex, pushedBy: "release-bot", network: python,
			netStatus: &v1alpha1.NetworkStatus{Mode: "allowlist", Policy: "skillci-net-" + runCodex, Enforced: true, Hosts: pythonHosts},
			conditions: []metav1.Condition{
				cond(conditionRetried, "InfrastructureFailure", retryReason, ts(10, 30, 14, 19, 30)),
				enforced(runCodex, "allowlist", start.Add(time.Second)),
			},
			retries: 1, earlier: []v1alpha1.RunAttempt{{JobName: runCodex, Message: retryReason}},
		},
		{
			skill: acme, version: "v1.1.0", digest: acmeV110, agent: opencd, agentVersion: "1.19.1", model: oss,
			trigger: v1alpha1.TriggerPush, created: start, completed: ts(10, 30, 16, 11, 2), phase: v1alpha1.PhaseSucceeded,
			verdict: v1alpha1.VerdictHelps, with: "0.92", without: "0.41", lift: "+0.51", samples: [2][]string{{"0.92"}, {"0.41"}},
			tiers: passedTiers, publication: "passed", reportDigest: reportOpencode, pushedBy: "release-bot", network: python,
			netStatus:  &v1alpha1.NetworkStatus{Mode: "allowlist", Policy: "skillci-net-" + runOpencode, Enforced: true, Hosts: pythonHosts},
			conditions: []metav1.Condition{enforced(runOpencode, "allowlist", start.Add(time.Second))},
		},
		{
			skill: acme, version: "v1.2.0", digest: acmeV120, agent: claude, agentVersion: "2.1.292", model: haiku, required: true,
			trigger: v1alpha1.TriggerPush, created: ts(10, 30, 14, 11, 40), completed: ts(10, 30, 14, 13, 20), phase: v1alpha1.PhaseFailed,
			tiers: &v1alpha1.TierStatuses{Tier1: "passed", Tier2: "passed", Tier3: "incomplete"}, message: unscoredMsg,
			findings:     []string{"tier3/execution: Harbor job did not complete successfully: 1 errored"},
			reportDigest: reportUnscored, pushedBy: "release-bot", network: full,
			netStatus:  &v1alpha1.NetworkStatus{Mode: "full", Policy: "skillci-net-" + runUnscored, Enforced: true},
			conditions: []metav1.Condition{enforced(runUnscored, "full", ts(10, 30, 14, 11, 41))},
		},
		{
			skill: acme, version: "v1.0.0", digest: acmeV100, agent: claude, agentVersion: "2.1.292", model: haiku, required: true,
			trigger: v1alpha1.TriggerPush, attempts: 3, created: ts(10, 28, 9, 12, 0), completed: ts(10, 28, 9, 41, 12), phase: v1alpha1.PhaseSucceeded,
			verdict: v1alpha1.VerdictHelps, with: "0.95", without: "0.37", lift: "+0.58", n: "3/3", ci: "[+0.51, +0.65]", withErr: "0.03", withoutErr: "0.01",
			samples: [2][]string{{"0.95", "0.90", "1.00"}, {"0.36", "0.38", "0.37"}}, tiers: passedTiers, publication: "passed",
			reportDigest: reportFallback,
		},
		{
			skill: pydoc, version: "v1.1.0", digest: pyV110, agent: claude, agentVersion: "2.1.292", model: haiku, required: true,
			trigger: v1alpha1.TriggerModelChange, created: ts(10, 29, 11, 0, 0), completed: ts(10, 29, 11, 3, 30), phase: v1alpha1.PhaseSucceeded,
			verdict: v1alpha1.VerdictRejected, tiers: &v1alpha1.TierStatuses{Tier1: "failed", Tier2: "passed", Tier3: "skipped"},
			publication: "failed", findings: []string{pyFinding, "tier1/license: no license declared"}, reportDigest: reportRejected, network: full,
			netStatus:  &v1alpha1.NetworkStatus{Mode: "full"},
			conditions: []metav1.Condition{cond("Rejected", "Tier1Failed", pyFinding, ts(10, 29, 11, 3, 30))},
		},
		{
			skill: k8s, version: "v1.0.0", digest: k8sV100, agent: claude, agentVersion: "2.1.292", model: haiku, required: true,
			trigger: v1alpha1.TriggerPush, attempts: 3, created: ts(10, 31, 8, 52, 10), phase: v1alpha1.PhaseRunning,
			pushedBy: "release-bot", network: &v1alpha1.NetworkSpec{Mode: "allowlist", Source: "skill", Allow: []string{"kubernetes.io"}},
		},
		{
			skill: k8s, version: "v1.0.0", digest: k8sV100, agent: codex, agentVersion: "0.161.0", model: gpt,
			trigger: v1alpha1.TriggerPush, attempts: 3, created: ts(10, 31, 8, 52, 10), phase: v1alpha1.PhasePending,
			message: "waiting for a free slot (1 of 1 jobs running)", pushedBy: "release-bot",
			network: &v1alpha1.NetworkSpec{Mode: "allowlist", Source: "skill", Allow: []string{"kubernetes.io"}},
		},
	}
}

// worldHarbor holds the channel tags of the test world.
func worldHarbor() *harbor.Fake {
	return harbor.NewFake(map[string][]harbor.Artifact{
		acme: {
			{Digest: acmeV100, Tags: []string{"v1.0.0", "stable"}},
			{Digest: acmeV110, Tags: []string{"v1.1.0", "beta"}},
			{Digest: acmeV120, Tags: []string{"v1.2.0"}},
		},
		k8s: {{Digest: k8sV100, Tags: []string{"v1.0.0"}}},
		pydoc: {
			{Digest: sum(pydoc + "@v1.0.0"), Tags: []string{"v1.0.0", "beta", "stable"}},
			{Digest: pyV110, Tags: []string{"v1.1.0"}},
		},
	})
}

// worldStore holds the reports and bundles of the test world.
func worldStore(t *testing.T) *fakeStore {
	t.Helper()
	f := newFakeStore()
	trials := filepath.Join(testdata, "trials")
	f.report(reportClaude, readFile(t, filepath.Join(trials, "claude-code-doks", "run-dir", "result.json")),
		map[string]string{session.AnnotationTrials: bundleClaude})
	f.bundle(t, bundleClaude, filepath.Join(trials, "claude-code-doks", "bundle"))
	f.report(reportCodex, minimalResult(codex), map[string]string{session.AnnotationTrials: bundleCodex})
	f.bundle(t, bundleCodex, filepath.Join(trials, "codex-synthetic", "bundle"))
	f.report(reportOpencode, minimalResult(opencd), map[string]string{session.AnnotationTrials: bundleOpencode})
	f.bundle(t, bundleOpencode, filepath.Join(trials, "opencode-synthetic", "bundle"))
	f.report(reportUnscored, readFile(t, filepath.Join(trials, "claude-code-unscored", "run-dir", "result.json")),
		map[string]string{session.AnnotationTrials: bundleUnscored})
	f.bundle(t, bundleUnscored, filepath.Join(trials, "claude-code-unscored", "bundle"))
	f.report(reportFallback, fallbackResult(), nil)
	f.report(reportRejected, minimalResult(claude), map[string]string{session.AnnotationTrialsWithheld: "no-trials"})
	return f
}

const modelsYAML = `models:
  - agent: claude-code
    agentVersion: "2.1.292"
    model: claude-haiku-5-5
    required: true
    sandboxProfile: default
  - agent: codex
    agentVersion: "0.161.0"
    model: openai/gpt-6.1-sol
  - agent: opencode
    agentVersion: "1.19.1"
    model: ollama-cloud/gpt-oss:120b
`

// clusterObjects are the objects besides the SkillEvals: the model list, the
// SkillCIConfig with its Hubble Relay, and llm-keys.
func clusterObjects() []client.Object {
	return []client.Object{
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "skill-ci-models", Namespace: "skill-ci"}, Data: map[string]string{"models.yaml": modelsYAML}},
		&v1alpha1.SkillCIConfig{
			ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.SkillCIConfigName, Namespace: "skill-ci", Generation: 1},
			Spec:       v1alpha1.SkillCIConfigSpec{Observability: v1alpha1.Observability{HubbleRelay: "hubble-relay.kube-system.svc.cluster.local:80"}},
			Status:     v1alpha1.SkillCIConfigStatus{Effective: &v1alpha1.EffectiveConfig{Network: v1alpha1.EffectiveNetwork{EnforcedBy: "cilium"}}},
		},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "llm-keys", Namespace: "skill-ci"}, Data: map[string][]byte{
			"anthropic": []byte(llmAnthropic), "openai": []byte(llmOpenAI), "openai-base-url": []byte("https://api.openai.com/v1"),
		}},
	}
}

// harness is a Server over a fake cluster and registry.
type harness struct {
	srv    *Server
	harbor *harbor.Fake
	store  *fakeStore
	client client.Client
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

// newWorld is the harness of the UI fixtures.
func newWorld(t *testing.T) *harness {
	t.Helper()
	var objs []client.Object
	for _, s := range worldSpecs() {
		objs = append(objs, s.object())
	}
	return newHarness(t, append(objs, clusterObjects()...), worldHarbor(), worldStore(t))
}

func newHarness(t *testing.T, objs []client.Object, hb *harbor.Fake, store *fakeStore) *harness {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(objs...).WithStatusSubresource(&v1alpha1.SkillEval{}).Build()
	h := &harness{harbor: hb, store: store, client: c}
	srv, err := New(Config{
		Reader: c, Secrets: c, HarborUsername: harborUser, HarborPassword: harborPassword,
		Namespace: "skill-ci", Project: "skills", Harbor: hb, Reports: store, Bundles: store,
		Version: "0.11.0", ModelsConfigMap: "skill-ci-models", Thresholds: policy.DefaultThresholds(),
		Platform: &platform.Source{Reader: c, Namespace: "skill-ci"},
		Now:      func() time.Time { return testNow }, Log: logr.Discard(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv = srv
	return h
}

// get serves one GET request and returns the recorded response.
func (h *harness) get(target string) *httptest.ResponseRecorder {
	return h.do(http.MethodGet, target, nil)
}

func (h *harness) do(method, target string, header http.Header) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	h.srv.ServeHTTP(rec, req)
	return rec
}

// decode reads a JSON response into v.
func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
}
