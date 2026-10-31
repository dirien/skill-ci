package harbor

import (
	"context"
	"encoding/json"
	"errors"
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

func TestGetArtifact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v2.0/projects/skills/repositories/acme/artifacts/sha256:aa":
			_, _ = w.Write([]byte(`{"digest":"sha256:aa","tags":[{"name":"v1.0.0"}],` +
				`"annotations":{"skillci.dev/network":"allowlist","skillci.dev/network-allow":"@python"}}`))
		case "/api/v2.0/projects/skills/repositories/acme/artifacts/sha256:bb":
			_, _ = w.Write([]byte(`{"digest":"sha256:bb","annotations":null}`))
		case "/api/v2.0/projects/skills/repositories/acme/artifacts/sha256:cc":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "skills", "robot", "pw")
	tests := []struct {
		digest   string
		want     Artifact
		notFound bool
		wantErr  bool
	}{
		{digest: "sha256:aa", want: Artifact{Digest: "sha256:aa", Tags: []string{"v1.0.0"}, Annotations: map[string]string{
			"skillci.dev/network": "allowlist", "skillci.dev/network-allow": "@python",
		}}},
		{digest: "sha256:bb", want: Artifact{Digest: "sha256:bb"}},
		{digest: "sha256:cc", wantErr: true},
		{digest: "sha256:dd", wantErr: true, notFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.digest, func(t *testing.T) {
			got, err := c.GetArtifact(context.Background(), "acme", tt.digest)
			if tt.wantErr {
				if err == nil || errors.Is(err, ErrNotFound) != tt.notFound {
					t.Fatalf("got %v, want error (not found %t)", err, tt.notFound)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestFakeGetArtifact(t *testing.T) {
	f := NewFake(map[string][]Artifact{"acme": {{Digest: "sha256:aa", Annotations: map[string]string{"k": "v"}}}})
	got, err := f.GetArtifact(context.Background(), "acme", "sha256:aa")
	if err != nil || got.Annotations["k"] != "v" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := f.GetArtifact(context.Background(), "acme", "sha256:zz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
