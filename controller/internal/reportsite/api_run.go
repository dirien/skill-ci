package reportsite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
	"github.com/dirien/skill-ci/controller/internal/reportstore"
	"github.com/dirien/skill-ci/controller/internal/session"
)

// attempt is one Job of a run: an earlier one from status.attempts, or the current one.
type attempt struct {
	number       int
	job          string
	reportDigest string
	message      string
	current      bool
}

// attempts returns the Jobs of a run, oldest first.
func attempts(se *v1alpha1.SkillEval) []attempt {
	var out []attempt
	for i, a := range se.Status.Attempts {
		out = append(out, attempt{number: i + 1, job: a.JobName, reportDigest: a.ReportDigest, message: a.Message})
	}
	if job := se.Status.JobName; job != "" && (len(out) == 0 || out[len(out)-1].job != job) {
		out = append(out, attempt{number: len(out) + 1, job: job, reportDigest: se.Status.ReportDigest, current: true})
	} else if len(out) > 0 && se.Status.ReportDigest != "" {
		out[len(out)-1].current = true
	}
	return out
}

// selectAttempt picks the attempt of the job query parameter, or the newest
// one. A run without a Job yet has the zero attempt.
func selectAttempt(se *v1alpha1.SkillEval, q url.Values) (attempt, *apiError) {
	list := attempts(se)
	job := q.Get("job")
	if job == "" {
		if len(list) == 0 {
			return attempt{current: true}, nil
		}
		return list[len(list)-1], nil
	}
	if !jobName.MatchString(job) {
		return attempt{}, badRequest("%q is not a valid Job name.", job)
	}
	for _, a := range list {
		if a.job == job {
			return a, nil
		}
	}
	return attempt{}, notFound("Run %s has no Job named %s.", se.Name, job)
}

// jobQuery is "?job=<job>" for an earlier attempt, empty for the newest one.
func jobQuery(se *v1alpha1.SkillEval, a attempt) string {
	list := attempts(se)
	if a.job == "" || (len(list) > 0 && list[len(list)-1].job == a.job) {
		return ""
	}
	return "?job=" + url.QueryEscape(a.job)
}

func (s *Server) apiRun(w http.ResponseWriter, r *http.Request) {
	name, e := pathName(r)
	if e != nil {
		writeErr(w, e)
		return
	}
	q := r.URL.Query()
	if e := checkQuery(q, "job"); e != nil {
		writeErr(w, e)
		return
	}
	se, e := s.getRun(r.Context(), name)
	if e != nil {
		writeErr(w, e)
		return
	}
	at, e := selectAttempt(se, q)
	if e != nil {
		writeErr(w, e)
		return
	}
	v := s.runDetail(r.Context(), se, at)
	s.writeView(w, r, &v)
}

func (s *Server) runDetail(ctx context.Context, se *v1alpha1.SkillEval, at attempt) apiv1.RunDetail {
	run := newRun(se, s.cfg.Thresholds.Percent())
	ref := s.bundleOf(ctx, run.Skill, run.ReportDigest)
	v := apiv1.RunDetail{
		Run:              s.runSummary(run, ref.state),
		SpecAgentVersion: se.Spec.AgentVersion,
		Channels:         orEmpty(s.channels.get(ctx).Tags(run.Skill, run.Digest)),
		Findings:         orEmpty(se.Status.Findings),
		Conditions:       []apiv1.Condition{},
		Regressed:        run.Regressed, Inconclusive: run.Inconclusive, Retried: run.Retried,
		Network:        runNetwork(se),
		Sandbox:        apiv1.Sandbox{Profile: se.Status.SandboxProfile, SpecProfile: se.Spec.SandboxProfile, WarmPool: se.Status.WarmPool},
		Attempts:       []apiv1.RunAttempt{},
		RefreshSeconds: refresh([]Run{run}),
	}
	v.WithSkillStdErr, v.WithoutSkillStdErr = newScore(se.Status.WithSkillStdErr).ptr(), newScore(se.Status.WithoutSkillStdErr).ptr()
	v.Samples = apiv1.Samples{WithSkill: []float64{}, WithoutSkill: []float64{}}
	if sm := se.Status.Samples; sm != nil {
		v.Samples.WithSkill, v.Samples.WithoutSkill = parseSamples(sm.WithSkill), parseSamples(sm.WithoutSkill)
	}
	for _, c := range se.Status.Conditions {
		v.Conditions = append(v.Conditions, apiv1.Condition{
			Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message, ChangedAt: c.LastTransitionTime.UTC(),
		})
	}
	for _, a := range attempts(se) {
		state := ref.state
		if !a.current {
			state = s.bundleOf(ctx, run.Skill, a.reportDigest).state
		}
		v.Attempts = append(v.Attempts, apiv1.RunAttempt{
			Number: a.number, JobName: a.job, ReportDigest: a.reportDigest, Message: a.message, Current: a.current, Trials: state,
		})
	}
	v.Report, _ = s.report(ctx, run, at, jobQuery(se, at))
	v.Trials = s.grid(ctx, run, at)
	return v
}

