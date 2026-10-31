package reportsite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
	"github.com/dirien/skill-ci/controller/internal/reportstore"
	"github.com/dirien/skill-ci/controller/internal/session"
)

const (
	// maxStep and maxResult bound the step and result numbers of an output request.
	maxStep   = 1 << 20
	maxResult = 1 << 16
	// maxAttempt bounds the attempt query parameter of a comparison.
	maxAttempt = 1 << 16
	// outputCSP is the CSP of a full tool output: never a document, even when opened directly.
	outputCSP = apiv1.CSP + "; sandbox"
)

// trialReq is a request for something inside the trial bundle of one run attempt.
type trialReq struct {
	se   *v1alpha1.SkillEval
	run  Run
	at   attempt
	jobQ string
	// bundle is the bundle digest, from the report manifest named by the SkillEval status.
	bundle string
	idx    *session.Index
	red    *session.Redactor
	// fp is the fingerprint of the redaction values, part of every cache key.
	fp string
}

// resolveTrials validates the run, the query and the attempt, and reads the
// bundle index of the attempt. Registry paths are built from SkillEval status
// and the index only, never from the request.
func (s *Server) resolveTrials(r *http.Request, params ...string) (*trialReq, *apiError) {
	name, e := pathName(r)
	if e != nil {
		return nil, e
	}
	q := r.URL.Query()
	if e := checkQuery(q, append([]string{"job"}, params...)...); e != nil {
		return nil, e
	}
	ctx := r.Context()
	se, e := s.getRun(ctx, name)
	if e != nil {
		return nil, e
	}
	at, e := selectAttempt(se, q)
	if e != nil {
		return nil, e
	}
	tr := &trialReq{se: se, run: newRun(se, s.cfg.Thresholds.Percent()), at: at, jobQ: jobQuery(se, at)}
	if at.reportDigest == "" {
		return nil, notFound("Run %s stored no report%s, so it has no trials.", name, attemptText(at))
	}
	ref := s.bundleOf(ctx, tr.run.Skill, at.reportDigest)
	switch {
	case ref.gone:
		return nil, notFound("The report of run %s is no longer in the registry.", name)
	case ref.state == apiv1.TrialsUnknown:
		s.cfg.Log.Error(ref.err, "reading report annotations", "run", name)
		return nil, registryError("Harbor did not return the report manifest of run %s: %s", name, errText(ref.err))
	case ref.state == apiv1.TrialsWithheld:
		return nil, withheld("The runner withheld the trial bundle of run %s because %s.", name, withheldText(ref.reason))
	case ref.state != apiv1.TrialsStored:
		return nil, notFound("Run %s has no trial bundle.", name)
	}
	tr.bundle = ref.digest
	idx, err := s.index(ctx, tr.run.Skill, ref.digest)
	switch {
	case errors.Is(err, reportstore.ErrNotFound):
		return nil, notFound("The trial bundle of run %s is no longer in the registry.", name)
	case err != nil:
		s.cfg.Log.Error(err, "reading the bundle index", "run", name)
		return nil, registryError("The trial bundle index of run %s could not be read: %v", name, err)
	}
	tr.idx = idx
	red, fp, err := s.secrets.redactor(ctx)
	if err != nil {
		s.cfg.Log.Error(err, "render-time redaction is not available")
		return nil, unavailable("The %s Secret could not be read, so sessions cannot be redacted: %v", s.cfg.LLMKeysSecret, err)
	}
	tr.red, tr.fp = red, fp
	return tr, nil
}

// entry finds a trial of the index by id. A withheld trial is a 404 withheld.
func (tr *trialReq) entry(id string) (*session.IndexTrial, *apiError) {
	for i := range tr.idx.Trials {
		if tr.idx.Trials[i].ID == id {
			return &tr.idx.Trials[i], nil
		}
	}
	for _, w := range tr.idx.Withheld {
		if w.ID == id {
			return nil, withheld("The runner withheld trial %s (%s).", id, w.Reason)
		}
	}
	return nil, notFound("Trial %s is not in the trial bundle of run %s.", id, tr.run.Name)
}

