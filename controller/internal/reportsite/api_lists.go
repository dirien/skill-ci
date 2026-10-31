package reportsite

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/platform"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
)

const (
	// recentRuns is the number of runs on the overview.
	recentRuns = 20
	// maxAttention caps each kind of the needs attention list.
	maxAttention = 20
	// defaultPageSize and maxPageSize bound GET /api/v1/runs pages.
	defaultPageSize = 50
	maxPageSize     = 200
	maxPage         = 1 << 20
)

// Values the enumerated query parameters of GET /api/v1/runs take.
var (
	sorts    = []string{"newest", "oldest", "skill", "lift"}
	verdicts = []string{
		string(v1alpha1.VerdictHelps), string(v1alpha1.VerdictNeutral), string(v1alpha1.VerdictHurts),
		string(v1alpha1.VerdictObsolete), string(v1alpha1.VerdictRegressed), string(v1alpha1.VerdictInconclusive),
		string(v1alpha1.VerdictRejected),
	}
	phases = []string{
		string(v1alpha1.PhasePending), string(v1alpha1.PhaseRunning), string(v1alpha1.PhaseSucceeded),
		string(v1alpha1.PhaseFailed), string(v1alpha1.PhasePlanned),
	}
	publications = []string{v1alpha1.TierPassed, v1alpha1.TierFailed, v1alpha1.TierIncomplete}
	triggers     = []string{string(v1alpha1.TriggerPush), string(v1alpha1.TriggerModelChange), string(v1alpha1.TriggerManual)}
)

// apiThresholds converts the controller thresholds.
func apiThresholds(t policy.Thresholds) apiv1.Thresholds {
	return apiv1.Thresholds{
		Helps: t.Helps, Hurts: t.Hurts, Promote: t.Promote, Demote: t.Demote, Obsolete: t.Obsolete,
		Regression: t.Regression, Confidence: t.Confidence,
	}
}

func (s *Server) apiSite(w http.ResponseWriter, r *http.Request) {
	if e := checkQuery(r.URL.Query()); e != nil {
		writeErr(w, e)
		return
	}
	s.writeView(w, r, &apiv1.Site{
		Namespace: s.cfg.Namespace, Project: s.cfg.Project, Version: s.cfg.Version,
		Thresholds: apiThresholds(s.cfg.Thresholds), GeneratedAt: s.cfg.Now().UTC(),
	})
}

// runSummary turns a run into a table row; trials is its bundle reference.
func (s *Server) runSummary(r Run, trials bundleRef) apiv1.RunSummary {
	se := r.se
	out := apiv1.RunSummary{
		Name: r.Name, Skill: r.Skill, Version: r.Version, Digest: r.Digest,
		Agent: r.Agent, AgentVersion: r.AgentVersion, RanVersion: se.Status.AgentVersion, Model: r.Model, Entry: r.Entry(),
		Required: r.Required, Attempts: se.Spec.Attempts, Trigger: r.Trigger, PushedBy: r.PushedBy,
		Phase: r.Phase, Verdict: r.Verdict,
		WithSkill: r.With.ptr(), WithoutSkill: r.Without.ptr(), Lift: r.Lift.ptr(), N: r.N,
		Publication: se.Status.Publication, Retries: se.Status.Retries,
		CreatedAt: r.Created, CompletedAt: timePtr(r.Completed), Message: se.Status.Message,
		HasReport: r.ReportDigest != "", Trials: trials.state, TrialsReason: trials.reason,
	}
	if t := se.Status.Tiers; t != nil {
		out.Tiers = apiv1.Tiers{Tier1: t.Tier1, Tier2: t.Tier2, Tier3: t.Tier3}
	}
	if r.CI != "" {
		out.LiftCI = &apiv1.Interval{Lower: r.CILower, Upper: r.CIUpper, Level: r.Confidence}
	}
	if !r.Completed.IsZero() && !r.Completed.Before(r.Created) {
		d := r.Completed.Sub(r.Created).Seconds()
		out.DurationSec = &d
	}
	out.NetworkMode = networkMode(se)
	return out
}

// networkMode is the effective network mode of a run; empty before network modes.
func networkMode(se *v1alpha1.SkillEval) string {
	if n := se.Status.Network; n != nil && n.Mode != "" {
		return n.Mode
	}
	if n := se.Spec.Network; n != nil {
		return n.Mode
	}
	return ""
}

