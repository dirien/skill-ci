// Package reportstore reads SkillEvaluator reports and trial bundles from the
// registry. The runner attaches report.html and result.json, and a bundle of
// gzip trial documents, to the evaluated skill digest as OCI referrers; this
// package fetches the referrer manifests and their files, shares concurrent
// fetches of the same blob, and keeps the results in two bounded in-memory
// caches: one for manifests and report files, one for decompressed trial
// documents, so trial traffic never evicts reports.
package reportstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/singleflight"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// Files the runner attaches to a report.
const (
	// FileReport is the SkillEvaluator HTML report.
	FileReport = "report.html"
	// FileResult is the machine-readable SkillEvaluator result.
	FileResult = "result.json"
	// FileBenchmark is the optional Markdown benchmark summary.
	FileBenchmark = "BENCHMARK.md"
)

const (
	// DefaultCacheBytes bounds the manifest and report file cache when
	// Options.CacheBytes is zero.
	DefaultCacheBytes = 32 << 20
	// DefaultTrialCacheBytes bounds the decompressed trial document cache when
	// Options.TrialCacheBytes is zero.
	DefaultTrialCacheBytes = 64 << 20
	maxManifestBytes       = 1 << 20
	maxFileBytes           = 16 << 20
	requestTimeout         = 30 * time.Second
)

// ErrNotFound means the report, or the file inside it, is not in the registry.
var ErrNotFound = errors.New("report not found")

// Options configure an OCI store.
type Options struct {
	// RegistryURL is the registry base URL, for example http://harbor.harbor.svc.cluster.local.
	// An http:// scheme selects plain HTTP.
	RegistryURL string
	// Project is the registry project that holds the skills.
	Project string
	// Username and Password are static credentials; both empty means anonymous.
	Username, Password string
	// CacheBytes bounds the cache of manifests and report files; zero means
	// DefaultCacheBytes.
	CacheBytes int64
	// TrialCacheBytes bounds the cache of decompressed trial documents (Gunzip);
	// zero means DefaultTrialCacheBytes. It is separate from CacheBytes, so
	// reading trials never evicts reports.
	TrialCacheBytes int64
}

// OCI reads reports from an OCI registry with oras-go.
type OCI struct {
	host      string
	project   string
	plainHTTP bool
	client    *auth.Client
	cache     *cache // manifests and report files, keyed by digest
	trials    *cache // decompressed gzip layers, keyed by layer digest
	flight    singleflight.Group
}

// New returns a store for one registry project.
func New(opts Options) (*OCI, error) {
	u, err := url.Parse(opts.RegistryURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid registry URL %q", opts.RegistryURL)
	}
	httpClient := *retry.DefaultClient
	httpClient.Timeout = requestTimeout
	client := &auth.Client{Client: &httpClient, Cache: auth.NewCache()}
	if opts.Username != "" {
		client.Credential = auth.StaticCredential(u.Host, auth.Credential{Username: opts.Username, Password: opts.Password})
	}
	return &OCI{
		host: u.Host, project: opts.Project, plainHTTP: u.Scheme == "http",
		client: client,
		cache:  newCache(orDefault(opts.CacheBytes, DefaultCacheBytes)),
		trials: newCache(orDefault(opts.TrialCacheBytes, DefaultTrialCacheBytes)),
	}, nil
}

func orDefault(n, def int64) int64 {
	if n <= 0 {
		return def
	}
	return n
}

// File returns one file of the report whose referrer manifest has the given
// digest, for example FileResult. The result is shared with the cache and must
// not be modified. It returns ErrNotFound when the manifest or the file is missing.
func (s *OCI) File(ctx context.Context, skill, reportDigest, name string) ([]byte, error) {
	repo, manifest, err := s.open(ctx, skill, reportDigest)
	if err != nil {
		return nil, err
	}
	layer, err := findLayer(manifest, reportDigest, name)
	if err != nil {
		return nil, err
	}
	key := layer.Digest.String()
	if data, ok := s.cache.get(key); ok {
		return data, nil
	}
	return s.shared(ctx, "file "+skill+" "+key, func(ctx context.Context) ([]byte, error) {
		data, err := s.fetchBlob(ctx, repo, layer)
		if err != nil {
			return nil, err
		}
		s.cache.put(key, data)
		return data, nil
	})
}