func parseSamples(list []string) []float64 {
	out := []float64{}
	for _, t := range list {
		if sc := newScore(t); sc.OK {
			out = append(out, sc.Value)
		}
	}
	return out
}

func runNetwork(se *v1alpha1.SkillEval) apiv1.RunNetwork {
	n := apiv1.RunNetwork{Declared: []string{}, Hosts: []string{}}
	if spec := se.Spec.Network; spec != nil {
		n.Mode, n.Source, n.Declared = spec.Mode, spec.Source, orEmpty(spec.Allow)
	}
	if st := se.Status.Network; st != nil {
		if st.Mode != "" {
			n.Mode = st.Mode
		}
		n.Hosts, n.Policy, n.Enforced = orEmpty(st.Hosts), st.Policy, st.Enforced
	}
	for _, c := range se.Status.Conditions {
		if c.Type == conditionNetworkPolicy {
			n.Reason, n.Message = c.Reason, c.Message
		}
	}
	n.Legacy = n.Mode == ""
	return n
}

// report reads the report of one attempt. The parsed result.json comes back
// for the trial grid and the sessions; nil when it was not read.
func (s *Server) report(ctx context.Context, run Run, at attempt, jobQ string) (apiv1.Report, *reportResult) {
	rep := apiv1.Report{State: apiv1.ReportNone, Dimensions: []apiv1.Dimension{}, Files: []apiv1.ReportFile{}}
	if at.reportDigest == "" {
		return rep, nil
	}
	rep.Digest = at.reportDigest
	base := "/runs/" + url.PathEscape(run.Name)
	rep.Files = []apiv1.ReportFile{
		{Name: reportstore.FileReport, Href: base + "/report" + jobQ},
		{Name: reportstore.FileResult, Href: base + "/result.json" + jobQ},
	}
	if at.current && run.Tier3Skipped() {
		// Tier 3 did not run: there are no dimension scores to read, only the stored files.
		rep.State = apiv1.ReportFiles
		return rep, nil
	}
	res, err := s.result(ctx, run, at.reportDigest)
	switch {
	case errors.Is(err, reportstore.ErrNotFound):
		rep.State, rep.Files = apiv1.ReportMissing, []apiv1.ReportFile{}
		return rep, nil
	case err != nil:
		rep.State, rep.Error = apiv1.ReportError, err.Error()
		return rep, nil
	}
	rep.Activation = res.Activation
	if len(res.Dimensions) == 0 {
		rep.State = apiv1.ReportFiles
		return rep, res
	}
	rep.State, rep.Dimensions = apiv1.ReportOK, res.Dimensions
	return rep, res
}

// result reads and parses result.json of a report.
func (s *Server) result(ctx context.Context, run Run, reportDigest string) (*reportResult, error) {
	if !digestPattern.MatchString(reportDigest) {
		return nil, errors.New("the report digest in the SkillEval status is not a sha256 digest")
	}
	data, err := s.cfg.Reports.File(ctx, run.Skill, reportDigest, reportstore.FileResult)
	if err != nil {
		if !errors.Is(err, reportstore.ErrNotFound) {
			s.cfg.Log.Error(err, "reading result.json", "run", run.Name)
		}
		return nil, fmt.Errorf("reading result.json: %w", err)
	}
	res, err := parseResult(data, run.Agent)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// grid builds the trial grid of one attempt: from the bundle index when there
// is one, else from result.json without links.
func (s *Server) grid(ctx context.Context, run Run, at attempt) apiv1.TrialGrid {
	none := func(state, reason string) apiv1.TrialGrid {
		return apiv1.TrialGrid{
			State: state, Reason: reason, Job: at.job, Cases: []apiv1.GridCase{},
			Credentials: []apiv1.Credential{}, Withheld: []apiv1.WithheldTrial{},
		}
	}
	if at.reportDigest == "" {
		return none(apiv1.GridNone, run.noReportReason(at.current))
	}
	if at.current && run.Tier3Skipped() {
		return none(apiv1.GridNone, "Tier 3 not run: "+run.RejectedBy()+" rejected this version, so no live trials were spent.")
	}
	ref := s.bundleOf(ctx, run.Skill, at.reportDigest)
	switch {
	case ref.gone:
		return none(apiv1.GridMissing, "The report is no longer in the registry.")
	case ref.state == apiv1.TrialsUnknown:
		return none(apiv1.GridError, "The report manifest could not be read: "+errText(ref.err))
	case ref.state == apiv1.TrialsStored:
		idx, err := s.index(ctx, run.Skill, ref.digest)
		switch {
		case errors.Is(err, reportstore.ErrNotFound):
			return none(apiv1.GridMissing, "The trial bundle is no longer in the registry.")
		case err != nil:
			return none(apiv1.GridError, "The trial bundle index could not be read: "+err.Error())
		}
		g, err := session.Grid(idx, at.job, ref.digest)
		if err != nil {
			return none(apiv1.GridError, err.Error())
		}
		return g
	case ref.reason == withheldNoTrials:
		return none(apiv1.GridNone, "No trials stored: "+withheldText(ref.reason)+".")
	}
	// No bundle: the scores of result.json, without sessions.
	state, reason := apiv1.GridFallback, "This run has no trial bundle (runner before trial bundles): scores from result.json, no sessions."
	switch {
	case ref.reason == withheldDisabled:
		reason = "This run has no trial bundle: " + withheldText(ref.reason) + ". Scores from result.json, no sessions."
	case ref.state == apiv1.TrialsWithheld:
		state = apiv1.GridWithheld
		reason = "The runner withheld the trial bundle (" + ref.reason + "): " + withheldText(ref.reason) + ". Scores from result.json, no sessions."
	}
	data, err := s.cfg.Reports.File(ctx, run.Skill, at.reportDigest, reportstore.FileResult)
	switch {
	case errors.Is(err, reportstore.ErrNotFound):
		return none(apiv1.GridMissing, "The report's result.json is no longer in the registry.")
	case err != nil:
		return none(apiv1.GridError, "result.json could not be read: "+err.Error())
	}
	g, err := session.FallbackGrid(data, run.Agent, at.job)
	if err != nil {
		// result.json without trials (Tier 3 left none): nothing to show.
		return none(state, reason)
	}
	g.State, g.Reason = state, reason
	return g
}

func errText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

// index reads and decodes the index of a trial bundle.
func (s *Server) index(ctx context.Context, skill, bundleDigest string) (*session.Index, error) {
	data, err := s.cfg.Reports.File(ctx, skill, bundleDigest, session.IndexFile)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", session.IndexFile, err)
	}
	idx, err := session.DecodeIndex(data)
	if err != nil {
		return nil, fmt.Errorf("decoding %s: %w", session.IndexFile, err)
	}
	return idx, nil
}

