package reportsite

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/models"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
)

func clientKey(namespace, name string) client.ObjectKey {
	return client.ObjectKey{Namespace: namespace, Name: name}
}

// failingVerdicts are the verdicts that do not count a required entry as passing.
var failingVerdicts = []string{
	string(v1alpha1.VerdictHurts), string(v1alpha1.VerdictRegressed), string(v1alpha1.VerdictRejected),
	string(v1alpha1.VerdictInconclusive),
}

func (s *Server) apiSkills(w http.ResponseWriter, r *http.Request) {
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
	v := apiv1.SkillList{Items: []apiv1.SkillSummary{}}
	if ch.Err != nil {
		v.ChannelsError = ch.Err.Error()
	}
	held, rejected := versionStates(runs)
	for _, skill := range skillNames(runs, ch) {
		row := apiv1.SkillSummary{Skill: skill, HeldBack: versionsOf(held, skill), Rejected: versionsOf(rejected, skill)}
		_, row.Stable = ch.Holder(skill, policy.TagStable)
		_, row.Beta = ch.Holder(skill, policy.TagBeta)
		var mine []Run
		for _, run := range runs { // newest first
			if run.Skill == skill {
				mine = append(mine, run)
			}
		}
		row.Runs, row.Versions = len(mine), len(digestsOf(runs, skill))
		for _, run := range mine {
			switch v1alpha1.Phase(run.Phase) {
			case v1alpha1.PhasePending:
				row.Active++
				row.Pending++
			case v1alpha1.PhaseRunning:
				row.Active++
				row.Running++
			}
			if row.Newest == "" || newerFirst(run.Version, row.Newest) < 0 {
				row.Newest = run.Version
			}
		}
		if len(mine) > 0 {
			row.LastRunAt = timePtr(mine[0].Created)
		}
		row.Required = requiredRatio(mine, row.Newest)
		v.Items = append(v.Items, row)
	}
	s.writeView(w, r, &v)
}

// requiredRatio counts the required entries of the newest version whose
// newest run succeeded with a verdict that does not fail it.
func requiredRatio(runs []Run, version string) apiv1.Ratio {
	seen := map[string]bool{}
	var out apiv1.Ratio
	for _, r := range runs { // newest first
		if !r.Required || r.Version != version || seen[r.Entry()] {
			continue
		}
		seen[r.Entry()] = true
		out.Total++
		if r.Phase == string(v1alpha1.PhaseSucceeded) && !slices.Contains(failingVerdicts, r.Verdict) {
			out.Passed++
		}
	}
	return out
}

func (s *Server) apiSkill(w http.ResponseWriter, r *http.Request) {
	skill := r.PathValue("skill")
	if len(skill) > 255 || !skillName.MatchString(skill) {
		writeErr(w, badRequest("%q is not a valid skill name.", skill))
		return
	}
	if e := checkQuery(r.URL.Query()); e != nil {
		writeErr(w, e)
		return
	}
	all, e := s.listRuns(r.Context())
	if e != nil {
		writeErr(w, e)
		return
	}
	ch := s.channels.get(r.Context())
	var runs []Run
	for _, run := range all {
		if run.Skill == skill {
			runs = append(runs, run)
		}
	}
	if len(runs) == 0 && !slices.Contains(ch.Skills(), skill) {
		writeErr(w, notFound("No skill named %s has runs or a repository in Harbor.", skill))
		return
	}
	v := s.skillDetail(r.Context(), skill, runs, ch)
	s.writeView(w, r, &v)
}

// column is one model list entry of the skill matrix.
type column struct {
	agent, agentVersion, model string
	required                   bool
}

func (c column) entry() string {
	if c.agentVersion == "" {
		return c.agent + "/" + c.model
	}
	return c.agent + "@" + c.agentVersion + "/" + c.model
}