// summaries turns runs into table rows, reading the trials state of each.
func (s *Server) summaries(ctx context.Context, runs []Run) []apiv1.RunSummary {
	states := s.trialStates(ctx, runs)
	out := make([]apiv1.RunSummary, 0, len(runs))
	for _, r := range runs {
		out = append(out, s.runSummary(r, states[r.Name]))
	}
	return out
}

func refresh(runs []Run) int {
	for _, r := range runs {
		if r.Active() {
			return refreshSeconds
		}
	}
	return 0
}

func (s *Server) apiOverview(w http.ResponseWriter, r *http.Request) {
	if e := checkQuery(r.URL.Query()); e != nil {
		writeErr(w, e)
		return
	}
	runs, e := s.listRuns(r.Context())
	if e != nil {
		writeErr(w, e)
		return
	}
	ch := s.channels.get(r.Context())
	v := apiv1.Overview{
		GeneratedAt: s.cfg.Now().UTC(), RefreshSeconds: refresh(runs),
		Attention: attention(runs), Channels: []apiv1.ChannelRow{},
	}
	if ch.Err != nil {
		v.ChannelsError = ch.Err.Error()
	}
	for _, run := range runs {
		v.Counts.Runs++
		switch v1alpha1.Phase(run.Phase) {
		case v1alpha1.PhasePending:
			v.Counts.Pending++
		case v1alpha1.PhaseRunning:
			v.Counts.Running++
		case v1alpha1.PhaseSucceeded:
			v.Counts.Succeeded++
		case v1alpha1.PhaseFailed:
			v.Counts.Failed++
		case v1alpha1.PhasePlanned:
			v.Counts.Planned++
		}
	}
	held, rejected := versionStates(runs)
	v.Counts.HeldBack, v.Counts.Rejected = len(held), len(rejected)
	skills := skillNames(runs, ch)
	v.Counts.Skills, v.Counts.OnStable = len(skills), ch.OnStable()
	for _, skill := range skills {
		row := apiv1.ChannelRow{Skill: skill, HeldBack: versionsOf(held, skill), Rejected: versionsOf(rejected, skill)}
		_, row.Stable = ch.Holder(skill, policy.TagStable)
		_, row.Beta = ch.Holder(skill, policy.TagBeta)
		row.Versions = len(digestsOf(runs, skill))
		v.Channels = append(v.Channels, row)
	}
	recent := runs[:min(len(runs), recentRuns)]
	v.Recent = s.summaries(r.Context(), recent)
	v.Network = s.networkPolicy(r.Context())
	s.writeView(w, r, &v)
}

// versionKey is one skill version: the skill and its digest.
type versionKey struct{ skill, digest string }

// versionStates returns the versions the regression gate holds back
// (Regressed or Inconclusive on a required run) and the versions rejected by
// Tier 1 or Tier 2 on a required run, each with its version tag.
func versionStates(runs []Run) (held, rejected map[versionKey]string) {
	held, rejected = map[versionKey]string{}, map[versionKey]string{}
	for _, r := range runs {
		k := versionKey{r.Skill, r.Digest}
		if r.Required && (r.Regressed != "" || r.Inconclusive != "") {
			held[k] = r.Version
		}
		if r.Required && r.Rejected() {
			rejected[k] = r.Version
		}
	}
	return held, rejected
}

