package harbor

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
)

// Fake is an in-memory Client for tests. Like Harbor, it keeps a tag unique
// within a repository: tagging a digest moves the tag away from other digests.
type Fake struct {
	mu    sync.Mutex
	repos map[string][]Artifact
	// Err, when set, is returned by every call.
	Err error
}

var _ Client = (*Fake)(nil)

// NewFake returns a Fake holding the given artifacts per skill.
func NewFake(repos map[string][]Artifact) *Fake {
	return &Fake{repos: repos}
}

// ListRepositories implements Client.
func (f *Fake) ListRepositories(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	names := make([]string, 0, len(f.repos))
	for n := range f.repos {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// ListArtifacts implements Client.
func (f *Fake) ListArtifacts(_ context.Context, skill string) ([]Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	out := make([]Artifact, 0, len(f.repos[skill]))
	for _, a := range f.repos[skill] {
		out = append(out, Artifact{Digest: a.Digest, Tags: slices.Clone(a.Tags), Annotations: maps.Clone(a.Annotations)})
	}
	return out, nil
}

// GetArtifact implements Client.
func (f *Fake) GetArtifact(_ context.Context, skill, digest string) (Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return Artifact{}, f.Err
	}
	for _, a := range f.repos[skill] {
		if a.Digest == digest {
			return Artifact{Digest: a.Digest, Tags: slices.Clone(a.Tags), Annotations: maps.Clone(a.Annotations)}, nil
		}
	}
	return Artifact{}, fmt.Errorf("fake harbor: %s@%s: %w", skill, digest, ErrNotFound)
}

// AddTag implements Client.
func (f *Fake) AddTag(_ context.Context, skill, digest, tag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	arts := f.repos[skill]
	target := -1
	for i := range arts {
		if arts[i].Digest == digest {
			target = i
		}
	}
	if target < 0 {
		return fmt.Errorf("fake harbor: %s@%s not found", skill, digest)
	}
	for i := range arts {
		arts[i].Tags = slices.DeleteFunc(arts[i].Tags, func(t string) bool { return t == tag })
	}
	arts[target].Tags = append(arts[target].Tags, tag)
	return nil
}

// RemoveTag implements Client.
func (f *Fake) RemoveTag(_ context.Context, skill, digest, tag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	arts := f.repos[skill]
	for i := range arts {
		if arts[i].Digest == digest {
			arts[i].Tags = slices.DeleteFunc(arts[i].Tags, func(t string) bool { return t == tag })
		}
	}
	return nil
}