// trial reads and decodes the trial document of an index entry.
func (s *Server) trial(ctx context.Context, tr *trialReq, e *session.IndexTrial) (*session.Trial, *apiError) {
	if s.cfg.Bundles == nil {
		return nil, notFound("This controller reads no trial bundles.")
	}
	data, err := s.cfg.Bundles.Gunzip(ctx, tr.run.Skill, tr.bundle, session.TrialLayer(e.ID), session.MaxTrialBytes)
	switch {
	case errors.Is(err, reportstore.ErrNotFound):
		return nil, notFound("The document of trial %s is no longer in the registry.", e.ID)
	case errors.Is(err, reportstore.ErrTooLarge):
		return nil, registryError("The document of trial %s is larger than %d bytes.", e.ID, session.MaxTrialBytes)
	case err != nil:
		s.cfg.Log.Error(err, "reading a trial document", "run", tr.run.Name, "trial", e.ID)
		return nil, registryError("Harbor did not return the document of trial %s: %v", e.ID, err)
	}
	doc, err := session.DecodeTrial(data)
	if err != nil {
		return nil, registryError("The document of trial %s could not be read: %v", e.ID, err)
	}
	if doc.ID != e.ID {
		return nil, registryError("The document of trial %s names trial %q.", e.ID, doc.ID)
	}
	return doc, nil
}

// runRef names the run and attempt in a session view.
func (tr *trialReq) runRef() apiv1.RunRef {
	r := tr.run
	return apiv1.RunRef{
		Name: r.Name, Skill: r.Skill, Version: r.Version, Agent: r.Agent, AgentVersion: r.AgentVersion,
		Model: r.Model, Entry: r.Entry(), Job: tr.at.job, CurrentJob: tr.jobQ == "",
	}
}

// trialBase is the API path of a trial.
func (tr *trialReq) trialBase(id string) string {
	return apiv1.Prefix + "/runs/" + url.PathEscape(tr.run.Name) + "/trials/" + url.PathEscape(id)
}

func (tr *trialReq) outputHref(id string, step, k int) string {
	return tr.trialBase(id) + "/steps/" + strconv.Itoa(step) + "/output/" + strconv.Itoa(k) + tr.jobQ
}

// reportResult reads result.json of the attempt for the arm means and the
// suggestions; nil when it cannot be read (the session still renders).
func (s *Server) reportResult(ctx context.Context, tr *trialReq) *reportResult {
	res, err := s.result(ctx, tr.run, tr.at.reportDigest)
	if err != nil {
		return nil
	}
	return res
}

// sessionError maps a session error to an API error.
func sessionError(err error, what string) *apiError {
	switch {
	case errors.Is(err, session.ErrLeak):
		return internalError("The %s still held a secret value after redaction, so it is withheld.", what)
	case errors.Is(err, session.ErrNotFound):
		return notFound("The %s does not exist: %v", what, err)
	default:
		return internalError("The %s could not be built: %v", what, err)
	}
}

// rendered is a response body with its headers.
type rendered struct {
	contentType string
	csp         string
	disposition string
}

