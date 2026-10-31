package reportsite

import (
	"context"
	"net/http"

	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
)

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

// apiRoutes registers the JSON API under /api/v1/. Every route is a stub that
// answers 501 until the handlers are built; unknown API paths answer a JSON 404.
func (s *Server) apiRoutes() {
	for _, pattern := range []string{
		"GET " + apiv1.Prefix + "/site",
		"GET " + apiv1.Prefix + "/overview",
		"GET " + apiv1.Prefix + "/runs",
		"GET " + apiv1.Prefix + "/runs/{name}",
		"GET " + apiv1.Prefix + "/runs/{name}/trials/{trial}",
		"GET " + apiv1.Prefix + "/runs/{name}/trials/{trial}/steps/{step}/output/{k}",
		"GET " + apiv1.Prefix + "/runs/{name}/trials/{trial}/trajectory.json",
		"GET " + apiv1.Prefix + "/runs/{name}/cases/{case}/compare",
		"GET " + apiv1.Prefix + "/skills",
		"GET " + apiv1.Prefix + "/skills/{skill}",
		"GET " + apiv1.Prefix + "/models",
	} {
		s.mux.HandleFunc(pattern, s.apiNotImplemented)
	}
	// Unknown API paths get a JSON 404, never the single page app. The API is
	// read-only: the mux answers other methods with 405 because every pattern is GET.
	s.mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		apiv1.WriteError(w, http.StatusNotFound, apiv1.CodeNotFound, "There is no API route at "+r.URL.Path+".")
	})
}

// apiNotImplemented answers every API route until its handler is built.
func (s *Server) apiNotImplemented(w http.ResponseWriter, r *http.Request) {
	apiv1.WriteError(w, http.StatusNotImplemented, apiv1.CodeNotImplemented, r.Pattern+" is not implemented yet.")
}
