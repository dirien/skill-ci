// Package reportsite serves the read-only reporting website of the controller:
// the single page app built from ui/ and the JSON API under /api/v1/ it reads
// (CONTRACT.md, "Reporting site"), plus the stored SkillEvaluator report files.
package reportsite

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/platform"
	"github.com/dirien/skill-ci/controller/internal/policy"
)

// DefaultSessionCacheBytes bounds the rendered trial session, compare,
// output and trajectory responses the site keeps in memory.
const DefaultSessionCacheBytes = 32 << 20

// ReportStore gives access to the files of a stored report or trial bundle.
type ReportStore interface {
	// File returns one file of the referrer whose manifest has the given
	// digest. It returns an error wrapping reportstore.ErrNotFound when the
	// manifest or the file is gone. The result must not be modified.
	File(ctx context.Context, skill, manifestDigest, name string) ([]byte, error)
}

// BundleStore reads trial bundles and report annotations from the registry;
// reportstore.OCI implements it.
type BundleStore interface {
	// Annotations returns the annotations of a referrer manifest. It returns an
	// error wrapping reportstore.ErrNotFound when the manifest is gone.
	Annotations(ctx context.Context, skill, manifestDigest string) (map[string]string, error)
	// Gunzip returns one gzip layer of a referrer, decompressed, at most limit
	// bytes (reportstore.ErrTooLarge past them).
	Gunzip(ctx context.Context, skill, manifestDigest, name string, limit int64) ([]byte, error)
}

// Config wires the site to its data sources.
type Config struct {
	// Addr is the listen address, for example ":8082".
	Addr string
	// Reader lists and gets SkillEvals, the model list ConfigMap and the
	// SkillCIConfig; the manager's cached client fits.
	Reader client.Reader
	// Secrets reads the llm-keys Secret for render-time redaction; the
	// manager's API reader fits, so no Secret informer is started. Nil
	// redacts the Harbor credentials and the patterns only.
	Secrets client.Reader
	// LLMKeysSecret is the Secret whose values are redacted; empty means llm-keys.
	LLMKeysSecret string
	// HarborUsername and HarborPassword are the registry credentials the
	// controller holds; they are redacted at render time too.
	HarborUsername, HarborPassword string
	// Namespace is where the SkillEvals live.
	Namespace string
	// Project is the Harbor project that holds the skills.
	Project string
	// Harbor reads the channel tags.
	Harbor harbor.Client
	// Reports reads the stored report files and the bundle index.
	Reports ReportStore
	// Bundles reads trial bundles and report annotations; nil means no trial
	// sessions (the API reports trials as unknown).
	Bundles BundleStore
	// Version is the controller release, shown by GET /api/v1/site.
	Version string
	// ModelsConfigMap names the model list ConfigMap, read by GET /api/v1/models.
	ModelsConfigMap string
	// Thresholds are the controller policy bands, shown and used for decisions.
	Thresholds policy.Thresholds
	// Platform supplies the network policy in force; nil shows the built-in defaults.
	Platform *platform.Source
	// SessionCacheBytes bounds the cache of rendered sessions; zero means DefaultSessionCacheBytes.
	SessionCacheBytes int64
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Log receives server and request errors.
	Log logr.Logger
}

// Server serves the site. It is an http.Handler and a manager Runnable.
type Server struct {
	cfg      Config
	channels *channelSource
	mux      *http.ServeMux
	ui       *spa
	secrets  *redactorSource
	bundles  *bundleMemo
	rendered *byteCache
}

// New reads the embedded UI and returns the server.
func New(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.LLMKeysSecret == "" {
		cfg.LLMKeysSecret = DefaultLLMKeysSecret
	}
	if cfg.SessionCacheBytes <= 0 {
		cfg.SessionCacheBytes = DefaultSessionCacheBytes
	}
	ui, err := newSPA()
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:      cfg,
		channels: &channelSource{client: cfg.Harbor, now: cfg.Now},
		mux:      http.NewServeMux(),
		ui:       ui,
		secrets: &redactorSource{
			reader: cfg.Secrets, namespace: cfg.Namespace, secret: cfg.LLMKeysSecret,
			harborUser: cfg.HarborUsername, harborPw: cfg.HarborPassword, now: cfg.Now,
		},
		bundles:  newBundleMemo(),
		rendered: newByteCache(cfg.SessionCacheBytes),
	}
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /runs/{name}/report", s.reportHTML)
	s.mux.HandleFunc("GET /runs/{name}/result.json", s.resultJSON)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	s.apiRoutes()
	s.mux.HandleFunc("GET /assets/{path...}", s.ui.asset)
	// The app's router handles every other path (history routing).
	s.mux.HandleFunc("GET /", s.ui.page)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	s.mux.ServeHTTP(w, r)
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
