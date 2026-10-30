package reportsite

import (
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/policy"
)

const refreshSeconds = 15

// Crumb is one step of the breadcrumb trail; the last one has no Href.
type Crumb struct {
	// Label is the visible text.
	Label string
	// Href is the link target, empty for the current page.
	Href string
}

// Page is what every template needs for the shared layout.
type Page struct {
	// Title is the document title.
	Title string
	// Crumbs is the breadcrumb trail.
	Crumbs []Crumb
	// Refresh is the reload interval in seconds, zero for none.
	Refresh int
	// Namespace and Project name the data sources shown in the header.
	Namespace, Project string
	// Generated is when the page was rendered.
	Generated time.Time
}

// Filter is the set of server-side filters of the runs table.
type Filter struct {
	// Skill, Model, Verdict, Phase and Publication each match exactly; empty matches all.
	Skill, Model, Verdict, Phase, Publication string
}

func filterFrom(q url.Values) Filter {
	return Filter{
		Skill: q.Get("skill"), Model: q.Get("model"), Verdict: q.Get("verdict"), Phase: q.Get("phase"),
		Publication: q.Get("publication"),
	}
}

// Active reports whether any filter is set.
func (f Filter) Active() bool { return f != Filter{} }

func (f Filter) match(r Run) bool {
	return (f.Skill == "" || r.Skill == f.Skill) &&
		(f.Model == "" || r.Model == f.Model) &&
		(f.Verdict == "" || r.Verdict == f.Verdict) &&
		(f.Phase == "" || r.Phase == f.Phase) &&
		(f.Publication == "" || r.Publication == f.Publication)
}

// FilterOptions are the values the filter drop-downs offer.
type FilterOptions struct {
	// Skills, Models, Verdicts, Phases and Publications hold the distinct values present in the runs.
	Skills, Models, Verdicts, Phases, Publications []string
}

// Stats are the counts in the header of the runs page.
type Stats struct {
	// Runs, Succeeded and Failed count SkillEvals by phase.
	Runs, Succeeded, Failed int
	// Skills counts skills known to the cluster or the registry.
	Skills int
	// OnStable counts skills with a stable version in the registry.
	OnStable int
	// HeldBack counts skill versions the regression gate holds back.
	HeldBack int
	// Rejected counts skill versions that failed Tier 1 or Tier 2 on a required model.
	Rejected int
}

// ChannelRow is one skill in the release channels table.
type ChannelRow struct {
	// Skill is the skill name.
	Skill string
	// Stable and Beta are the versions that hold the tags, empty when none does.
	Stable, Beta string
	// HeldBack lists the versions the regression gate holds back, newest first.
	HeldBack []string
	// Rejected lists the versions that failed Tier 1 or Tier 2 on a required model, newest first.
	Rejected []string
	// Versions counts the digests that have been evaluated.
	Versions int
}

type indexView struct {
	Page
	Stats       Stats
	Channels    []ChannelRow
	ChannelsOK  bool
	ChannelsErr string
	Filter      Filter
	Options     FilterOptions
	Runs        []Run
	Thresholds  policy.Thresholds
}

func buildIndex(page Page, runs []Run, ch *Channels, f Filter, th policy.Thresholds) indexView {
	skills := skillNames(runs, ch)
	v := indexView{Page: page, Filter: f, Thresholds: th, ChannelsOK: ch.Known(), Options: filterOptions(runs, f)}
	if ch != nil && ch.Err != nil {
		v.ChannelsErr = ch.Err.Error()
	}
	held := map[[2]string]string{}     // (skill, digest) -> version
	rejected := map[[2]string]string{} // (skill, digest) -> version
	for _, r := range runs {
		v.Stats.Runs++
		switch v1alpha1.Phase(r.Phase) {
		case v1alpha1.PhaseSucceeded:
			v.Stats.Succeeded++
		case v1alpha1.PhaseFailed:
			v.Stats.Failed++
		}
		if r.Regressed != "" {
			held[[2]string{r.Skill, r.Digest}] = r.Version
		}
		if r.Required && r.Rejected() {
			rejected[[2]string{r.Skill, r.Digest}] = r.Version
		}
		if r.Active() {
			v.Refresh = refreshSeconds
		}
		if f.match(r) {
			v.Runs = append(v.Runs, r)
		}
	}
	v.Stats.Skills = len(skills)
	v.Stats.HeldBack = len(held)
	v.Stats.Rejected = len(rejected)
	if ch.Known() {
		v.Stats.OnStable = ch.OnStable()
	}
	for _, skill := range skills {
		row := ChannelRow{Skill: skill}
		_, row.Stable = ch.Holder(skill, policy.TagStable)
		_, row.Beta = ch.Holder(skill, policy.TagBeta)
		digests := map[string]bool{}
		for _, r := range runs {
			if r.Skill == skill {
				digests[r.Digest] = true
			}
		}
		row.Versions = len(digests)
		for key, version := range held {
			if key[0] == skill {
				row.HeldBack = append(row.HeldBack, version)
			}
		}
		for key, version := range rejected {
			if key[0] == skill {
				row.Rejected = append(row.Rejected, version)
			}
		}
		slices.SortFunc(row.HeldBack, newerFirst)
		slices.SortFunc(row.Rejected, newerFirst)
		v.Channels = append(v.Channels, row)
	}
	return v
}

