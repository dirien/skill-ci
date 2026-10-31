package controller

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/client-go/tools/events"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/metrics"
	"github.com/dirien/skill-ci/controller/internal/models"
	"github.com/dirien/skill-ci/controller/internal/policy"
)

const (
	tier1Failed = `{"tiers":{"tier1":"failed","tier2":"passed","tier3":"skipped"},"publication":"failed",` +
		`"findings":["tier1/security: hardcoded credential in scripts/deploy.sh","tier1/license: no license"]}`
	tier2Failed = `{"tiers":{"tier1":"passed","tier2":"failed","tier3":"skipped"},"publication":"failed",` +
		`"findings":["tier2/dedup: 91% similar to acme-notes"]}`
	allPassed = `{"withSkill":0.95,"withoutSkill":0.36,"lift":0.59,` +
		`"tiers":{"tier1":"passed","tier2":"passed","tier3":"passed"},"publication":"passed","findings":[]}`
)

func TestRunningTierResult(t *testing.T) {
	tests := []struct {
		name         string
		message      string
		wantPhase    v1alpha1.Phase
		wantVerdict  v1alpha1.Verdict
		wantWith     string
		wantTiers    *v1alpha1.TierStatuses
		wantPub      string
		wantFindings int
		wantMsg      string
	}{
		{"all tiers passed", allPassed, v1alpha1.PhaseSucceeded, v1alpha1.VerdictHelps, "0.95",
			&v1alpha1.TierStatuses{Tier1: "passed", Tier2: "passed", Tier3: "passed"}, "passed", 0, ""},
		{"tier 1 failed without scores is a finished run", tier1Failed, v1alpha1.PhaseSucceeded, v1alpha1.VerdictRejected, "",
			&v1alpha1.TierStatuses{Tier1: "failed", Tier2: "passed", Tier3: "skipped"}, "failed", 2, ""},
		{"tier 2 failed without scores is a finished run", tier2Failed, v1alpha1.PhaseSucceeded, v1alpha1.VerdictRejected, "",
			&v1alpha1.TierStatuses{Tier1: "passed", Tier2: "failed", Tier3: "skipped"}, "failed", 1, ""},
		{"tier 1 failed with scores keeps them and is still rejected",
			`{"withSkill":0.9,"withoutSkill":0.4,"tiers":{"tier1":"failed"},"findings":["tier1/pii: email address"]}`,
			v1alpha1.PhaseSucceeded, v1alpha1.VerdictRejected, "0.90", &v1alpha1.TierStatuses{Tier1: "failed"}, "", 1, ""},
		{"older runner: no tiers, nothing recorded", `{"withSkill":0.95,"withoutSkill":0.36,"lift":0.59}`,
			v1alpha1.PhaseSucceeded, v1alpha1.VerdictHelps, "0.95", nil, "", 0, ""},
		{"an error after the gate keeps the tiers and findings",
			`{"error":"no eval dataset: add evals/evals.json","tiers":{"tier1":"passed","tier2":"passed","tier3":"skipped"},` +
				`"publication":"incomplete","findings":["tier1/quality: description is short"]}`,
			v1alpha1.PhaseFailed, "", "", &v1alpha1.TierStatuses{Tier1: "passed", Tier2: "passed", Tier3: "skipped"}, "", 1, "no eval dataset"},
		{"no scores and no failed tier is a failure",
			`{"tiers":{"tier1":"incomplete","tier2":"passed","tier3":"skipped"},"publication":"incomplete"}`,
			v1alpha1.PhaseFailed, "", "", nil, "", 0, "no scores and no failed tier"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Config{MaxConcurrentJobs: 4, Thresholds: policy.DefaultThresholds()}, 200,
				map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}})
			got := e.run(e.create("acme", "v1.0.0", digestA, haiku), tt.message).Status
			if got.Phase != tt.wantPhase || got.Verdict != tt.wantVerdict || got.WithSkill != tt.wantWith {
				t.Errorf("phase/verdict/with = %s/%s/%q, want %s/%s/%q", got.Phase, got.Verdict, got.WithSkill, tt.wantPhase, tt.wantVerdict, tt.wantWith)
			}
			if (got.Tiers == nil) != (tt.wantTiers == nil) || (got.Tiers != nil && *got.Tiers != *tt.wantTiers) {
				t.Errorf("tiers = %+v, want %+v", got.Tiers, tt.wantTiers)
			}
			if got.Phase == v1alpha1.PhaseSucceeded && got.Publication != tt.wantPub {
				t.Errorf("publication = %q, want %q", got.Publication, tt.wantPub)
			}
			if len(got.Findings) != tt.wantFindings {
				t.Errorf("findings = %q, want %d", got.Findings, tt.wantFindings)
			}
			if !strings.Contains(got.Message, tt.wantMsg) {
				t.Errorf("message = %q, want it to contain %q", got.Message, tt.wantMsg)
			}
		})
	}
}

