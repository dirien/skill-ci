package webhook

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/evals"
	"github.com/dirien/skill-ci/controller/internal/models"
)

const maxBody = 1 << 20

// Handler serves POST /webhook/harbor.
type Handler struct {
	// Client reads the model list and creates SkillEvals.
	Client client.Client
	// Namespace is where SkillEvals and the model list live.
	Namespace string
	// Project is the only Harbor project that triggers evals.
	Project string
	// ModelsConfigMap names the ConfigMap that holds the model list.
	ModelsConfigMap string
	// Token is the expected "Bearer <token>" secret; empty accepts every request.
	Token string
	// Attempts is the trials per arm for new SkillEvals.
	Attempts int
	// DefaultSandboxProfile is the SandboxProfile of an entry that names none.
	DefaultSandboxProfile string
	// Log receives one line per request.
	Log logr.Logger
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var p Payload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&p); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	created, err := h.handle(r.Context(), p)
	if err != nil {
		h.Log.Error(err, "handling harbor webhook", "type", p.Type)
		http.Error(w, "internal error", http.StatusInternalServerError) // Harbor retries on non-2xx
		return
	}
	h.Log.Info("harbor webhook handled", "type", p.Type, "repository", p.EventData.Repository.RepoFullName, "created", created)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"created": created})
}

func (h *Handler) handle(ctx context.Context, p Payload) (int, error) {
	pushes := Decide(p, h.Project)
	if len(pushes) == 0 {
		return 0, nil
	}
	var cm corev1.ConfigMap
	if err := h.Client.Get(ctx, types.NamespacedName{Namespace: h.Namespace, Name: h.ModelsConfigMap}, &cm); err != nil {
		return 0, fmt.Errorf("reading model list: %w", err)
	}
	list, err := models.FromConfigMap(&cm)
	if err != nil {
		return 0, fmt.Errorf("parsing model list: %w", err)
	}
	created := 0
	for _, push := range pushes {
		for _, m := range list {
			ok, err := evals.Ensure(ctx, h.Client, h.Namespace, evals.Request{
				Skill: push.Skill, Version: push.Version, Digest: push.Digest,
				Model: m, Attempts: h.Attempts, Trigger: v1alpha1.TriggerPush,
				DefaultSandboxProfile: h.DefaultSandboxProfile,
			})
			if err != nil {
				return created, fmt.Errorf("ensuring skilleval: %w", err)
			}
			if ok {
				created++
			}
		}
	}
	return created, nil
}

func (h *Handler) authorized(r *http.Request) bool {
	if h.Token == "" {
		return true
	}
	want := []byte("Bearer " + h.Token)
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) == 1
}

// Server serves the webhook and the health endpoints. It is a manager Runnable.
type Server struct {
	addr    string
	handler http.Handler
	log     logr.Logger
}

// NewServer returns a Server that listens on addr.
func NewServer(addr string, h *Handler) *Server {
	if h.Token == "" {
		h.Log.Info("WEBHOOK_TOKEN is empty: accepting unauthenticated webhook requests")
	}
	mux := http.NewServeMux()
	mux.Handle("POST /webhook/harbor", h)
	ok := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", ok)
	return &Server{addr: addr, handler: mux, log: h.Log}
}

// Start serves until ctx is done, then shuts down gracefully.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{Addr: s.addr, Handler: s.handler, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	s.log.Info("webhook server listening", "addr", s.addr)
	select {
	case err := <-errCh:
		return fmt.Errorf("serving webhook: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("shutting down webhook server: %w", err)
		}
		return nil
	}
}

// NeedLeaderElection reports false: every replica should receive webhooks.
func (s *Server) NeedLeaderElection() bool { return false }
