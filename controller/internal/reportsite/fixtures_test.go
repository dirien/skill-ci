package reportsite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/evals"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/reportstore"
)

const (
	haiku  = "claude-haiku-5-5"
	sonnet = "claude-sonnet-5-5"
	opus   = "claude-opus-5-5"
)

// testNow is the clock of all tests: 30 Oct 2026, 14:30 UTC.
var testNow = time.Date(2026, 10, 30, 14, 30, 0, 0, time.UTC)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

// spec describes one SkillEval of the fixture.
type spec struct {
	skill, version, model string
	required              bool
	trigger               v1alpha1.Trigger
	started               time.Time
	took                  time.Duration
	phase                 v1alpha1.Phase
	with, without, lift   string
	verdict               v1alpha1.Verdict
	report                bool
	message               string
	regressed             string
	tiers                 [3]string // tier1, tier2, tier3; empty for a runner without tiers
	publication           string
	findings              []string
	rejected              string // message of the Rejected condition
	retries               int
	retried               string // message of the Retried condition
}

func at(hour, minute int) time.Time {
	return time.Date(2026, 10, 30, hour, minute, 0, 0, time.UTC)
}

// digestOf is the skill digest of a version, stable across fixtures.
func digestOf(skill, version string) string { return sum(skill + "@" + version) }

func reportDigestOf(s spec) string { return sum("report/" + s.skill + s.version + s.model) }

func (s spec) object() *v1alpha1.SkillEval {
	se := &v1alpha1.SkillEval{
		Name: evals.Name(s.skill, s.version, s.model), Namespace: "skill-ci",
		CreationTimestamp: metav1.NewTime(s.started),
		Spec: v1alpha1.SkillEvalSpec{
			Skill: s.skill, Version: s.version, Digest: digestOf(s.skill, s.version), Agent: "claude-code",
			Model: s.model, Required: s.required, Attempts: 1, Trigger: s.trigger,
		},
		Status: v1alpha1.SkillEvalStatus{
			Phase: s.phase, JobName: "job-" + evals.Slug(s.version+s.model), WithSkill: s.with, WithoutSkill: s.without,
			Lift: s.lift, Verdict: s.verdict, Message: s.message,
		},
	}
	if s.took > 0 {
		done := metav1.NewTime(s.started.Add(s.took))
		se.Status.CompletedAt = &done
	}
	if s.report {
		se.Status.ReportDigest = reportDigestOf(s)
	}
	if s.tiers != [3]string{} {
		se.Status.Tiers = &v1alpha1.TierStatuses{Tier1: s.tiers[0], Tier2: s.tiers[1], Tier3: s.tiers[2]}
	}
	se.Status.Publication, se.Status.Findings = s.publication, s.findings
	if s.rejected != "" {
		se.Status.Conditions = append(se.Status.Conditions, metav1.Condition{
			Type: "Rejected", Status: metav1.ConditionTrue, Reason: "Tier1Failed", Message: s.rejected,
			LastTransitionTime: metav1.NewTime(s.started.Add(s.took)),
		})
	}
	se.Status.Retries = s.retries
	if s.retried != "" {
		se.Status.Conditions = append(se.Status.Conditions, metav1.Condition{
			Type: "Retried", Status: metav1.ConditionTrue, Reason: "InfrastructureFailure", Message: s.retried,
			LastTransitionTime: metav1.NewTime(s.started),
		})
	}
	if s.regressed != "" {
		se.Status.Conditions = []metav1.Condition{{
			Type: "Regressed", Status: metav1.ConditionTrue, Reason: "WorseThanStable", Message: s.regressed,
			LastTransitionTime: metav1.NewTime(s.started.Add(s.took)),
		}}
	}
	return se
}

func ok(skill, version, model string, required bool, trigger v1alpha1.Trigger, start time.Time, with, without, lift string, verdict v1alpha1.Verdict) spec {
	return spec{
		skill: skill, version: version, model: model, required: required, trigger: trigger, started: start,
		took: 6*time.Minute + 20*time.Second, phase: v1alpha1.PhaseSucceeded, with: with, without: without, lift: lift, verdict: verdict,
	}
}

const heldBack = "claude-code/claude-haiku-5-5 scores 0.69 with v1.2.0, stable v1.1.0 scored 0.95; " +
	"claude-code/claude-sonnet-5-5 scores 0.70 with v1.2.0, stable v1.1.0 scored 0.95 (tolerance 0.05)"

