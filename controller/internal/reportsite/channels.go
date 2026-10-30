package reportsite

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/semver"
)

const (
	channelTTL     = 5 * time.Second
	channelTimeout = 8 * time.Second
)

// Channels is a snapshot of the tags in the registry: which digest of each
// skill carries stable, beta and its version tag.
type Channels struct {
	// Err is why the snapshot is empty; nil when Harbor answered.
	Err error

	skills map[string][]harbor.Artifact
}

// Known reports whether the snapshot reflects the registry.
func (c *Channels) Known() bool { return c != nil && c.Err == nil }

// Skills returns the skills that have a repository in the registry.
func (c *Channels) Skills() []string {
	if !c.Known() {
		return nil
	}
	names := make([]string, 0, len(c.skills))
	for n := range c.skills {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Tags returns the channel tags (everything but the version tag) of a digest.
func (c *Channels) Tags(skill, digest string) []string {
	if !c.Known() {
		return nil
	}
	var out []string
	for _, a := range c.skills[skill] {
		if a.Digest == digest {
			for _, t := range a.Tags {
				if !semver.IsRelease(t) {
					out = append(out, t)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// Holder returns the digest and version tag of the artifact that carries tag
// in skill; both are empty when no artifact does.
func (c *Channels) Holder(skill, tag string) (digest, version string) {
	if !c.Known() {
		return "", ""
	}
	for _, a := range c.skills[skill] {
		if a.HasTag(tag) {
			return a.Digest, semver.Newest(a.Tags)
		}
	}
	return "", ""
}

// OnStable counts the skills that have a stable version.
func (c *Channels) OnStable() int {
	if !c.Known() {
		return 0
	}
	n := 0
	for skill := range c.skills {
		if d, _ := c.Holder(skill, policy.TagStable); d != "" {
			n++
		}
	}
	return n
}

// channelSource reads Channels from Harbor and reuses the answer for a few
// seconds, so a page refresh storm does not become an API storm.
type channelSource struct {
	client harbor.Client
	now    func() time.Time

	mu   sync.Mutex
	snap *Channels
	at   time.Time
}

func (s *channelSource) get(ctx context.Context) *Channels {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap != nil && s.now().Sub(s.at) < channelTTL {
		return s.snap
	}
	ctx, cancel := context.WithTimeout(ctx, channelTimeout)
	defer cancel()
	s.snap, s.at = s.load(ctx), s.now()
	return s.snap
}

func (s *channelSource) load(ctx context.Context) *Channels {
	names, err := s.client.ListRepositories(ctx)
	if err != nil {
		return &Channels{Err: fmt.Errorf("listing skills in Harbor: %w", err)}
	}
	skills := make(map[string][]harbor.Artifact, len(names))
	for _, n := range names {
		arts, err := s.client.ListArtifacts(ctx, n)
		if err != nil {
			return &Channels{Err: fmt.Errorf("listing artifacts of %s in Harbor: %w", n, err)}
		}
		skills[n] = arts
	}
	return &Channels{skills: skills}
}
