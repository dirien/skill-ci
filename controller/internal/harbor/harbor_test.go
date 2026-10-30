package harbor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestHTTPClient(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.EscapedPath())
		if u, p, ok := r.BasicAuth(); !ok || u != "robot" || p != "pw" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/projects/skills/repositories":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "skills/acme"}, {"name": "skills/other"}})
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"digest":"sha256:aa","tags":[{"name":"v1.0.0"},{"name":"beta"}]},{"digest":"sha256:bb","tags":null}]`))
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusConflict)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "skills", "robot", "pw")
	ctx := context.Background()

	repos, err := c.ListRepositories(ctx)
	if err != nil || !reflect.DeepEqual(repos, []string{"acme", "other"}) {
		t.Fatalf("ListRepositories() = %v, %v", repos, err)
	}
	arts, err := c.ListArtifacts(ctx, "acme")
	want := []Artifact{{Digest: "sha256:aa", Tags: []string{"v1.0.0", "beta"}}, {Digest: "sha256:bb"}}
	if err != nil || !reflect.DeepEqual(arts, want) {
		t.Fatalf("ListArtifacts() = %v, %v", arts, err)
	}
	if err := c.AddTag(ctx, "acme", "sha256:aa", "stable"); err != nil {
		t.Errorf("AddTag() on conflict should succeed, got %v", err)
	}
	if err := c.RemoveTag(ctx, "acme", "sha256:aa", "stable"); err != nil {
		t.Errorf("RemoveTag() on missing tag should succeed, got %v", err)
	}
	if !reflect.DeepEqual(calls[len(calls)-2:], []string{
		"POST /api/v2.0/projects/skills/repositories/acme/artifacts/sha256:aa/tags",
		"DELETE /api/v2.0/projects/skills/repositories/acme/artifacts/sha256:aa/tags/stable",
	}) {
		t.Errorf("unexpected calls: %v", calls)
	}

	bad := New(srv.URL, "skills", "robot", "wrong")
	if _, err := bad.ListRepositories(ctx); err == nil {
		t.Error("ListRepositories() with bad credentials should fail")
	}
}