// demoSpecs mirror the cluster after the demo: DEMO.md lists the scores.
func demoSpecs() []spec {
	push, change := v1alpha1.TriggerPush, v1alpha1.TriggerModelChange
	a, k, p := "acme-release-notes", "k8s-resource-naming", "python-docstrings"
	specs := []spec{
		ok(a, "v1.0.0", haiku, true, push, at(13, 2), "0.95", "0.35", "+0.60", "Helps"),
		ok(a, "v1.0.0", sonnet, true, push, at(13, 2), "0.95", "0.32", "+0.63", "Helps"),
		ok(a, "v1.0.0", opus, false, change, at(13, 14), "0.95", "0.36", "+0.59", "Helps"),
		ok(a, "v1.1.0", haiku, true, push, at(13, 25), "0.95", "0.37", "+0.58", "Helps"),
		ok(a, "v1.1.0", sonnet, true, push, at(13, 25), "0.95", "0.31", "+0.64", "Helps"),
		ok(a, "v1.1.0", opus, false, push, at(13, 25), "0.95", "0.37", "+0.58", "Helps"),
		ok(a, "v1.2.0", haiku, true, push, at(13, 52), "0.69", "0.36", "+0.33", "Regressed"),
		ok(a, "v1.2.0", sonnet, true, push, at(13, 52), "0.70", "0.31", "+0.39", "Regressed"),
		ok(a, "v1.2.0", opus, false, push, at(13, 52), "0.71", "0.37", "+0.34", "Helps"),
		ok(k, "v1.0.0", haiku, true, push, at(13, 40), "0.93", "0.36", "+0.57", "Helps"),
		ok(k, "v1.0.0", sonnet, true, push, at(13, 40), "0.95", "0.33", "+0.62", "Helps"),
		ok(p, "v1.0.0", haiku, true, push, at(14, 5), "0.62", "0.59", "+0.03", "Neutral"),
		ok(p, "v1.0.0", sonnet, true, push, at(14, 5), "0.95", "0.52", "+0.43", "Helps"),
		ok(p, "v1.0.0", opus, false, change, at(14, 5), "0.96", "0.55", "+0.41", "Helps"),
	}
	for i := range specs {
		s := &specs[i]
		s.report = s.skill == a && s.version == "v1.1.0" // only these ran on runner 0.3.0
		if s.skill == a && s.version == "v1.2.0" && s.model != opus {
			s.regressed = heldBack
		}
	}
	// A failed run and one that is still going.
	specs = append(specs,
		spec{skill: k, version: "v1.0.0", model: opus, trigger: change, started: at(14, 12), took: 90 * time.Second,
			phase: v1alpha1.PhaseFailed, message: "runner exited with code 1: ANTHROPIC_API_KEY rejected (HTTP 401)"},
		spec{skill: a, version: "v1.3.0", model: haiku, required: true, trigger: push, started: at(14, 27), phase: v1alpha1.PhaseRunning},
		spec{skill: a, version: "v1.3.0", model: sonnet, required: true, trigger: push, started: at(14, 27), phase: v1alpha1.PhasePending},
	)
	return specs
}

// Skills of the tier fixtures.
const (
	sloppy = "sloppy-deploy"
	dup    = "dup-notes"
	gap    = "scanner-gap"
)

var (
	allPassed = [3]string{"passed", "passed", "passed"}
	finding1  = "tier1/security: hardcoded credential in scripts/deploy.sh"
	finding2  = "tier1/license: no license declared"
)

// tierSpecs are the demo runs after the runner reports tiers: the newer
// acme-release-notes runs carry tier results, the older ones are in the old
// format, and three more skills show a Tier 1 rejection, a Tier 2 rejection
// and an incomplete tier.
func tierSpecs() []spec {
	push := v1alpha1.TriggerPush
	specs := demoSpecs()
	for i := range specs {
		s := &specs[i]
		if s.skill == "acme-release-notes" && s.version == "v1.1.0" {
			s.tiers, s.publication = allPassed, "passed"
		}
		if s.skill == "acme-release-notes" && s.version == "v1.2.0" {
			s.tiers, s.publication = allPassed, "passed"
		}
	}
	rejected := func(skill string, model string, required bool, tiers [3]string, findings ...string) spec {
		s := spec{
			skill: skill, version: "v1.0.0", model: model, required: required, trigger: push, started: at(14, 20),
			took: 40 * time.Second, phase: v1alpha1.PhaseSucceeded, verdict: v1alpha1.VerdictRejected,
			tiers: tiers, publication: "failed", findings: findings,
			rejected: findings[0],
		}
		return s
	}
	specs = append(specs,
		rejected(sloppy, haiku, true, [3]string{"failed", "passed", "skipped"}, finding1, finding2),
		rejected(sloppy, sonnet, true, [3]string{"failed", "passed", "skipped"}, finding1, finding2),
		rejected(sloppy, opus, false, [3]string{"failed", "passed", "skipped"}, finding1, finding2),
		rejected(dup, haiku, true, [3]string{"passed", "failed", "skipped"}, "tier2/dedup: 91% similar to acme-release-notes"),
		rejected(dup, sonnet, true, [3]string{"passed", "failed", "skipped"}, "tier2/dedup: 91% similar to acme-release-notes"),
		ok(gap, "v1.0.0", haiku, true, push, at(14, 22), "0.95", "0.40", "+0.55", "Helps"),
		ok(gap, "v1.0.0", sonnet, true, push, at(14, 22), "0.95", "0.40", "+0.55", "Helps"),
	)
	for i := range specs {
		s := &specs[i]
		if s.skill == gap {
			s.tiers, s.publication = [3]string{"incomplete", "passed", "passed"}, "incomplete"
		}
		if s.skill == sloppy && s.model == haiku {
			s.report = true
		}
	}
	return specs
}

