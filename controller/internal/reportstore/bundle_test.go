package reportstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
)

const (
	bundleType = "application/vnd.skillci.trials.v1+json"
	trialType  = "application/vnd.skillci.trial.v1+gzip"
)

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// addBundle stores a trial bundle manifest whose layers are the given
// documents, gzipped, and returns its digest.
func addBundle(t *testing.T, reg *fakeRegistry, docs map[string][]byte) digest.Digest {
	t.Helper()
	layers := []fakeLayer{{name: "index.json", mediaType: "application/vnd.skillci.trials.index.v1+json", body: []byte(`{"trials":[]}`)}}
	for name, doc := range docs {
		layers = append(layers, fakeLayer{name: name, mediaType: trialType, body: gz(t, doc)})
	}
	return reg.addManifest(t, bundleType, map[string]string{
		"skillci.dev/trials-schema": "1", "skillci.dev/redaction": "v1",
	}, layers)
}

func newTestStore(t *testing.T, reg *fakeRegistry, opts Options) *OCI {
	t.Helper()
	opts.RegistryURL, opts.Project, opts.Username, opts.Password = reg.srv.URL, "skills", "robot", "secret"
	store, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAnnotations(t *testing.T) {
	reg := newFakeRegistry(t, map[string]string{FileResult: "{}"})
	report := reg.addManifest(t, artifactType, map[string]string{"skillci.dev/trials": "sha256:" + strings.Repeat("a", 64)},
		[]fakeLayer{{name: FileResult, mediaType: "application/json", body: []byte(`{"report":2}`)}})
	withheld := reg.addManifest(t, artifactType, map[string]string{"skillci.dev/trials-withheld": "secret-scan"}, nil)
	bundle := addBundle(t, reg, map[string][]byte{"trial-a.json.gz": []byte("{}")})
	store := newTestStore(t, reg, Options{})

	tests := []struct {
		name    string
		digest  string
		key     string
		want    string
		wantLen int
		wantErr error
	}{
		{name: "report names its bundle", digest: report.String(), key: "skillci.dev/trials", want: "sha256:" + strings.Repeat("a", 64), wantLen: 1},
		{name: "report says why there is no bundle", digest: withheld.String(), key: "skillci.dev/trials-withheld", want: "secret-scan", wantLen: 1},
		{name: "bundle schema and redaction", digest: bundle.String(), key: "skillci.dev/trials-schema", want: "1", wantLen: 2},
		{name: "manifest without annotations", digest: reg.manifestDgs.String(), wantLen: 0},
		{name: "unknown manifest", digest: digest.FromString("gone").String(), wantErr: ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.Annotations(context.Background(), "acme", tt.digest)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if got == nil {
				t.Fatal("annotations are nil, want a map")
			}
			if len(got) != tt.wantLen || got[tt.key] != tt.want {
				t.Errorf("annotations = %v, want %d entries with %s=%q", got, tt.wantLen, tt.key, tt.want)
			}
		})
	}
	if _, err := store.Annotations(context.Background(), "acme", "latest"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("a tag instead of a digest: error = %v, want a validation error", err)
	}
}

func TestAnnotationsUseTheCachedManifest(t *testing.T) {
	reg := newFakeRegistry(t, map[string]string{FileResult: `{"ok":true}`})
	report := reg.addManifest(t, artifactType, map[string]string{"skillci.dev/trials-withheld": "disabled"},
		[]fakeLayer{{name: FileResult, mediaType: "application/json", body: []byte(`{}`)}})
	store := newTestStore(t, reg, Options{})
	if _, err := store.File(context.Background(), "acme", report.String(), FileResult); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		got, err := store.Annotations(context.Background(), "acme", report.String())
		if err != nil {
			t.Fatal(err)
		}
		if got["skillci.dev/trials-withheld"] != "disabled" {
			t.Fatalf("annotations = %v", got)
		}
	}
	if hits := reg.manifestHits.Load(); hits != 1 {
		t.Errorf("manifest requests = %d, want 1", hits)
	}
}