func (s *Server) skillDetail(ctx context.Context, skill string, runs []Run, ch *Channels) apiv1.SkillDetail {
	v := apiv1.SkillDetail{Skill: skill, Runs: len(runs), Columns: []apiv1.ModelColumn{}, Rows: []apiv1.VersionRow{}, RefreshSeconds: refresh(runs)}
	if ch.Err != nil {
		v.ChannelsError = ch.Err.Error()
	}
	_, v.Stable = ch.Holder(skill, policy.TagStable)
	_, v.Beta = ch.Holder(skill, policy.TagBeta)
	stableDigest, stableVersion := ch.Holder(skill, policy.TagStable)

	cols := map[[3]string]*column{}
	byDigest := map[string][]Run{}
	for _, r := range runs { // newest first
		key := [3]string{r.Agent, r.AgentVersion, r.Model}
		if cols[key] == nil {
			cols[key] = &column{agent: r.Agent, agentVersion: r.AgentVersion, model: r.Model}
		}
		cols[key].required = cols[key].required || r.Required
		byDigest[r.Digest] = append(byDigest[r.Digest], r)
	}
	order := make([]*column, 0, len(cols))
	for _, c := range cols {
		order = append(order, c)
	}
	slices.SortFunc(order, func(a, b *column) int {
		if a.required != b.required {
			if a.required {
				return -1
			}
			return 1
		}
		return strings.Compare(a.entry(), b.entry())
	})
	for _, c := range order {
		v.Columns = append(v.Columns, apiv1.ModelColumn{Agent: c.agent, AgentVersion: c.agentVersion, Model: c.model, Entry: c.entry(), Required: c.required})
	}
	var newest []Run
	for _, group := range byDigest {
		for _, c := range order {
			for _, r := range group {
				if r.Agent == c.agent && r.AgentVersion == c.agentVersion && r.Model == c.model {
					newest = append(newest, r)
					break
				}
			}
		}
	}
	states := s.trialStates(ctx, newest)
	for digest, group := range byDigest {
		row := apiv1.VersionRow{Version: group[0].Version, Digest: digest, Channels: orEmpty(ch.Tags(skill, digest)), Cells: make([]*apiv1.MatrixCell, len(order))}
		var latest []Run // the newest run per entry drives the decision
		for i, c := range order {
			for _, r := range group {
				if r.Agent == c.agent && r.AgentVersion == c.agentVersion && r.Model == c.model {
					row.Cells[i] = matrixCell(r, states[r.Name].state)
					latest = append(latest, r)
					break // newest first
				}
			}
		}
		d := decide(latest, stableInfo{Known: ch.Known(), This: digest == stableDigest && digest != "", Holder: stableVersion}, s.cfg.Thresholds)
		row.Decision = apiv1.Decision{Kind: d.Kind, Headline: d.Headline, Detail: d.Detail}
		v.Rows = append(v.Rows, row)
	}
	slices.SortFunc(v.Rows, func(a, b apiv1.VersionRow) int {
		return cmp.Or(newerFirst(a.Version, b.Version), strings.Compare(a.Digest, b.Digest))
	})
	return v
}

func matrixCell(r Run, trials string) *apiv1.MatrixCell {
	c := &apiv1.MatrixCell{
		Run: r.Name, Phase: r.Phase, Verdict: r.Verdict,
		WithSkill: r.With.ptr(), WithoutSkill: r.Without.ptr(), Lift: r.Lift.ptr(), N: r.N, Trials: trials,
	}
	if r.CI != "" {
		c.LiftCI = &apiv1.Interval{Lower: r.CILower, Upper: r.CIUpper, Level: r.Confidence}
	}
	if t := r.se.Status.Tiers; t != nil {
		c.Tiers = apiv1.Tiers{Tier1: t.Tier1, Tier2: t.Tier2, Tier3: t.Tier3}
	}
	return c
}