// serveRendered answers from the response cache, or builds, caches and
// answers. The ETag covers the release, the redaction values, the bundle
// digest, the route and the query, so a conditional request gets a 304.
func (s *Server) serveRendered(w http.ResponseWriter, r *http.Request, tr *trialReq, key string, meta rendered, build func() ([]byte, *apiError)) {
	key = tr.fp + "\x00" + tr.bundle + "\x00" + key + "\x00" + tr.jobQ
	sum := sha256.Sum256([]byte(s.cfg.Version + "\x00" + key))
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	body, ok := s.rendered.get(key)
	if !ok {
		var e *apiError
		if body, e = build(); e != nil {
			writeErr(w, e)
			return
		}
		s.rendered.put(key, body)
	}
	h := w.Header()
	h.Set("Content-Type", meta.contentType)
	h.Set("Cache-Control", cacheSession)
	h.Set("Content-Security-Policy", meta.csp)
	h.Set("ETag", etag)
	if meta.disposition != "" {
		h.Set("Content-Disposition", meta.disposition)
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

var jsonMeta = rendered{contentType: "application/json; charset=utf-8", csp: apiv1.CSP}

func (s *Server) apiTrial(w http.ResponseWriter, r *http.Request) {
	id, e := pathID(r, "trial", "trial")
	if e != nil {
		writeErr(w, e)
		return
	}
	tr, e := s.resolveTrials(r)
	if e != nil {
		writeErr(w, e)
		return
	}
	entry, e := tr.entry(id)
	if e != nil {
		writeErr(w, e)
		return
	}
	s.serveRendered(w, r, tr, "trial\x00"+id, jsonMeta, func() ([]byte, *apiError) {
		ctx := r.Context()
		doc, e := s.trial(ctx, tr, entry)
		if e != nil {
			return nil, e
		}
		in := session.Input{
			Run: tr.runRef(), Trigger: tr.run.Trigger, PushedBy: tr.run.PushedBy, Index: tr.idx, Trial: doc, Redactor: tr.red,
			OutputHref:     func(step, k int) string { return tr.outputHref(id, step, k) },
			TrajectoryHref: tr.trialBase(id) + "/trajectory.json" + tr.jobQ,
		}
		if res := s.reportResult(ctx, tr); res != nil {
			in.ArmMeans, in.Suggestions = res.ArmMeans[doc.Arm], res.Suggestions
		}
		view, err := session.Build(in)
		if err != nil {
			s.cfg.Log.Error(err, "building a session", "run", tr.run.Name, "trial", id)
			return nil, sessionError(err, "session")
		}
		return encodeChecked(view, tr.red)
	})
}

// encodeChecked encodes a view and fails closed when a secret value is in it.
func encodeChecked(v any, red *session.Redactor) ([]byte, *apiError) {
	body, err := encodeJSON(v)
	if err != nil {
		return nil, internalError("The response could not be encoded.")
	}
	if red.Leaks(body) {
		return nil, sessionError(session.ErrLeak, "response")
	}
	return body, nil
}

func (s *Server) apiOutput(w http.ResponseWriter, r *http.Request) {
	id, e := pathID(r, "trial", "trial")
	if e != nil {
		writeErr(w, e)
		return
	}
	step, e := pathInt(r, "step", 1, maxStep)
	if e != nil {
		writeErr(w, e)
		return
	}
	k, e := pathInt(r, "k", 0, maxResult)
	if e != nil {
		writeErr(w, e)
		return
	}
	tr, e := s.resolveTrials(r)
	if e != nil {
		writeErr(w, e)
		return
	}
	entry, e := tr.entry(id)
	if e != nil {
		writeErr(w, e)
		return
	}
	meta := rendered{contentType: "text/plain; charset=utf-8", csp: outputCSP}
	s.serveRendered(w, r, tr, fmt.Sprintf("output\x00%s\x00%d\x00%d", id, step, k), meta, func() ([]byte, *apiError) {
		doc, e := s.trial(r.Context(), tr, entry)
		if e != nil {
			return nil, e
		}
		text, err := session.Output(doc, step, k, tr.red)
		if err != nil {
			return nil, sessionError(err, fmt.Sprintf("output %d of step %d", k, step))
		}
		return []byte(text), nil
	})
}

func (s *Server) apiTrajectory(w http.ResponseWriter, r *http.Request) {
	id, e := pathID(r, "trial", "trial")
	if e != nil {
		writeErr(w, e)
		return
	}
	tr, e := s.resolveTrials(r)
	if e != nil {
		writeErr(w, e)
		return
	}
	entry, e := tr.entry(id)
	if e != nil {
		writeErr(w, e)
		return
	}
	meta := rendered{contentType: "application/json", csp: apiv1.CSP, disposition: `attachment; filename="` + id + `-trajectory.json"`}
	s.serveRendered(w, r, tr, "trajectory\x00"+id, meta, func() ([]byte, *apiError) {
		doc, e := s.trial(r.Context(), tr, entry)
		if e != nil {
			return nil, e
		}
		data, err := session.TrajectoryFile(doc, tr.red)
		if err != nil {
			return nil, sessionError(err, "trajectory of trial "+id)
		}
		return data, nil
	})
}

func (s *Server) apiCompare(w http.ResponseWriter, r *http.Request) {
	caseID, e := pathID(r, "case", "case")
	if e != nil {
		writeErr(w, e)
		return
	}
	tr, e := s.resolveTrials(r, "attempt", "with", "without")
	if e != nil {
		writeErr(w, e)
		return
	}
	pair, e := comparePair(tr, caseID, r.URL.Query())
	if e != nil {
		writeErr(w, e)
		return
	}
	key := fmt.Sprintf("compare\x00%s\x00%d\x00%s\x00%s", caseID, pair.attempt, pair.ids[0], pair.ids[1])
	s.serveRendered(w, r, tr, key, jsonMeta, func() ([]byte, *apiError) {
		ctx := r.Context()
		var docs [2]*session.Trial
		for i, entry := range pair.entries {
			if entry == nil {
				continue
			}
			doc, e := s.trial(ctx, tr, entry)
			if e != nil && e.status != http.StatusNotFound {
				return nil, e
			}
			docs[i] = doc
		}
		in := session.CompareInput{
			Run: tr.runRef(), Trigger: tr.run.Trigger, PushedBy: tr.run.PushedBy, Index: tr.idx,
			Case: caseID, Attempt: pair.attempt, WithSkill: docs[0], WithoutSkill: docs[1], Redactor: tr.red,
			OutputHref: tr.outputHref,
		}
		if res := s.reportResult(ctx, tr); res != nil {
			in.ArmMeans = res.ArmMeans
		}
		view, err := session.Compare(in)
		if err != nil {
			return nil, sessionError(err, "comparison")
		}
		return encodeChecked(view, tr.red)
	})
}

// pair is the two trials of a comparison: with-skill first.
type pair struct {
	attempt int
	ids     [2]string
	entries [2]*session.IndexTrial
}

// comparePair resolves the case and the trials to compare: attempt k of
// both arms (?attempt=, default 1), or the trials named by ?with= and ?without=.
func comparePair(tr *trialReq, caseID string, q url.Values) (pair, *apiError) {
	known := false
	for _, c := range tr.idx.Cases {
		known = known || c.ID == caseID
	}
	if !known {
		return pair{}, notFound("Case %s is not in the trial bundle of run %s.", caseID, tr.run.Name)
	}
	att, e := intParam(q, "attempt", 1, maxAttempt, 0)
	if e != nil {
		return pair{}, e
	}
	var p pair
	arms := [2]string{apiv1.ArmWithSkill, apiv1.ArmWithoutSkill}
	for i, key := range []string{"with", "without"} {
		id := q.Get(key)
		if id == "" {
			continue
		}
		if !session.TrialID.MatchString(id) {
			return pair{}, badRequest("Query parameter %q: %q is not a valid trial id.", key, id)
		}
		a, ok := armTrial(tr.idx, id, arms[i], caseID)
		if !ok {
			return pair{}, notFound("No %s trial %s of case %s in the trial bundle.", arms[i], id, caseID)
		}
		if att != 0 && a != att {
			return pair{}, badRequest("Trial %s is attempt %d, not attempt %d.", id, a, att)
		}
		att = a
	}
	if att == 0 {
		att = 1
	}
	p.attempt = att
	for i, arm := range arms {
		want := q.Get([]string{"with", "without"}[i])
		for j := range tr.idx.Trials {
			e := &tr.idx.Trials[j]
			if e.Arm == arm && e.Case == caseID && (e.ID == want || (want == "" && e.Attempt == att)) {
				p.entries[i], p.ids[i] = e, e.ID
				break
			}
		}
	}
	return p, nil
}

// armTrial returns the attempt of a trial of the given arm and case, bundled or withheld.
func armTrial(idx *session.Index, id, arm, caseID string) (int, bool) {
	for _, e := range idx.Trials {
		if e.ID == id && e.Arm == arm && e.Case == caseID {
			return e.Attempt, true
		}
	}
	for _, w := range idx.Withheld {
		if w.ID == id && w.Arm == arm && w.Case == caseID {
			return w.Attempt, true
		}
	}
	return 0, false
}

// pathInt returns a path value as a whole number in [lo, hi].
func pathInt(r *http.Request, key string, lo, hi int) (int, *apiError) {
	v := r.PathValue(key)
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi || v != strconv.Itoa(n) {
		return 0, badRequest("%s must be a whole number from %d to %d, got %q.", key, lo, hi, v)
	}
	return n, nil
}