// versionsOf returns the versions of one skill in m, newest first.
func versionsOf(m map[versionKey]string, skill string) []string {
	out := []string{}
	for k, v := range m {
		if k.skill == skill {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, newerFirst)
	return slices.Compact(out)
}

func digestsOf(runs []Run, skill string) map[string]bool {
	out := map[string]bool{}
	for _, r := range runs {
		if r.Skill == skill {
			out[r.Digest] = true
		}
	}
	return out
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

// attention lists failed runs, held back and rejected versions, running runs
// and pending runs, in that order, newest first within each kind.
func attention(runs []Run) []apiv1.AttentionItem {
	var failed, held, rejected, running, pending []apiv1.AttentionItem
	seenHeld, seenRejected := map[versionKey]bool{}, map[versionKey]bool{}
	for _, r := range runs { // newest first
		k := versionKey{r.Skill, r.Digest}
		item := apiv1.AttentionItem{Skill: r.Skill, Version: r.Version, Run: r.Name, Entry: r.Entry(), Since: r.Created}
		if !r.Completed.IsZero() {
			item.Since = r.Completed
		}
		switch {
		case r.Phase == string(v1alpha1.PhaseFailed):
			item.Kind, item.Message = apiv1.AttentionFailed, sentence(cmp.Or(r.se.Status.Message, "The run failed."))
			failed = append(failed, item)
		case r.Phase == string(v1alpha1.PhaseRunning):
			item.Kind, item.Since = apiv1.AttentionRunning, r.Created
			item.Message = sentence(cmp.Or(r.se.Status.Message, "The Job is running."))
			running = append(running, item)
		case r.Phase == string(v1alpha1.PhasePending):
			item.Kind, item.Since = apiv1.AttentionPending, r.Created
			item.Message = sentence(cmp.Or(r.se.Status.Message, "Waiting for the Job to start."))
			pending = append(pending, item)
		}
		if r.Required && (r.Regressed != "" || r.Inconclusive != "") && !seenHeld[k] {
			seenHeld[k] = true
			item.Kind, item.Message = apiv1.AttentionHeldBack, cmp.Or(r.Regressed, r.Inconclusive)
			held = append(held, item)
		}
		if r.Required && r.Rejected() && !seenRejected[k] {
			seenRejected[k] = true
			item.Kind, item.Message = apiv1.AttentionRejected, r.RejectedText()
			rejected = append(rejected, item)
		}
	}
	out := []apiv1.AttentionItem{}
	for _, list := range [][]apiv1.AttentionItem{failed, held, rejected, running, pending} {
		slices.SortStableFunc(list, func(a, b apiv1.AttentionItem) int { return b.Since.Compare(a.Since) })
		out = append(out, list[:min(len(list), maxAttention)]...)
	}
	return out
}

// sentence starts msg with a capital letter and ends it with a period.
func sentence(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return msg
	}
	if r := []rune(msg); unicode.IsLower(r[0]) {
		r[0] = unicode.ToUpper(r[0])
		msg = string(r)
	}
	if !strings.HasSuffix(msg, ".") {
		return msg + "."
	}
	return msg
}

// networkPolicy is the platform network policy in force.
func (s *Server) networkPolicy(ctx context.Context) apiv1.NetworkPolicy {
	st, err := s.cfg.Platform.Lookup(ctx)
	if err != nil {
		s.cfg.Log.Error(err, "reading the platform settings")
	}
	p := st.Settings.Network
	v := apiv1.NetworkPolicy{
		Default: p.DefaultMode(), Modes: orEmpty(slices.Clone(p.AllowedModes())), Bundles: orEmpty(platform.BundleNames(p.Bundles)),
		Enforcement: string(p.EnforcementMode()), Source: st.Source, HubbleRelay: st.Settings.HubbleRelay,
	}
	switch st.Source {
	case v1alpha1.ConfigSourceSkillCIConfig:
		v.SourceText = "SkillCIConfig " + v1alpha1.SkillCIConfigName
	case v1alpha1.ConfigSourceLastValid:
		v.SourceText = "the last valid spec of SkillCIConfig " + v1alpha1.SkillCIConfigName
	default:
		v.Source, v.SourceText = v1alpha1.ConfigSourceBuiltIn, "built-in defaults"
	}
	switch {
	case err != nil:
		v.Problem = "The SkillCIConfig could not be read: " + err.Error()
	case st.Invalid != nil:
		v.Problem = "SkillCIConfig " + v1alpha1.SkillCIConfigName + " is invalid: " + st.Invalid.Error()
	}
	if st.Found && s.cfg.Reader != nil {
		var cfg v1alpha1.SkillCIConfig
		key := clientKey(s.cfg.Namespace, v1alpha1.SkillCIConfigName)
		if s.cfg.Reader.Get(ctx, key, &cfg) == nil && cfg.Status.Effective != nil {
			v.EnforcedBy = cfg.Status.Effective.Network.EnforcedBy
		}
	}
	return v
}

func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, page, size, e := runQuery(q)
	if e != nil {
		writeErr(w, e)
		return
	}
	runs, e := s.listRuns(r.Context())
	if e != nil {
		writeErr(w, e)
		return
	}
	var matched []Run
	for _, run := range runs {
		if matchRun(f, run) {
			matched = append(matched, run)
		}
	}
	sortBy(matched, f.Sort)
	pages := max(1, (len(matched)+size-1)/size)
	from := min(len(matched), (page-1)*size)
	to := min(len(matched), from+size)
	v := apiv1.RunList{
		Items:          s.summaries(r.Context(), matched[from:to]),
		Pagination:     apiv1.Pagination{Page: page, PageSize: size, Total: len(matched), Pages: pages},
		Filter:         f,
		Options:        filterOptions(runs, f),
		RefreshSeconds: refresh(matched),
	}
	s.writeView(w, r, &v)
}

