package reportstore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	artifactType = "application/vnd.skillci.report.v1+json"
	manifestType = "application/vnd.oci.image.manifest.v1+json"
)

// fakeRegistry serves one report manifest with two files from the registry
// API, behind basic auth, and counts blob requests.
type fakeRegistry struct {
	srv         *httptest.Server
	manifestRaw []byte
	manifestDgs digest.Digest
	blobs       map[digest.Digest][]byte
	blobHits    atomic.Int32
}

func newFakeRegistry(t *testing.T, files map[string]string) *fakeRegistry {
	t.Helper()
	f := &fakeRegistry{blobs: map[digest.Digest][]byte{}}
	m := ocispec.Manifest{MediaType: manifestType, ArtifactType: artifactType,
		SchemaVersion: 2,
		Config:        ocispec.DescriptorEmptyJSON}
	for name, body := range files {
		d := digest.FromString(body)
		f.blobs[d] = []byte(body)
		m.Layers = append(m.Layers, ocispec.Descriptor{
			MediaType: "application/octet-stream", Digest: d, Size: int64(len(body)),
			Annotations: map[string]string{ocispec.AnnotationTitle: name},
		})
	}
	var err error
	if f.manifestRaw, err = json.Marshal(m); err != nil {
		t.Fatal(err)
	}
	f.manifestDgs = digest.FromBytes(f.manifestRaw)

	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRegistry) serve(w http.ResponseWriter, r *http.Request) {
	if user, pass, ok := r.BasicAuth(); !ok || user != "robot" || pass != "secret" {
		w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	const prefix = "/v2/skills/acme/"
	switch {
	case strings.HasPrefix(r.URL.Path, prefix+"manifests/"):
		if strings.TrimPrefix(r.URL.Path, prefix+"manifests/") != f.manifestDgs.String() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", manifestType)
		w.Header().Set("Docker-Content-Digest", f.manifestDgs.String())
		_, _ = w.Write(f.manifestRaw)
	case strings.HasPrefix(r.URL.Path, prefix+"blobs/"):
		f.blobHits.Add(1)
		body, ok := f.blobs[digest.Digest(strings.TrimPrefix(r.URL.Path, prefix+"blobs/"))]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	default:
		http.NotFound(w, r)
	}
}

func TestFile(t *testing.T) {
	reg := newFakeRegistry(t, map[string]string{FileReport: "<html>report</html>", FileResult: `{"ok":true}`})
	store, err := New(Options{RegistryURL: reg.srv.URL, Project: "skills", Username: "robot", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		skill   string
		digest  string
		file    string
		want    string
		wantErr error
	}{
		{name: "report html", skill: "acme", digest: reg.manifestDgs.String(), file: FileReport, want: "<html>report</html>"},
		{name: "result json", skill: "acme", digest: reg.manifestDgs.String(), file: FileResult, want: `{"ok":true}`},
		{name: "file not attached", skill: "acme", digest: reg.manifestDgs.String(), file: FileBenchmark, wantErr: ErrNotFound},
		{name: "unknown manifest", skill: "acme", digest: digest.FromString("other").String(), file: FileReport, wantErr: ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.File(context.Background(), tt.skill, tt.digest, tt.file)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if string(got) != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFileRejectsBadInput(t *testing.T) {
	store, err := New(Options{RegistryURL: "http://registry.invalid", Project: "skills"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.File(context.Background(), "acme", "latest", FileReport); err == nil {
		t.Error("a tag instead of a digest was accepted")
	}
	if _, err := New(Options{RegistryURL: "not a url"}); err == nil {
		t.Error("an invalid registry URL was accepted")
	}
}

func TestFileWrongCredentials(t *testing.T) {
	reg := newFakeRegistry(t, map[string]string{FileResult: "{}"})
	store, err := New(Options{RegistryURL: reg.srv.URL, Project: "skills", Username: "robot", Password: "wrong"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.File(context.Background(), "acme", reg.manifestDgs.String(), FileResult)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want an authorization failure that is not ErrNotFound", err)
	}
}

func TestFileIsCached(t *testing.T) {
	reg := newFakeRegistry(t, map[string]string{FileResult: `{"cached":true}`})
	store, err := New(Options{RegistryURL: reg.srv.URL, Project: "skills", Username: "robot", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := store.File(context.Background(), "acme", reg.manifestDgs.String(), FileResult); err != nil {
			t.Fatal(err)
		}
	}
	if hits := reg.blobHits.Load(); hits != 1 {
		t.Errorf("blob requests = %d, want 1", hits)
	}
}
