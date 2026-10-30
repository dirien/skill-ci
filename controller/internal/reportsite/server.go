// Package reportsite serves the read-only reporting website of the controller:
// every SkillEval as a run, the release channels per skill, the policy
// decision per skill version, and the SkillEvaluator report of each run.
package reportsite

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/policy"
)

//go:embed web/templates/*.html web/site.css
var assets embed.FS

// pages are the page templates; each defines "content" for the shared layout.
var pages = []string{"index", "skill", "run", "error"}

// ReportStore gives access to the files of a stored report.
type ReportStore interface {
	// File returns one file of the report whose referrer manifest has the given
	// digest. It returns an error wrapping reportstore.ErrNotFound when the
	// report or the file is gone. The result must not be modified.
	File(ctx context.Context, skill, reportDigest, name string) ([]byte, error)
}

// Config wires the site to its data sources.
type Config struct {
	// Addr is the listen address, for example ":8082".
	Addr string
	// Reader lists and gets SkillEvals; the manager's cached client fits.
	Reader client.Reader
	// Namespace is where the SkillEvals live.
	Namespace string
	// Project is the Harbor project that holds the skills.
	Project string
	// Harbor reads the channel tags.
	Harbor harbor.Client
	// Reports reads the stored reports.
	Reports ReportStore
	// Thresholds are the controller policy bands, shown and used for decisions.
	Thresholds policy.Thresholds
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Log receives server and request errors.
	Log logr.Logger
}

// Server serves the site. It is an http.Handler and a manager Runnable.
type Server struct {
	cfg      Config
	channels *channelSource
	tmpl     map[string]*template.Template
	mux      *http.ServeMux
}

// New parses the embedded templates and returns the server.
func New(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	css, err := fs.ReadFile(assets, "web/site.css")
	if err != nil {
		return nil, fmt.Errorf("reading stylesheet: %w", err)
	}
	s := &Server{
		cfg:      cfg,
		channels: &channelSource{client: cfg.Harbor, now: cfg.Now},
		tmpl:     make(map[string]*template.Template, len(pages)),
		mux:      http.NewServeMux(),
	}
	for _, name := range pages {
		// The stylesheet is part of the template text, so it is trusted markup, not data.
		t, err := template.New(name).Funcs(funcs(cfg.Now)).Parse(`{{define "css"}}` + string(css) + `{{end}}`)
		if err != nil {
			return nil, fmt.Errorf("parsing stylesheet template: %w", err)
		}
		t, err = t.ParseFS(assets, "web/templates/layout.html", "web/templates/partials.html", "web/templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parsing %s template: %w", name, err)
		}
		s.tmpl[name] = t
	}
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /{$}", s.index)
	s.mux.HandleFunc("GET /skills/{skill}", s.skill)
	s.mux.HandleFunc("GET /runs/{name}", s.run)
	s.mux.HandleFunc("GET /runs/{name}/report", s.reportHTML)
	s.mux.HandleFunc("GET /runs/{name}/result.json", s.resultJSON)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, r, http.StatusNotFound, "Page not found", "There is no page at "+r.URL.Path+".")
	})
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	s.mux.ServeHTTP(w, r)
}

// page returns the layout data shared by all pages.
func (s *Server) page(title string, crumbs ...Crumb) Page {
	return Page{Title: title, Crumbs: crumbs, Namespace: s.cfg.Namespace, Project: s.cfg.Project, Generated: s.cfg.Now().UTC()}
}

// render executes the page template into a buffer first, so a template error
// becomes a 500 instead of a half-written page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl[name].ExecuteTemplate(&buf, "layout", data); err != nil {
		s.cfg.Log.Error(err, "rendering page", "page", name, "path", r.URL.Path)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	// No scripts, no remote resources: the pages are plain HTML and one inline stylesheet.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

type errorView struct {
	Page
	Heading, Message string
}

// fail renders an error page in the normal layout.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, heading, message string) {
	v := errorView{Page: s.page(heading, Crumb{"Runs", "/"}, Crumb{Label: heading}), Heading: heading, Message: message}
	s.render(w, r, status, "error", v)
}

// runs lists the SkillEvals of the namespace, newest first.
func (s *Server) runs(ctx context.Context) ([]Run, error) {
	var list v1alpha1.SkillEvalList
	if err := s.cfg.Reader.List(ctx, &list, client.InNamespace(s.cfg.Namespace)); err != nil {
		return nil, fmt.Errorf("listing SkillEvals: %w", err)
	}
	runs := make([]Run, 0, len(list.Items))
	for i := range list.Items {
		runs = append(runs, newRun(&list.Items[i]))
	}
	sortRuns(runs)
	return runs, nil
}

// Start serves until ctx is done, then shuts down gracefully.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{Addr: s.cfg.Addr, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	s.cfg.Log.Info("reports site listening", "addr", s.cfg.Addr)
	select {
	case err := <-errCh:
		return fmt.Errorf("serving reports site: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("shutting down reports site: %w", err)
		}
		return nil
	}
}

// NeedLeaderElection reports false: every replica serves the site.
func (s *Server) NeedLeaderElection() bool { return false }