func (s *Server) apiModels(w http.ResponseWriter, r *http.Request) {
	if e := checkQuery(r.URL.Query()); e != nil {
		writeErr(w, e)
		return
	}
	runs, e := s.listRuns(r.Context())
	if e != nil {
		writeErr(w, e)
		return
	}
	v := apiv1.ModelList{Source: s.cfg.ModelsConfigMap, Items: []apiv1.ModelEntry{}}
	listed, err := s.modelList(r.Context())
	if err != nil {
		v.Error = err.Error()
	}
	used := make([]bool, len(runs))
	for _, m := range listed {
		item := newEntryStats(m.Agent, m.AgentVersion, m.Model, m.Required, m.SandboxProfile, true)
		for i, run := range runs {
			if run.se.Spec.Agent == m.Agent && run.se.Spec.AgentVersion == m.AgentVersion && run.Model == m.Model {
				used[i] = true
				item.add(run)
			}
		}
		v.Items = append(v.Items, item.result())
	}
	// Retired entries: runs whose entry is no longer listed, in entry order.
	retired := map[string]*entryStats{}
	var keys []string
	for i, run := range runs {
		if used[i] {
			continue
		}
		spec := run.se.Spec
		key := spec.Agent + "\x00" + spec.AgentVersion + "\x00" + spec.Model
		if retired[key] == nil {
			retired[key] = newEntryStats(spec.Agent, spec.AgentVersion, spec.Model, false, spec.SandboxProfile, false)
			keys = append(keys, key)
		}
		retired[key].Required = retired[key].Required || run.Required
		retired[key].add(run)
	}
	slices.SortFunc(keys, func(a, b string) int { return strings.Compare(retired[a].Entry, retired[b].Entry) })
	for _, k := range keys {
		v.Items = append(v.Items, retired[k].result())
	}
	s.writeView(w, r, &v)
}

// modelList reads the model list ConfigMap.
func (s *Server) modelList(ctx context.Context) ([]models.Model, error) {
	var cm corev1.ConfigMap
	err := s.cfg.Reader.Get(ctx, clientKey(s.cfg.Namespace, s.cfg.ModelsConfigMap), &cm)
	switch {
	case apierrors.IsNotFound(err):
		return nil, &apiError{message: "ConfigMap " + s.cfg.ModelsConfigMap + " does not exist, so only the entries of earlier runs are shown."}
	case err != nil:
		s.cfg.Log.Error(err, "reading the model list")
		return nil, &apiError{message: "ConfigMap " + s.cfg.ModelsConfigMap + " could not be read: " + err.Error()}
	}
	list, err := models.FromConfigMap(&cm)
	if err != nil {
		return nil, &apiError{message: "ConfigMap " + s.cfg.ModelsConfigMap + " is not a valid model list: " + err.Error()}
	}
	return list, nil
}

// entryStats sums the runs of one model list entry.
type entryStats struct {
	apiv1.ModelEntry
	liftSum float64
	lifts   int
}

func newEntryStats(agent, agentVersion, model string, required bool, profile string, listed bool) *entryStats {
	m := models.Model{Agent: agent, AgentVersion: agentVersion, Model: model}
	return &entryStats{ModelEntry: apiv1.ModelEntry{
		Agent: agent, AgentVersion: agentVersion, Model: model, Entry: m.Key(), Required: required,
		SandboxProfile: profile, Listed: listed, Verdicts: map[string]int{},
	}}
}

func (e *entryStats) add(r Run) {
	e.Runs++
	if r.Phase == string(v1alpha1.PhaseFailed) {
		e.Failed++
	}
	e.Retries += r.se.Status.Retries
	if r.Verdict != "" {
		e.Verdicts[r.Verdict]++
	}
	if e.LastRunAt == nil || r.Created.After(*e.LastRunAt) {
		e.LastRunAt = timePtr(r.Created)
	}
	if r.Lift.OK {
		e.liftSum += r.Lift.Value
		e.lifts++
	}
}

// result is the entry with its mean lift, rounded to three decimals.
func (e *entryStats) result() apiv1.ModelEntry {
	out := e.ModelEntry
	if e.lifts > 0 {
		mean := round(e.liftSum/float64(e.lifts), 3)
		out.MeanLift = &mean
	}
	return out
}