// open validates the manifest digest, opens the skill repository and returns
// the referrer manifest, from the cache when it is there.
func (s *OCI) open(ctx context.Context, skill, manifestDigest string) (*remote.Repository, ocispec.Manifest, error) {
	var m ocispec.Manifest
	if err := digest.Digest(manifestDigest).Validate(); err != nil {
		return nil, m, fmt.Errorf("manifest digest %q: %w", manifestDigest, err)
	}
	repo, err := remote.NewRepository(s.host + "/" + s.project + "/" + skill)
	if err != nil {
		return nil, m, fmt.Errorf("opening repository %s: %w", skill, err)
	}
	repo.PlainHTTP = s.plainHTTP
	repo.Client = s.client

	raw, ok := s.cache.get(manifestDigest)
	if !ok {
		raw, err = s.shared(ctx, "manifest "+skill+" "+manifestDigest, func(ctx context.Context) ([]byte, error) {
			return s.fetchManifest(ctx, repo, manifestDigest)
		})
		if err != nil {
			return nil, m, err
		}
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, m, fmt.Errorf("decoding manifest %s: %w", manifestDigest, err)
	}
	return repo, m, nil
}

// fetchManifest reads a referrer manifest from the registry and caches it.
func (s *OCI) fetchManifest(ctx context.Context, repo *remote.Repository, manifestDigest string) ([]byte, error) {
	desc, rc, err := repo.FetchReference(ctx, manifestDigest)
	if err != nil {
		return nil, notFoundOr(err, "fetching manifest "+manifestDigest)
	}
	defer func() { _ = rc.Close() }()
	if desc.Size > maxManifestBytes {
		return nil, fmt.Errorf("manifest %s is %d bytes, over the %d byte limit", manifestDigest, desc.Size, maxManifestBytes)
	}
	raw, err := content.ReadAll(rc, desc) // checks size and digest
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", manifestDigest, err)
	}
	s.cache.put(manifestDigest, raw)
	return raw, nil
}

// findLayer returns the layer of the manifest whose title is name.
func findLayer(m ocispec.Manifest, manifestDigest, name string) (ocispec.Descriptor, error) {
	for _, layer := range m.Layers {
		if layer.Annotations[ocispec.AnnotationTitle] == name {
			return layer, nil
		}
	}
	return ocispec.Descriptor{}, fmt.Errorf("file %s in manifest %s: %w", name, manifestDigest, ErrNotFound)
}

// fetchBlob reads one layer from the registry, at most maxFileBytes, and
// checks its size and digest. It does not cache.
func (s *OCI) fetchBlob(ctx context.Context, repo *remote.Repository, layer ocispec.Descriptor) ([]byte, error) {
	key := layer.Digest.String()
	if layer.Size > maxFileBytes {
		return nil, fmt.Errorf("file %s is %d bytes, over the %d byte limit: %w", key, layer.Size, maxFileBytes, ErrTooLarge)
	}
	rc, err := repo.Blobs().Fetch(ctx, layer)
	if err != nil {
		return nil, notFoundOr(err, "fetching file "+key)
	}
	defer func() { _ = rc.Close() }()
	data, err := content.ReadAll(rc, layer) // checks size and digest
	if err != nil {
		return nil, fmt.Errorf("reading file %s: %w", key, err)
	}
	return data, nil
}

// shared runs fetch once for all concurrent callers with the same key. The
// fetch runs without the first caller's cancellation, so one caller giving
// up does not fail the others (the HTTP client timeout still bounds it); each
// caller stops waiting when its own context ends. fetch must cache its result
// before returning, so a caller that arrives after the flight finds it there.
func (s *OCI) shared(ctx context.Context, key string, fetch func(context.Context) ([]byte, error)) ([]byte, error) {
	detached := context.WithoutCancel(ctx)
	ch := s.flight.DoChan(key, func() (any, error) {
		data, err := fetch(detached)
		return data, err
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		data, _ := res.Val.([]byte) // fetch returns []byte
		return data, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for %s: %w", key, context.Cause(ctx))
	}
}

// notFoundOr maps a registry 404 to ErrNotFound and wraps anything else.
func notFoundOr(err error, what string) error {
	if errors.Is(err, errdef.ErrNotFound) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return fmt.Errorf("%s: %w", what, err)
}
