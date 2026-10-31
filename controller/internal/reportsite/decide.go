package reportsite

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/policy"
)

// Decision explains in plain words what the tag policy did with one skill version.
type Decision struct {
	// Kind picks the marker: pass, neutral, regressed or failed.
	Kind string
	// Headline is the short outcome, for example "Held back".
	Headline string
	// Detail is the reason in one sentence.
	Detail string
}

// stableInfo says who holds the stable tag of the skill.
type stableInfo struct {
	// Known is false when the registry could not be read.
	Known bool
	// This reports whether the decided version holds stable.
	This bool
	// Holder is the version that holds stable, empty when none does.
	Holder string
}

// decide ports the wording of tools/report_page.py: the policy outcome for
// the evals of one skill version, read against the controller thresholds.
func decide(runs []Run, st stableInfo, th policy.Thresholds) Decision {
	var required []Run
	for _, r := range runs {
		if r.Required {
			required = append(required, r)
		}
	}
	if len(required) == 0 {
		return Decision{Kind: "neutral", Headline: "No decision", Detail: "No required model has been evaluated for this version."}
	}
	if open := filterRuns(required, func(r Run) bool { return !r.Finished() }); len(open) > 0 {
		done := len(required) - len(open)
		if allPlanned(open) {
			return Decision{Kind: "neutral", Headline: "Planned", Detail: "Dry run: the evals are recorded and no Job was created."}
		}
		return Decision{Kind: "neutral", Headline: "Waiting", Detail: fmt.Sprintf("%d of %d required models have finished.", done, len(required))}
	}
	if failed := filterRuns(required, func(r Run) bool { return r.Phase == string(v1alpha1.PhaseFailed) }); len(failed) > 0 {
		return Decision{Kind: "failed", Headline: "Eval failed", Detail: fmt.Sprintf("An eval failed on %s, so the policy did not decide.", entries(failed))}
	}
	for _, r := range required {
		if r.Rejected() {
			return Decision{Kind: "failed", Headline: "Rejected", Detail: r.RejectedText()}
		}
	}
	for _, r := range required {
		if r.Regressed != "" {
			return Decision{Kind: "regressed", Headline: "Held back", Detail: r.Regressed}
		}
	}
	for _, r := range required {
		if r.Inconclusive != "" {
			return Decision{Kind: "neutral", Headline: "Not proven", Detail: r.Inconclusive + ". The stable version stays; more trials would show whether the drop is real."}
		}
	}
	lowest, below := liftSummary(required, th.Promote)
	var unproven []Run
	for _, r := range required {
		if r.CI != "" && r.Lift.OK && math.Round(r.Lift.Value*100)/100 >= th.Promote-1e-9 && !r.BeyondNoise() {
			unproven = append(unproven, r)
		}
	}
	switch {
	case st.This && lowest.Model == "":
		return Decision{Kind: "pass", Headline: "On stable", Detail: "This version holds the stable tag."}
	case st.This:
		return Decision{Kind: "pass", Headline: "On stable", Detail: fmt.Sprintf("Lift is %+.2f or more on every required model (%s lowest).", lowest.Lift.Value, lowest.Entry())}
	case len(below) > 0:
		parts := make([]string, len(below))
		for i, r := range below {
			parts[i] = fmt.Sprintf("%s %+.2f", r.Entry(), r.Lift.Value)
		}
		return Decision{Kind: "neutral", Headline: "Not promoted", Detail: fmt.Sprintf("Stable needs %+.2f on every required model; below it: %s.", th.Promote, strings.Join(parts, ", "))}
	case len(unproven) > 0:
		parts := make([]string, len(unproven))
		for i, r := range unproven {
			parts[i] = fmt.Sprintf("%s %+.2f (%s)", r.Entry(), r.Lift.Value, r.Evidence())
		}
		return Decision{Kind: "neutral", Headline: "Not promoted", Detail: "The lift reaches " + fmt.Sprintf("%+.2f", th.Promote) + " but is not beyond noise, so the skill may not help: " + strings.Join(parts, ", ") + ". Stable needs the whole interval above zero."}
	case !st.Known:
		return Decision{Kind: "neutral", Headline: "Meets threshold", Detail: fmt.Sprintf("Lift is %+.2f or more on every required model. Release channels are unavailable, so the tag is unknown.", th.Promote)}
	case st.Holder != "":
		return Decision{Kind: "neutral", Headline: "Meets threshold", Detail: fmt.Sprintf("Lift is %+.2f or more on every required model; %s holds stable.", th.Promote, st.Holder)}
	default:
		return Decision{Kind: "neutral", Headline: "Meets threshold", Detail: fmt.Sprintf("Lift is %+.2f or more on every required model; stable is not assigned yet.", th.Promote)}
	}
}

// liftSummary returns the required run with the lowest lift and the runs
// whose lift misses the promote threshold, lowest first. Lifts are compared at
// two decimals like the policy does.
func liftSummary(required []Run, promote float64) (lowest Run, below []Run) {
	scored := filterRuns(required, func(r Run) bool { return r.Lift.OK })
	slices.SortFunc(scored, func(a, b Run) int {
		if c := compareFloat(a.Lift.Value, b.Lift.Value); c != 0 {
			return c
		}
		return strings.Compare(a.Entry(), b.Entry())
	})
	if len(scored) == 0 {
		return Run{}, nil
	}
	for _, r := range scored {
		if math.Round(r.Lift.Value*100)/100 < promote-1e-9 {
			below = append(below, r)
		}
	}
	return scored[0], below
}

func compareFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func filterRuns(runs []Run, keep func(Run) bool) []Run {
	var out []Run
	for _, r := range runs {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

func allPlanned(runs []Run) bool {
	return len(filterRuns(runs, func(r Run) bool { return r.Phase == string(v1alpha1.PhasePlanned) })) == len(runs)
}

func entries(runs []Run) string {
	names := make([]string, len(runs))
	for i, r := range runs {
		names[i] = r.Entry()
	}
	return strings.Join(names, ", ")
}