func TestGunzip(t *testing.T) {
	doc := []byte(`{"schema":1,"id":"trial-1","steps":[1,2,3]}`)
	reg := newFakeRegistry(t, map[string]string{FileResult: "{}"})
	bundle := addBundle(t, reg, map[string][]byte{"trial-1.json.gz": doc, "trial-empty.json.gz": {}})
	notGzip := reg.addManifest(t, bundleType, nil, []fakeLayer{{name: "trial-2.json.gz", mediaType: trialType, body: []byte("plain text")}})
	store := newTestStore(t, reg, Options{})
	n := int64(len(doc))

	tests := []struct {
		name     string
		manifest digest.Digest
		file     string
		limit    int64
		want     string
		wantErr  error
		anyErr   bool
	}{
		{name: "round trip", manifest: bundle, file: "trial-1.json.gz", limit: 1 << 20, want: string(doc)},
		{name: "exactly at the limit", manifest: bundle, file: "trial-1.json.gz", limit: n, want: string(doc)},
		{name: "one byte over the limit", manifest: bundle, file: "trial-1.json.gz", limit: n - 1, wantErr: ErrTooLarge},
		{name: "empty document", manifest: bundle, file: "trial-empty.json.gz", limit: 1, want: ""},
		{name: "layer not in the bundle", manifest: bundle, file: "trial-9.json.gz", limit: 1 << 20, wantErr: ErrNotFound},
		{name: "unknown bundle", manifest: digest.FromString("gone"), file: "trial-1.json.gz", limit: 1 << 20, wantErr: ErrNotFound},
		{name: "layer is not gzip", manifest: notGzip, file: "trial-2.json.gz", limit: 1 << 20, anyErr: true},
		{name: "limit not positive", manifest: bundle, file: "trial-1.json.gz", limit: 0, anyErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.Gunzip(context.Background(), "acme", tt.manifest.String(), tt.file, tt.limit)
			switch {
			case tt.anyErr:
				if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrTooLarge) {
					t.Fatalf("error = %v, want an error that is neither ErrNotFound nor ErrTooLarge", err)
				}
				return
			case !errors.Is(err, tt.wantErr):
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if string(got) != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGunzipStopsAGzipBombAtTheLimit(t *testing.T) {
	const limit = 8 << 20
	reg := newFakeRegistry(t, map[string]string{FileResult: "{}"})
	// 256 MiB of zeros, a few hundred KiB gzipped; the input is garbage
	// before the measurement starts.
	bundle := addBundle(t, reg, map[string][]byte{"trial-bomb.json.gz": make([]byte, 256<<20)})
	store := newTestStore(t, reg, Options{})

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := store.Gunzip(context.Background(), "acme", bundle.String(), "trial-bomb.json.gz", limit)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
		t.Errorf("allocated %d MiB for an 8 MiB limit; the bomb was decompressed past it", alloc>>20)
	}
	if _, ok := store.trials.get(blobOf(t, reg, bundle, "trial-bomb.json.gz").String()); ok {
		t.Error("a document over the limit was cached")
	}
}

// blobOf returns the digest of the layer with the given title.
func blobOf(t *testing.T, reg *fakeRegistry, manifest digest.Digest, name string) digest.Digest {
	t.Helper()
	store := newTestStore(t, reg, Options{})
	_, m, err := store.open(context.Background(), "acme", manifest.String())
	if err != nil {
		t.Fatal(err)
	}
	layer, err := findLayer(m, manifest.String(), name)
	if err != nil {
		t.Fatal(err)
	}
	return layer.Digest
}

func TestGunzipCachedDocumentStillHonorsTheLimit(t *testing.T) {
	doc := []byte(strings.Repeat("x", 100))
	reg := newFakeRegistry(t, map[string]string{FileResult: "{}"})
	bundle := addBundle(t, reg, map[string][]byte{"trial-1.json.gz": doc})
	store := newTestStore(t, reg, Options{})
	if _, err := store.Gunzip(context.Background(), "acme", bundle.String(), "trial-1.json.gz", 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Gunzip(context.Background(), "acme", bundle.String(), "trial-1.json.gz", 10); !errors.Is(err, ErrTooLarge) {
		t.Errorf("cached document with a smaller limit: error = %v, want ErrTooLarge", err)
	}
	if hits := reg.blobHits.Load(); hits != 1 {
		t.Errorf("blob requests = %d, want 1", hits)
	}
}

func TestGunzipConcurrentCallsShareOneFetch(t *testing.T) {
	doc := []byte(`{"shared":true}`)
	reg := newFakeRegistry(t, map[string]string{FileResult: "{}"})
	bundle := addBundle(t, reg, map[string][]byte{"trial-1.json.gz": doc})
	reg.hold, reg.started = make(chan struct{}), make(chan struct{}, 16)
	store := newTestStore(t, reg, Options{})

	const callers = 8
	var wg sync.WaitGroup
	results := make([][]byte, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Go(func() {
			results[i], errs[i] = store.Gunzip(context.Background(), "acme", bundle.String(), "trial-1.json.gz", 1<<20)
		})
	}
	<-reg.started                     // the first fetch is in flight
	time.Sleep(50 * time.Millisecond) // the other callers join it
	close(reg.hold)
	wg.Wait()
	for i := range callers {
		if errs[i] != nil || string(results[i]) != string(doc) {
			t.Errorf("caller %d: content %q, error %v", i, results[i], errs[i])
		}
	}
	if hits := reg.blobHits.Load(); hits != 1 {
		t.Errorf("blob requests = %d, want 1", hits)
	}
	if hits := reg.manifestHits.Load(); hits != 1 {
		t.Errorf("manifest requests = %d, want 1", hits)
	}
}

func TestGunzipCanceledCallerDoesNotFailTheOthers(t *testing.T) {
	doc := []byte(`{"ok":true}`)
	reg := newFakeRegistry(t, map[string]string{FileResult: "{}"})
	bundle := addBundle(t, reg, map[string][]byte{"trial-1.json.gz": doc})
	store := newTestStore(t, reg, Options{})
	if _, err := store.Annotations(context.Background(), "acme", bundle.String()); err != nil {
		t.Fatal(err) // cache the manifest, so both callers meet at the blob
	}
	reg.hold, reg.started = make(chan struct{}), make(chan struct{}, 16)

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := store.Gunzip(ctx, "acme", bundle.String(), "trial-1.json.gz", 1<<20)
		first <- err
	}()
	<-reg.started
	second := make(chan []byte, 1)
	go func() {
		data, err := store.Gunzip(context.Background(), "acme", bundle.String(), "trial-1.json.gz", 1<<20)
		if err != nil {
			t.Errorf("second caller: %v", err)
		}
		second <- data
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Errorf("canceled caller: error = %v, want context.Canceled", err)
	}
	close(reg.hold)
	if got := <-second; string(got) != string(doc) {
		t.Errorf("second caller: content = %q, want %q", got, doc)
	}
	if hits := reg.blobHits.Load(); hits != 1 {
		t.Errorf("blob requests = %d, want 1", hits)
	}
}

func TestTrialCacheDoesNotEvictReports(t *testing.T) {
	reg := newFakeRegistry(t, map[string]string{FileReport: "<html>report</html>", FileResult: `{"ok":true}`})
	docs := map[string][]byte{}
	for _, id := range []string{"a", "b", "c", "d"} {
		docs["trial-"+id+".json.gz"] = []byte(strings.Repeat(id, 10<<10)) // 10 KiB each
	}
	bundle := addBundle(t, reg, docs)
	// The trial cache holds one document; the report cache holds both
	// manifests and both report files.
	store := newTestStore(t, reg, Options{CacheBytes: 16 << 10, TrialCacheBytes: 15 << 10})
	ctx := context.Background()

	for _, f := range []string{FileReport, FileResult} {
		if _, err := store.File(ctx, "acme", reg.manifestDgs.String(), f); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		got, err := store.Gunzip(ctx, "acme", bundle.String(), "trial-"+id+".json.gz", 1<<20)
		if err != nil || len(got) != 10<<10 {
			t.Fatalf("trial %s: %d bytes, error %v", id, len(got), err)
		}
	}
	blobs, manifests := reg.blobHits.Load(), reg.manifestHits.Load()
	if blobs != 6 || manifests != 2 {
		t.Fatalf("blob and manifest requests = %d and %d, want 6 and 2", blobs, manifests)
	}

	for _, f := range []string{FileReport, FileResult} {
		if _, err := store.File(ctx, "acme", reg.manifestDgs.String(), f); err != nil {
			t.Fatal(err)
		}
	}
	if hits := reg.blobHits.Load(); hits != blobs {
		t.Errorf("report files were fetched again (%d blob requests, want %d): trials evicted reports", hits, blobs)
	}
	if _, err := store.Gunzip(ctx, "acme", bundle.String(), "trial-d.json.gz", 1<<20); err != nil {
		t.Fatal(err)
	}
	if hits := reg.blobHits.Load(); hits != blobs {
		t.Errorf("the newest trial document was fetched again (%d blob requests, want %d)", hits, blobs)
	}
	if _, err := store.Gunzip(ctx, "acme", bundle.String(), "trial-a.json.gz", 1<<20); err != nil {
		t.Fatal(err)
	}
	if hits := reg.blobHits.Load(); hits != blobs+1 {
		t.Errorf("blob requests = %d, want %d: the trial cache kept more than its budget", hits, blobs+1)
	}
	if hits := reg.manifestHits.Load(); hits != manifests {
		t.Errorf("manifest requests = %d, want %d", hits, manifests)
	}
}
