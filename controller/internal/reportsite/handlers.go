package reportsite

import (
	"bytes"
	"errors"
	"net/http"
	"path"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/reportstore"
)

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	runs, err := s.runs(r.Context())
	if err != nil {
		s.cfg.Log.Error(err, "listing runs")
		s.fail(w, r, http.StatusServiceUnavailable, "Cannot read runs", "The SkillEvals could not be listed: "+err.Error())
		return
	}
	v := buildIndex(s.page("Runs", Crumb{Label: "Runs"}), runs, s.channels.get(r.Context()), filterFrom(r.URL.Query()), s.cfg.Thresholds)
	s.render(w, r, http.StatusOK, "index", v)
}

func (s *Server) skill(w http.ResponseWriter, r *http.Request) {
	skill := r.PathValue("skill")
	all, err := s.runs(r.Context())
	if err != nil {
		s.cfg.Log.Error(err, "listing runs")
		s.fail(w, r, http.StatusServiceUnavailable, "Cannot read runs", "The SkillEvals could not be listed: "+err.Error())
		return
	}
	ch := s.channels.get(r.Context())
	runs := all[:0:0]
	for _, run := range all {
		if run.Skill == skill {
			runs = append(runs, run)
		}
	}
	if len(runs) == 0 && !slices.Contains(ch.Skills(), skill) {
		s.fail(w, r, http.StatusNotFound, "Skill not found", "No skill named "+skill+" has runs or a repository in Harbor.")
		return
	}
	page := s.page(skill, Crumb{"Runs", "/"}, Crumb{Label: skill})
	s.render(w, r, http.StatusOK, "skill", buildSkill(page, skill, runs, ch, s.cfg.Thresholds))
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	run, ok := s.lookup(w, r)
	if !ok {
		return
	}
	page := s.page(run.Name, Crumb{"Runs", "/"}, Crumb{run.Skill, "/skills/" + run.Skill}, Crumb{Label: run.Name})
	v := runView{Page: page, Run: run, Channels: s.channels.get(r.Context()).Tags(run.Skill, run.Digest), Report: s.reportView(r, run)}
	s.render(w, r, http.StatusOK, "run", v)
}

func (s *Server) reportView(r *http.Request, run Run) ReportView {
	if !run.HasReport() {
		return ReportView{State: "none"}
	}
	if run.NoScores() {
		// Tier 3 did not run: there are no dimension scores to read, only the stored files.
		return ReportView{State: "files"}
	}
	data, err := s.cfg.Reports.File(r.Context(), run.Skill, run.ReportDigest, reportstore.FileResult)
	switch {
	case errors.Is(err, reportstore.ErrNotFound):
		return ReportView{State: "missing"}
	case err != nil:
		s.cfg.Log.Error(err, "reading report", "run", run.Name)
		return ReportView{State: "error", Err: err.Error()}
	}
	res, err := parseResult(data, run.Agent)
	if err != nil {
		return ReportView{State: "error", Err: err.Error()}
	}
	return ReportView{State: "ok", Result: res}
}

func (s *Server) reportHTML(w http.ResponseWriter, r *http.Request) {
	data, run, ok := s.reportFile(w, r, reportstore.FileReport)
	if !ok {
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "private, max-age=3600")
	// The report is served as stored and loads Chart.js from a CDN. Keep it from
	// posting data anywhere or being framed by other sites.
	h.Set("Content-Security-Policy", "connect-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'")
	serveFile(w, r, run, data)
}

func (s *Server) resultJSON(w http.ResponseWriter, r *http.Request) {
	data, run, ok := s.reportFile(w, r, reportstore.FileResult)
	if !ok {
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Disposition", `attachment; filename="`+run.Name+`-result.json"`)
	h.Set("Cache-Control", "private, max-age=3600")
	serveFile(w, r, run, data)
}

// serveFile writes a stored report file. The content is immutable per report
// digest, so the digest doubles as the ETag and conditional requests work.
func serveFile(w http.ResponseWriter, r *http.Request, run Run, data []byte) {
	etag := `"` + run.ReportDigest + `-` + path.Base(r.URL.Path) + `"`
	w.Header().Set("ETag", etag)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// reportFile loads one file of the run's report, or writes the error page.
func (s *Server) reportFile(w http.ResponseWriter, r *http.Request, name string) ([]byte, Run, bool) {
	run, ok := s.lookup(w, r)
	if !ok {
		return nil, Run{}, false
	}
	if !run.HasReport() {
		s.fail(w, r, http.StatusNotFound, "No report stored", "Run "+run.Name+" has no stored report. Runs on runner versions before 0.3.0 did not attach one.")
		return nil, run, false
	}
	data, err := s.cfg.Reports.File(r.Context(), run.Skill, run.ReportDigest, name)
	switch {
	case errors.Is(err, reportstore.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "Report not found", "The registry has no "+name+" for run "+run.Name+" any more.")
		return nil, run, false
	case err != nil:
		s.cfg.Log.Error(err, "reading report file", "run", run.Name, "file", name)
		s.fail(w, r, http.StatusBadGateway, "Cannot read the report", "Harbor did not return "+name+": "+err.Error())
		return nil, run, false
	}
	return data, run, true
}

// lookup finds the run named in the path, or writes the 404 or 503 page.
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (Run, bool) {
	name := r.PathValue("name")
	var se v1alpha1.SkillEval
	err := s.cfg.Reader.Get(r.Context(), client.ObjectKey{Namespace: s.cfg.Namespace, Name: name}, &se)
	switch {
	case apierrors.IsNotFound(err):
		s.fail(w, r, http.StatusNotFound, "Run not found", "No run named "+name+" in "+s.cfg.Namespace+". Runs are removed when their SkillEval is deleted.")
		return Run{}, false
	case err != nil:
		s.cfg.Log.Error(err, "reading run", "run", name)
		s.fail(w, r, http.StatusServiceUnavailable, "Cannot read the run", err.Error())
		return Run{}, false
	}
	return newRun(&se), true
}