// runQuery validates the query of GET /api/v1/runs.
func runQuery(q map[string][]string) (apiv1.RunFilter, int, int, *apiError) {
	if e := checkQuery(q, "skill", "agent", "model", "verdict", "phase", "publication", "trigger", "q", "sort", "page", "pageSize"); e != nil {
		return apiv1.RunFilter{}, 0, 0, e
	}
	var f apiv1.RunFilter
	var e *apiError
	for _, p := range []struct {
		key string
		dst *string
	}{{"skill", &f.Skill}, {"agent", &f.Agent}, {"model", &f.Model}, {"q", &f.Q}} {
		if *p.dst, e = textParam(q, p.key); e != nil {
			return f, 0, 0, e
		}
	}
	for _, p := range []struct {
		key    string
		dst    *string
		values []string
	}{
		{"verdict", &f.Verdict, verdicts}, {"phase", &f.Phase, phases}, {"publication", &f.Publication, publications},
		{"trigger", &f.Trigger, triggers}, {"sort", &f.Sort, sorts},
	} {
		if *p.dst, e = enumParam(q, p.key, p.values); e != nil {
			return f, 0, 0, e
		}
	}
	f.Sort = cmp.Or(f.Sort, "newest")
	page, e := intParam(q, "page", 1, maxPage, 1)
	if e != nil {
		return f, 0, 0, e
	}
	size, e := intParam(q, "pageSize", 1, maxPageSize, defaultPageSize)
	if e != nil {
		return f, 0, 0, e
	}
	return f, page, size, nil
}

func matchRun(f apiv1.RunFilter, r Run) bool {
	return (f.Skill == "" || r.Skill == f.Skill) &&
		(f.Agent == "" || r.Harness() == f.Agent) &&
		(f.Model == "" || r.Model == f.Model) &&
		(f.Verdict == "" || r.Verdict == f.Verdict) &&
		(f.Phase == "" || r.Phase == f.Phase) &&
		(f.Publication == "" || r.se.Status.Publication == f.Publication) &&
		(f.Trigger == "" || r.Trigger == f.Trigger) &&
		(f.Q == "" || strings.Contains(r.Name, strings.ToLower(f.Q)))
}

// sortBy orders runs (already newest first) by the sort parameter.
func sortBy(runs []Run, by string) {
	switch by {
	case "oldest":
		slices.Reverse(runs)
	case "skill":
		slices.SortStableFunc(runs, func(a, b Run) int {
			return cmp.Or(strings.Compare(a.Skill, b.Skill), newerFirst(a.Version, b.Version))
		})
	case "lift":
		slices.SortStableFunc(runs, func(a, b Run) int {
			switch {
			case a.Lift.OK && b.Lift.OK:
				return cmp.Compare(b.Lift.Value, a.Lift.Value)
			case a.Lift.OK:
				return -1
			case b.Lift.OK:
				return 1
			default:
				return 0
			}
		})
	}
}

// filterOptions are the distinct values of all runs; a filter value from a
// shared URL stays selectable even when no run has it.
func filterOptions(runs []Run, f apiv1.RunFilter) apiv1.FilterOptions {
	var o apiv1.FilterOptions
	for _, r := range runs {
		o.Skills = append(o.Skills, r.Skill)
		o.Agents = append(o.Agents, r.Harness())
		o.Models = append(o.Models, r.Model)
		o.Phases = append(o.Phases, r.Phase)
		o.Triggers = append(o.Triggers, r.Trigger)
		if p := r.se.Status.Publication; p != "" {
			o.Publications = append(o.Publications, p)
		}
		if r.Verdict != "" {
			o.Verdicts = append(o.Verdicts, r.Verdict)
		}
	}
	return apiv1.FilterOptions{
		Skills: keepSorted(o.Skills, f.Skill), Agents: keepSorted(o.Agents, f.Agent), Models: keepSorted(o.Models, f.Model),
		Verdicts: keepSorted(o.Verdicts, f.Verdict), Phases: keepSorted(o.Phases, f.Phase),
		Publications: keepSorted(o.Publications, f.Publication), Triggers: keepSorted(o.Triggers, f.Trigger),
	}
}

func keepSorted(values []string, extra string) []string {
	if extra != "" {
		values = append(values, extra)
	}
	values = slices.DeleteFunc(values, func(v string) bool { return v == "" })
	slices.Sort(values)
	return orEmpty(slices.Compact(values))
}
