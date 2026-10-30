// Package reportstore reads SkillEvaluator reports from the registry. The
// runner attaches report.html and result.json to the evaluated skill digest as
// an OCI referrer; this package fetches the referrer manifest and its files and
// keeps them in a bounded in-memory cache.
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
	// DefaultCacheBytes bounds the in-memory cache when Options.CacheBytes is zero.
	DefaultCacheBytes = 32 << 20
	maxManifestBytes  = 1 << 20
	maxFileBytes      = 16 << 20
	requestTimeout    = 30 * time.Second
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
	// CacheBytes bounds the in-memory cache; zero means DefaultCacheBytes.
	CacheBytes int64
}

// OCI reads reports from an OCI registry with oras-go.
type OCI struct {
	host      string
	project   string
	plainHTTP bool
	client    *auth.Client
	cache     *cache
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
	limit := opts.CacheBytes
	if limit <= 0 {
		limit = DefaultCacheBytes
	}
	return &OCI{
		host: u.Host, project: opts.Project, plainHTTP: u.Scheme == "http",
		client: client, cache: newCache(limit),
	}, nil
}

// File returns one file of the report whose referrer manifest has the given
// digest, for example FileResult. The result is shared with the cache and must
// not be modified. It returns ErrNotFound when the manifest or the file is missing.
func (s *OCI) File(ctx context.Context, skill, reportDigest, name string) ([]byte, error) {
	if err := digest.Digest(reportDigest).Validate(); err != nil {
		return nil, fmt.Errorf("report digest %q: %w", reportDigest, err)
	}
	repo, err := remote.NewRepository(s.host + "/" + s.project + "/" + skill)
	if err != nil {
		return nil, fmt.Errorf("opening repository %s: %w", skill, err)
	}
	repo.PlainHTTP = s.plainHTTP
	repo.Client = s.client

	manifest, err := s.manifest(ctx, repo, reportDigest)
	if err != nil {
		return nil, err
	}
	for _, layer := range manifest.Layers {
		if layer.Annotations[ocispec.AnnotationTitle] == name {
			return s.blob(ctx, repo, layer)
		}
	}
	return nil, fmt.Errorf("file %s in report %s: %w", name, reportDigest, ErrNotFound)
}

func (s *OCI) manifest(ctx context.Context, repo *remote.Repository, reportDigest string) (ocispec.Manifest, error) {
	var m ocispec.Manifest
	raw, ok := s.cache.get(reportDigest)
	if !ok {
		desc, rc, err := repo.FetchReference(ctx, reportDigest)
		if err != nil {
			return m, notFoundOr(err, "fetching report manifest "+reportDigest)
		}
		defer func() { _ = rc.Close() }()
		if desc.Size > maxManifestBytes {
			return m, fmt.Errorf("report manifest %s is %d bytes, over the %d byte limit", reportDigest, desc.Size, maxManifestBytes)
		}
		if raw, err = content.ReadAll(rc, desc); err != nil {
			return m, fmt.Errorf("reading report manifest %s: %w", reportDigest, err)
		}
		s.cache.put(reportDigest, raw)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("decoding report manifest %s: %w", reportDigest, err)
	}
	return m, nil
}

func (s *OCI) blob(ctx context.Context, repo *remote.Repository, layer ocispec.Descriptor) ([]byte, error) {
	key := layer.Digest.String()
	if data, ok := s.cache.get(key); ok {
		return data, nil
	}
	if layer.Size > maxFileBytes {
		return nil, fmt.Errorf("report file %s is %d bytes, over the %d byte limit", key, layer.Size, maxFileBytes)
	}
	rc, err := repo.Blobs().Fetch(ctx, layer)
	if err != nil {
		return nil, notFoundOr(err, "fetching report file "+key)
	}
	defer func() { _ = rc.Close() }()
	data, err := content.ReadAll(rc, layer) // checks size and digest
	if err != nil {
		return nil, fmt.Errorf("reading report file %s: %w", key, err)
	}
	s.cache.put(key, data)
	return data, nil
}

// notFoundOr maps a registry 404 to ErrNotFound and wraps anything else.
func notFoundOr(err error, what string) error {
	if errors.Is(err, errdef.ErrNotFound) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return fmt.Errorf("%s: %w", what, err)
}
