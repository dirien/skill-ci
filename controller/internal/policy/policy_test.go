package policy

import (
	"reflect"
	"testing"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
)

func TestVerdict(t *testing.T) {
	tests := []struct {
		name          string
		lift, without float64
		want          v1alpha1.Verdict
	}{
		{"clear help", 0.59, 0.36, v1alpha1.VerdictHelps},
		{"exactly helps band", 0.05, 0.30, v1alpha1.VerdictHelps},
		{"just below helps", 0.04, 0.30, v1alpha1.VerdictNeutral},
		{"float noise at helps band", 0.95 - 0.90, 0.30, v1alpha1.VerdictHelps},
		{"exactly hurts band", -0.10, 0.30, v1alpha1.VerdictHurts},
		{"just above hurts", -0.09, 0.30, v1alpha1.VerdictNeutral},
		{"neutral baseline below obsolete", 0.0, 0.79, v1alpha1.VerdictNeutral},
		{"neutral baseline obsolete", 0.0, 0.80, v1alpha1.VerdictObsolete},
		{"helps beats obsolete", 0.06, 0.90, v1alpha1.VerdictHelps},
		{"hurts beats obsolete", -0.20, 0.90, v1alpha1.VerdictHurts},
	}
	th := DefaultThresholds()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := th.Verdict(tt.lift, tt.without); got != tt.want {
				t.Errorf("Verdict(%v, %v) = %s, want %s", tt.lift, tt.without, got, tt.want)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	ok := func(lift float64, v v1alpha1.Verdict) Result {
		return Result{Required: true, Succeeded: true, Lift: lift, Verdict: v}
	}
	tests := []struct {
		name    string
		results []Result
		want    Outcome
	}{
		{"no results", nil, Outcome{}},
		{"only optional models", []Result{{Succeeded: true, Lift: 0.9}}, Outcome{}},
		{"required still running", []Result{ok(0.6, "Helps"), {Required: true}}, Outcome{}},
		{"promote on all required", []Result{ok(0.59, "Helps"), ok(0.10, "Helps")},
			Outcome{Complete: true, Promote: true}},
		{"one below promote", []Result{ok(0.59, "Helps"), ok(0.09, "Helps")}, Outcome{Complete: true}},
		{"demote on any required", []Result{ok(0.59, "Helps"), ok(-0.10, "Hurts")},
			Outcome{Complete: true, Demote: true}},
		{"optional hurt is ignored", []Result{ok(0.30, "Helps"), {Succeeded: true, Lift: -0.5}},
			Outcome{Complete: true, Promote: true}},
		{"obsolete on all", []Result{ok(0.0, "Obsolete"), ok(0.01, "Obsolete")},
			Outcome{Complete: true, Obsolete: true}},
		{"obsolete needs all", []Result{ok(0.0, "Obsolete"), ok(0.0, "Neutral")}, Outcome{Complete: true}},
	}
	th := DefaultThresholds()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := th.Evaluate(tt.results); got != tt.want {
				t.Errorf("Evaluate() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPlan(t *testing.T) {
	arts := func(old, cur []string) []harbor.Artifact {
		return []harbor.Artifact{{Digest: "sha256:old", Tags: old}, {Digest: "sha256:cur", Tags: cur}}
	}
	tests := []struct {
		name   string
		out    Outcome
		newest bool
		arts   []harbor.Artifact
		want   []Op
	}{
		{"newest gets beta, old loses it", Outcome{Complete: true}, true,
			arts([]string{"v1.0.0", "beta"}, []string{"v1.1.0"}),
			[]Op{{Digest: "sha256:old", Tag: "beta"}, {Add: true, Digest: "sha256:cur", Tag: "beta"}}},
		{"beta already in place", Outcome{Complete: true}, true,
			arts([]string{"v1.0.0"}, []string{"v1.1.0", "beta"}), nil},
		{"not newest leaves beta alone", Outcome{Complete: true}, false,
			arts([]string{"v1.0.0", "beta"}, []string{"v1.1.0"}), nil},
		{"promote moves stable", Outcome{Complete: true, Promote: true}, false,
			arts([]string{"v1.0.0", "stable"}, []string{"v1.1.0"}),
			[]Op{{Digest: "sha256:old", Tag: "stable"}, {Add: true, Digest: "sha256:cur", Tag: "stable"}}},
		{"promote and beta together", Outcome{Complete: true, Promote: true}, true,
			arts([]string{"v1.0.0", "stable", "beta"}, []string{"v1.1.0"}),
			[]Op{
				{Digest: "sha256:old", Tag: "beta"}, {Add: true, Digest: "sha256:cur", Tag: "beta"},
				{Digest: "sha256:old", Tag: "stable"}, {Add: true, Digest: "sha256:cur", Tag: "stable"},
			}},
		{"demote removes stable from digest", Outcome{Complete: true, Demote: true}, false,
			arts([]string{"v1.0.0"}, []string{"v1.1.0", "stable"}),
			[]Op{{Digest: "sha256:cur", Tag: "stable"}}},
		{"demote without stable is a no-op", Outcome{Complete: true, Demote: true}, false,
			arts([]string{"v1.0.0", "stable"}, []string{"v1.1.0"}), nil},
		{"obsolete never touches tags", Outcome{Complete: true, Obsolete: true}, false,
			arts([]string{"v1.0.0"}, []string{"v1.1.0", "stable"}), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Plan(tt.out, "sha256:cur", tt.newest, tt.arts); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Plan() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
