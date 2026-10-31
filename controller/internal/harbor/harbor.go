// Package harbor is a small client for the parts of the Harbor v2.0 REST API
// that skill-ci needs: listing skills and managing their tags.
package harbor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const pageSize = 100

// Artifact is a skill version in the registry, identified by its digest.
type Artifact struct {
	// Digest is the manifest digest.
	Digest string
	// Tags are the tag names pointing at the digest.
	Tags []string
	// Annotations are the OCI manifest annotations, for example
	// skillci.dev/network; nil when the manifest has none.
	Annotations map[string]string
}

// ErrNotFound is returned by GetArtifact for a digest the registry does not have.
var ErrNotFound = errors.New("artifact not found")

// HasTag reports whether the artifact carries tag.
func (a Artifact) HasTag(tag string) bool {
	return slices.Contains(a.Tags, tag)
}

// Tagged reports whether the artifact with the given digest carries tag.
func Tagged(arts []Artifact, digest, tag string) bool {
	for _, a := range arts {
		if a.Digest == digest {
			return a.HasTag(tag)
		}
	}
	return false
}

// Client is what the controller needs from a registry.
type Client interface {
	// ListRepositories returns the skill names in the project.
	ListRepositories(ctx context.Context) ([]string, error)
	// ListArtifacts returns the artifacts of one skill with their tags.
	ListArtifacts(ctx context.Context, skill string) ([]Artifact, error)
	// GetArtifact returns one artifact of a skill, by digest, with its tags
	// and annotations. A missing artifact is an error wrapping ErrNotFound.
	GetArtifact(ctx context.Context, skill, digest string) (Artifact, error)
	// AddTag tags the artifact with the given digest. Adding an existing tag succeeds.
	AddTag(ctx context.Context, skill, digest, tag string) error
	// RemoveTag removes a tag from the artifact. Removing a missing tag succeeds.
	RemoveTag(ctx context.Context, skill, digest, tag string) error
}

// HTTPClient talks to Harbor over HTTP with basic auth.
type HTTPClient struct {
	base     string
	project  string
	username string
	password string
	http     *http.Client
}

var _ Client = (*HTTPClient)(nil)

// New returns a client for one Harbor project.
func New(baseURL, project, username, password string) *HTTPClient {
	return &HTTPClient{
		base:     strings.TrimRight(baseURL, "/") + "/api/v2.0",
		project:  project,
		username: username,
		password: password,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// ListRepositories implements Client.
func (c *HTTPClient) ListRepositories(ctx context.Context) ([]string, error) {
	var names []string
	err := c.paginate(ctx, "/projects/"+url.PathEscape(c.project)+"/repositories", func(raw json.RawMessage) (int, error) {
		var page []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return 0, fmt.Errorf("decoding repositories: %w", err)
		}
		for _, r := range page {
			names = append(names, strings.TrimPrefix(r.Name, c.project+"/"))
		}
		return len(page), nil
	})
	return names, err
}

// ListArtifacts implements Client.
func (c *HTTPClient) ListArtifacts(ctx context.Context, skill string) ([]Artifact, error) {
	var arts []Artifact
	err := c.paginate(ctx, c.artifactsPath(skill)+"?with_tag=true", func(raw json.RawMessage) (int, error) {
		var page []apiArtifact
		if err := json.Unmarshal(raw, &page); err != nil {
			return 0, fmt.Errorf("decoding artifacts: %w", err)
		}
		for _, a := range page {
			arts = append(arts, a.artifact())
		}
		return len(page), nil
	})
	return arts, err
}

// apiArtifact is an artifact as the Harbor API returns it.
type apiArtifact struct {
	Digest string `json:"digest"`
	Tags   []struct {
		Name string `json:"name"`
	} `json:"tags"`
	Annotations map[string]string `json:"annotations"`
}

func (a apiArtifact) artifact() Artifact {
	art := Artifact{Digest: a.Digest, Annotations: a.Annotations}
	for _, t := range a.Tags {
		art.Tags = append(art.Tags, t.Name)
	}
	return art
}

// GetArtifact implements Client.
func (c *HTTPClient) GetArtifact(ctx context.Context, skill, digest string) (Artifact, error) {
	status, body, err := c.do(ctx, http.MethodGet, c.artifactsPath(skill)+"/"+digest+"?with_tag=true", nil)
	if err != nil {
		return Artifact{}, err
	}
	if status == http.StatusNotFound {
		return Artifact{}, fmt.Errorf("%s@%s: %w", skill, digest, ErrNotFound)
	}
	if err := statusError(status, "reading "+skill+"@"+digest); err != nil {
		return Artifact{}, err
	}
	var a apiArtifact
	if err := json.Unmarshal(body, &a); err != nil {
		return Artifact{}, fmt.Errorf("decoding artifact %s@%s: %w", skill, digest, err)
	}
	return a.artifact(), nil
}

// AddTag implements Client.
func (c *HTTPClient) AddTag(ctx context.Context, skill, digest, tag string) error {
	body, err := json.Marshal(map[string]string{"name": tag})
	if err != nil {
		return fmt.Errorf("encoding tag: %w", err)
	}
	status, _, err := c.do(ctx, http.MethodPost, c.artifactsPath(skill)+"/"+digest+"/tags", body)
	if err != nil {
		return err
	}
	if status == http.StatusConflict { // already tagged
		return nil
	}
	return statusError(status, "tagging "+skill+"@"+digest+" with "+tag)
}

// RemoveTag implements Client.
func (c *HTTPClient) RemoveTag(ctx context.Context, skill, digest, tag string) error {
	status, _, err := c.do(ctx, http.MethodDelete, c.artifactsPath(skill)+"/"+digest+"/tags/"+url.PathEscape(tag), nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound { // already gone
		return nil
	}
	return statusError(status, "untagging "+skill+"@"+digest+" "+tag)
}

func (c *HTTPClient) artifactsPath(skill string) string {
	// Harbor wants a repository name with slashes double-encoded.
	repo := url.PathEscape(url.PathEscape(skill))
	return "/projects/" + url.PathEscape(c.project) + "/repositories/" + repo + "/artifacts"
}

// paginate calls fn with each page body until a page is not full.
func (c *HTTPClient) paginate(ctx context.Context, path string, fn func(json.RawMessage) (int, error)) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; ; page++ {
		status, body, err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s%spage=%d&page_size=%d", path, sep, page, pageSize), nil)
		if err != nil {
			return err
		}
		if err := statusError(status, "listing "+path); err != nil {
			return err
		}
		n, err := fn(body)
		if err != nil {
			return err
		}
		if n < pageSize {
			return nil
		}
	}
}

func (c *HTTPClient) do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("building %s %s: %w", method, path, err)
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("calling harbor %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("reading harbor response: %w", err)
	}
	return resp.StatusCode, data, nil
}

func statusError(status int, what string) error {
	if status >= 200 && status < 300 {
		return nil
	}
	return errors.New(what + ": harbor returned HTTP " + http.StatusText(status) + " (" + fmt.Sprint(status) + ")")
}