// skillNames returns the skills in the runs and the registry, sorted.
func skillNames(runs []Run, ch *Channels) []string {
	seen := map[string]bool{}
	for _, r := range runs {
		seen[r.Skill] = true
	}
	for _, n := range ch.Skills() {
		seen[n] = true
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func filterOptions(runs []Run, f Filter) FilterOptions {
	var o FilterOptions
	for _, r := range runs {
		o.Skills = append(o.Skills, r.Skill)
		o.Models = append(o.Models, r.Model)
		o.Phases = append(o.Phases, r.Phase)
		if r.Publication != "" {
			o.Publications = append(o.Publications, r.Publication)
		}
		if r.Verdict != "" {
			o.Verdicts = append(o.Verdicts, r.Verdict)
		}
	}
	// A filter value from a shared URL stays selectable even when no run has it.
	o.Skills, o.Models = keepSorted(o.Skills, f.Skill), keepSorted(o.Models, f.Model)
	o.Verdicts, o.Phases = keepSorted(o.Verdicts, f.Verdict), keepSorted(o.Phases, f.Phase)
	o.Publications = keepSorted(o.Publications, f.Publication)
	return o
}

func keepSorted(values []string, extra string) []string {
	if extra != "" {
		values = append(values, extra)
	}
	slices.Sort(values)
	return slices.Compact(values)
}

// ModelColumn is one model column of the skill page.
type ModelColumn struct {
	// Agent and Model identify the column.
	Agent, Model string
	// Required is true when any run of the model is required.
	Required bool
}

// VersionRow is one evaluated digest of a skill.
type VersionRow struct {
	// Version and Digest identify the row.
	Version, Digest string
	// Channels are the channel tags the digest carries in the registry.
	Channels []string
	// Cells holds the newest run per model column, nil where the model never ran.
	Cells []*Run
	// Decision is what the tag policy did with this version.
	Decision Decision
}

type skillView struct {
	Page
	Skill       string
	Stable      string
	Beta        string
	RunCount    int
	ChannelsErr string
	Models      []ModelColumn
	Rows        []VersionRow
	Thresholds  policy.Thresholds
}

func buildSkill(page Page, skill string, runs []Run, ch *Channels, th policy.Thresholds) skillView {
	v := skillView{Page: page, Skill: skill, RunCount: len(runs), Thresholds: th}
	if ch != nil && ch.Err != nil {
		v.ChannelsErr = ch.Err.Error()
	}
	_, v.Stable = ch.Holder(skill, policy.TagStable)
	_, v.Beta = ch.Holder(skill, policy.TagBeta)
	stableDigest, stableVersion := ch.Holder(skill, policy.TagStable)

	cols := map[[2]string]*ModelColumn{}
	byDigest := map[string][]Run{}
	for _, r := range runs { // runs are newest first
		key := [2]string{r.Agent, r.Model}
		if cols[key] == nil {
			cols[key] = &ModelColumn{Agent: r.Agent, Model: r.Model}
		}
		cols[key].Required = cols[key].Required || r.Required
		byDigest[r.Digest] = append(byDigest[r.Digest], r)
	}
	for _, c := range cols {
		v.Models = append(v.Models, *c)
	}
	slices.SortFunc(v.Models, func(a, b ModelColumn) int {
		if a.Required != b.Required {
			if a.Required {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Agent+"/"+a.Model, b.Agent+"/"+b.Model)
	})

	for digest, group := range byDigest {
		row := VersionRow{Version: group[0].Version, Digest: digest, Channels: ch.Tags(skill, digest), Cells: make([]*Run, len(v.Models))}
		for i, col := range v.Models {
			for j := range group {
				if group[j].Agent == col.Agent && group[j].Model == col.Model {
					row.Cells[i] = &group[j]
					break // newest first
				}
			}
		}
		latest := make([]Run, 0, len(v.Models)) // newest run per model drives the decision
		for _, c := range row.Cells {
			if c != nil {
				latest = append(latest, *c)
			}
		}
		row.Decision = decide(latest, stableInfo{Known: ch.Known(), This: digest == stableDigest && digest != "", Holder: stableVersion}, th)
		v.Rows = append(v.Rows, row)
	}
	slices.SortFunc(v.Rows, func(a, b VersionRow) int {
		if c := newerFirst(a.Version, b.Version); c != 0 {
			return c
		}
		return strings.Compare(a.Digest, b.Digest)
	})
	return v
}

// ReportView is the report section of the run page.
type ReportView struct {
	// State is none (no report stored), missing (stored but gone from the
	// registry), error (registry or file problem), files (Tier 3 did not run,
	// so only the stored files are offered) or ok.
	State string
	// Err is the error text for the error state.
	Err string
	// Result holds the dimension scores for the ok state.
	Result Result
}

type runView struct {
	Page
	Run      Run
	Channels []string
	Report   ReportView
}