func TestRejectedTagPolicy(t *testing.T) {
	type eval struct {
		version, digest string
		model           models.Model
		msg             string
	}
	optional := models.Model{Agent: "claude-code", Model: "claude-opus-5-5"}
	tests := []struct {
		name  string
		start map[string][]harbor.Artifact
		evals []eval
		runs  int                 // how many of the evals finish; 0 means all
		want  map[string][]string // digest -> sorted tags afterwards
	}{
		{
			name:  "rejected on every required model gets neither beta nor stable",
			start: map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}},
			evals: []eval{{"v1.0.0", digestA, haiku, tier1Failed}, {"v1.0.0", digestA, sonnet, tier1Failed}},
			want:  map[string][]string{digestA: {"v1.0.0"}},
		},
		{
			name:  "rejected on one required model blocks the other model's lift",
			start: map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0"}}}},
			evals: []eval{{"v1.0.0", digestA, haiku, allPassed}, {"v1.0.0", digestA, sonnet, tier2Failed}},
			want:  map[string][]string{digestA: {"v1.0.0"}},
		},
		{
			name:  "a rejected run counts as finished",
			start: map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0", "stable"}}}},
			evals: []eval{{"v1.0.0", digestA, haiku, tier1Failed}, {"v1.0.0", digestA, sonnet, tier1Failed}},
			want:  map[string][]string{digestA: {"v1.0.0"}},
		},
		{
			name:  "waits for the other required model before acting",
			start: map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0", "stable"}}}},
			evals: []eval{{"v1.0.0", digestA, haiku, tier1Failed}, {"v1.0.0", digestA, sonnet, tier1Failed}},
			runs:  1,
			want:  map[string][]string{digestA: {"stable", "v1.0.0"}},
		},
		{
			name: "rejected newest version leaves beta and stable on the older one",
			start: map[string][]harbor.Artifact{"acme": {
				{Digest: digestA, Tags: []string{"v1.0.0", "beta", "stable"}},
				{Digest: digestB, Tags: []string{"v1.1.0"}},
			}},
			evals: []eval{
				{"v1.0.0", digestA, haiku, allPassed}, {"v1.0.0", digestA, sonnet, allPassed},
				{"v1.1.0", digestB, haiku, tier1Failed}, {"v1.1.0", digestB, sonnet, tier1Failed},
			},
			want: map[string][]string{digestA: {"beta", "stable", "v1.0.0"}, digestB: {"v1.1.0"}},
		},
		{
			name: "a rejected optional model does not block promotion",
			start: map[string][]harbor.Artifact{"acme": {
				{Digest: digestA, Tags: []string{"v1.0.0"}},
			}},
			evals: []eval{{"v1.0.0", digestA, haiku, allPassed}, {"v1.0.0", digestA, sonnet, allPassed}, {"v1.0.0", digestA, optional, tier1Failed}},
			want:  map[string][]string{digestA: {"beta", "stable", "v1.0.0"}},
		},
		{
			name: "the regression gate still holds back a version that was not rejected",
			start: map[string][]harbor.Artifact{"acme": {
				{Digest: digestA, Tags: []string{"v1.0.0", "stable"}},
				{Digest: digestB, Tags: []string{"v1.1.0"}},
			}},
			evals: []eval{
				{"v1.0.0", digestA, haiku, allPassed}, {"v1.0.0", digestA, sonnet, allPassed},
				{"v1.1.0", digestB, haiku, scoreWith(0.5, 0.36)}, {"v1.1.0", digestB, sonnet, scoreWith(0.5, 0.36)},
			},
			want: map[string][]string{digestA: {"stable", "v1.0.0"}, digestB: {"beta", "v1.1.0"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200, tt.start)
			var created []*v1alpha1.SkillEval
			for _, ev := range tt.evals {
				created = append(created, e.create("acme", ev.version, ev.digest, ev.model))
			}
			for i, se := range created {
				if tt.runs > 0 && i >= tt.runs {
					break
				}
				e.run(se, tt.evals[i].msg)
			}
			for digest, want := range tt.want {
				if got := tags(t, e.harbor, "acme", digest); !slices.Equal(got, want) {
					t.Errorf("tags of %s = %v, want %v", digest[:10], got, want)
				}
			}
		})
	}
}