func tierHarbor() *harbor.Fake {
	arts := map[string][]harbor.Artifact{
		sloppy: {{Digest: digestOf(sloppy, "v1.0.0"), Tags: []string{"v1.0.0"}}},
		dup:    {{Digest: digestOf(dup, "v1.0.0"), Tags: []string{"v1.0.0"}}},
		gap:    {{Digest: digestOf(gap, "v1.0.0"), Tags: []string{"v1.0.0", "beta", "stable"}}},
	}
	demo := demoHarbor()
	for _, n := range []string{"acme-release-notes", "k8s-resource-naming", "python-docstrings"} {
		a, err := demo.ListArtifacts(context.Background(), n)
		if err != nil {
			panic(err) // the fake never fails without Err set
		}
		arts[n] = a
	}
	return harbor.NewFake(arts)
}

func demoHarbor() *harbor.Fake {
	return harbor.NewFake(map[string][]harbor.Artifact{
		"acme-release-notes": {
			{Digest: digestOf("acme-release-notes", "v1.0.0"), Tags: []string{"v1.0.0"}},
			{Digest: digestOf("acme-release-notes", "v1.1.0"), Tags: []string{"v1.1.0", "stable"}},
			{Digest: digestOf("acme-release-notes", "v1.2.0"), Tags: []string{"v1.2.0", "beta"}},
			{Digest: digestOf("acme-release-notes", "v1.3.0"), Tags: []string{"v1.3.0"}},
		},
		"k8s-resource-naming": {{Digest: digestOf("k8s-resource-naming", "v1.0.0"), Tags: []string{"v1.0.0", "beta", "stable"}}},
		"python-docstrings":   {{Digest: digestOf("python-docstrings", "v1.0.0"), Tags: []string{"v1.0.0", "beta"}}},
	})
}

// resultJSON is the shape of SkillEvaluator's result.json, reduced to the lift block.
func resultJSON(model string, with, without [6]float64) []byte {
	names := [6]string{"accuracy", "goal_accuracy", "behavior_check", "skill_execution", "skill_efficiency", "security"}
	lift := ""
	for i, n := range names {
		lift += fmt.Sprintf(`"%s":{"with_skill":%v,"without_skill":%v,"delta":%.4f,"direction":"up"},`, n, with[i], without[i], with[i]-without[i])
	}
	return []byte(fmt.Sprintf(`{"agents":{"claude-code":{"model":%q,"skill_activation":{"status":"activated","positive_trials":3,"activated_trials":3},`+
		`"lift":{%s"overall":{"with_skill":0.95,"without_skill":0.3665,"delta":0.5835}}}}}`, model, lift))
}

// fakeReports is a ReportStore over a map keyed by report digest and file name.
type fakeReports struct {
	files map[string][]byte
	err   error
}

func (f *fakeReports) File(_ context.Context, _, digest, name string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if data, ok := f.files[digest+"/"+name]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("fake store %s/%s: %w", digest, name, reportstore.ErrNotFound)
}

const reportBody = "<!DOCTYPE html><html><head><title>SkillEvaluator Validation Report</title></head><body>report</body></html>"

// demoReports stores report.html and result.json for every spec with a report.
func demoReports(specs []spec) *fakeReports {
	f := &fakeReports{files: map[string][]byte{}}
	for _, s := range specs {
		if s.report {
			f.files[reportDigestOf(s)+"/"+reportstore.FileReport] = []byte(reportBody)
			f.files[reportDigestOf(s)+"/"+reportstore.FileResult] = resultJSON(s.model,
				[6]float64{1, 1, 1, 1, 0.75, 1}, [6]float64{0.4, 0.0833, 0.2818, 0, 0.25, 1})
		}
	}
	return f
}

// harness is a Server over a fake client with the demo data.
type harness struct {
	srv     *Server
	harbor  *harbor.Fake
	reports *fakeReports
	client  client.Client
}

func newHarness(t *testing.T, specs []spec) *harness {
	t.Helper()
	return newHarnessWith(t, specs, demoHarbor())
}

// newHarnessWith is newHarness over a given registry.
func newHarnessWith(t *testing.T, specs []spec, hb *harbor.Fake) *harness {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objs := make([]client.Object, len(specs))
	for i, s := range specs {
		objs[i] = s.object()
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	h := &harness{harbor: hb, reports: demoReports(specs), client: c}
	srv, err := New(Config{
		Reader: c, Namespace: "skill-ci", Project: "skills", Harbor: h.harbor, Reports: h.reports,
		Thresholds: policy.DefaultThresholds(), Now: func() time.Time { return testNow }, Log: logr.Discard(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv = srv
	return h
}

// get serves one GET request and returns the recorded response.
func (h *harness) get(target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}