func (s *Server) reportHTML(w http.ResponseWriter, r *http.Request) {
	data, digest, ok := s.reportFile(w, r, reportstore.FileReport)
	if !ok {
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "private, max-age=3600")
	// The report is served as stored and loads Chart.js from a CDN. Keep it from
	// posting data anywhere or being framed by other sites.
	h.Set("Content-Security-Policy", "connect-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'")
	serveStored(w, r, digest, data)
}

func (s *Server) resultJSON(w http.ResponseWriter, r *http.Request) {
	data, digest, ok := s.reportFile(w, r, reportstore.FileResult)
	if !ok {
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Disposition", `attachment; filename="`+r.PathValue("name")+`-result.json"`)
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("Content-Security-Policy", apiv1.CSP)
	serveStored(w, r, digest, data)
}

// serveStored writes a stored report file. The content is immutable per report
// digest, so the digest doubles as the ETag and conditional requests work.
func serveStored(w http.ResponseWriter, r *http.Request, digest string, data []byte) {
	w.Header().Set("ETag", `"`+digest+`-`+path.Base(r.URL.Path)+`"`)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// reportFile loads one file of the report of the run's newest attempt, or of
// the attempt named by ?job=. Errors are plain text: these are page routes.
func (s *Server) reportFile(w http.ResponseWriter, r *http.Request, file string) ([]byte, string, bool) {
	fail := func(status int, msg string) ([]byte, string, bool) {
		w.Header().Set("Cache-Control", cacheNoStore)
		w.Header().Set("Content-Security-Policy", apiv1.CSP)
		http.Error(w, msg, status)
		return nil, "", false
	}
	name, e := pathName(r)
	if e == nil {
		e = checkQuery(r.URL.Query(), "job")
	}
	if e != nil {
		return fail(e.status, e.message)
	}
	se, e := s.getRun(r.Context(), name)
	if e != nil {
		return fail(e.status, e.message)
	}
	at, e := selectAttempt(se, r.URL.Query())
	if e != nil {
		return fail(e.status, e.message)
	}
	if at.reportDigest == "" || !digestPattern.MatchString(at.reportDigest) {
		return fail(http.StatusNotFound, "Run "+se.Name+" has no stored report"+attemptText(at)+".")
	}
	data, err := s.cfg.Reports.File(r.Context(), se.Spec.Skill, at.reportDigest, file)
	switch {
	case errors.Is(err, reportstore.ErrNotFound):
		return fail(http.StatusNotFound, "The registry has no "+file+" for run "+se.Name+" any more.")
	case err != nil:
		s.cfg.Log.Error(err, "reading report file", "run", se.Name, "file", file)
		return fail(http.StatusBadGateway, "Harbor did not return "+file+": "+err.Error())
	}
	return data, at.reportDigest, true
}

func attemptText(a attempt) string {
	if a.current || a.number == 0 {
		return ""
	}
	return " for attempt " + strconv.Itoa(a.number)
}