// TestRejectedRecordsConditionEventAndMetrics checks what a rejection leaves
// behind: the Rejected condition with the findings, a Warning event, the
// Rejected verdict, and the publication and rejected series.
func TestRejectedRecordsConditionEventAndMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics.Register(reg)
	e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
		map[string][]harbor.Artifact{"rejects": {{Digest: digestA, Tags: []string{"v1.0.0", "stable"}}}})
	a := e.create("rejects", "v1.0.0", digestA, haiku)
	b := e.create("rejects", "v1.0.0", digestA, sonnet)
	e.run(a, tier1Failed)
	e.run(b, tier1Failed)

	for _, se := range []*v1alpha1.SkillEval{a, b} {
		got := e.get(se)
		cond := findCondition(got.Status.Conditions, ConditionRejected)
		if cond == nil || cond.Reason != ReasonTier1Failed || !strings.Contains(cond.Message, "tier1/security: hardcoded credential") {
			t.Errorf("Rejected condition on %s = %+v", se.Name, cond)
		}
		if got.Status.Verdict != v1alpha1.VerdictRejected {
			t.Errorf("verdict on %s = %s, want Rejected", se.Name, got.Status.Verdict)
		}
		policyCond := findCondition(got.Status.Conditions, ConditionPolicy)
		if policyCond == nil || policyCond.Reason != "Rejected" || !strings.Contains(policyCond.Message, "-stable") {
			t.Errorf("PolicyApplied condition on %s = %+v, want Rejected with -stable", se.Name, policyCond)
		}
	}

	rec, ok := e.r.Recorder.(*events.FakeRecorder)
	if !ok {
		t.Fatalf("recorder is %T", e.r.Recorder)
	}
	close(rec.Events)
	var found bool
	for ev := range rec.Events {
		found = found || strings.Contains(ev, "Warning") && strings.Contains(ev, ReasonTier1Failed)
	}
	if !found {
		t.Error("no Warning Tier1Failed event")
	}

	// The series are package-level and shared with other tests: look at this skill only.
	got := map[string]float64{}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["skill"] == "rejects" {
				got[mf.GetName()+"/"+labels["model"]+"/"+labels["status"]+"/"+labels["version"]] = m.GetGauge().GetValue()
			}
		}
	}
	want := map[string]float64{
		"skillci_publication/claude-haiku-5-5/failed/v1.0.0":  1,
		"skillci_publication/claude-sonnet-5-5/failed/v1.0.0": 1,
		"skillci_rejected///v1.0.0":                           1,
		"skillci_stable///v1.0.0":                             0, // it lost stable
		"skillci_obsolete///v1.0.0":                           0,
		"skillci_regressed///v1.0.0":                          0,
		"skillci_inconclusive///v1.0.0":                       0,
	}
	if !maps.Equal(got, want) {
		t.Errorf("series of the rejected skill = %v, want %v", got, want)
	}
	for name := range got {
		if strings.HasPrefix(name, "skillci_lift") || strings.HasPrefix(name, "skillci_with") {
			t.Errorf("rejected run has score series %s", name)
		}
	}
}

// TestRejectedSettles checks a rejected digest converges: reconciling the
// finished runs again writes nothing.
func TestRejectedSettles(t *testing.T) {
	e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
		map[string][]harbor.Artifact{"acme": {{Digest: digestA, Tags: []string{"v1.0.0", "stable"}}}})
	a := e.create("acme", "v1.0.0", digestA, haiku)
	b := e.create("acme", "v1.0.0", digestA, sonnet)
	e.run(a, tier2Failed)
	e.run(b, tier2Failed)

	// The first re-run may rewrite the PolicyApplied message ("tag changes" -> "no tag changes").
	for _, se := range []*v1alpha1.SkillEval{a, b} {
		e.reconcile(se)
	}
	before := e.snapshot()
	for _, se := range []*v1alpha1.SkillEval{a, b} {
		e.reconcile(se)
	}
	for name, now := range e.snapshot() {
		if now.ResourceVersion != before[name].ResourceVersion {
			t.Errorf("%s was written again (resourceVersion %s -> %s)", name, before[name].ResourceVersion, now.ResourceVersion)
		}
	}
	if got := tags(t, e.harbor, "acme", digestA); !slices.Equal(got, []string{"v1.0.0"}) {
		t.Errorf("tags = %v, want only the version tag", got)
	}
}
