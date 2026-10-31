// Package webhook receives Harbor webhooks and turns skill pushes into SkillEvals.
package webhook

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/semver"
)

// eventPushArtifact is the Harbor event type for a pushed or tagged artifact.
const eventPushArtifact = "PUSH_ARTIFACT"

// Payload is the part of Harbor's "Default" webhook payload we use.
type Payload struct {
	Type string `json:"type"`
	// Operator is the Harbor user who triggered the event, for a push the one who pushed.
	Operator  string `json:"operator"`
	EventData struct {
		Resources  []Resource `json:"resources"`
		Repository struct {
			Name         string `json:"name"`
			Namespace    string `json:"namespace"`
			RepoFullName string `json:"repo_full_name"`
		} `json:"repository"`
	} `json:"event_data"`
}

// Resource is one artifact reference in a payload.
type Resource struct {
	Digest string `json:"digest"`
	Tag    string `json:"tag"`
}

// Push is a new skill version worth evaluating.
type Push struct {
	// Skill is the repository name without the project.
	Skill string
	// Version is the vX.Y.Z tag.
	Version string
	// Digest is the manifest digest.
	Digest string
}

// Decide returns the pushes to evaluate. It ignores other event types, other
// projects, and every tag that is not a vX.Y.Z release, so retagging a digest
// as beta or stable never starts an eval.
func Decide(p Payload, project string) []Push {
	if p.Type != eventPushArtifact {
		return nil
	}
	repo := p.EventData.Repository
	proj, skill, ok := strings.Cut(repo.RepoFullName, "/")
	if !ok { // fall back to the separate fields
		proj, skill = repo.Namespace, repo.Name
	}
	if proj != project || skill == "" {
		return nil
	}
	var pushes []Push
	for _, r := range p.EventData.Resources {
		if semver.IsRelease(r.Tag) && r.Digest != "" {
			pushes = append(pushes, Push{Skill: skill, Version: r.Tag, Digest: r.Digest})
		}
	}
	return pushes
}

// PushedBy returns the payload's operator as the value of the SkillEval
// annotation skillci.dev/pushed-by: trimmed, at most
// v1alpha1.MaxPushedByLength characters of printable UTF-8. Anything else,
// and an empty operator, gives "" (no annotation).
func PushedBy(p Payload) string {
	op := strings.TrimSpace(p.Operator)
	if op == "" || len(op) > v1alpha1.MaxPushedByLength || !utf8.ValidString(op) {
		return ""
	}
	for _, r := range op {
		if !unicode.IsPrint(r) {
			return ""
		}
	}
	return op
}
