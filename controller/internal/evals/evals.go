// Package evals names, labels and creates SkillEval objects. It is the one
// place that knows how a skill digest and a model map to an object.
package evals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/models"
)

const (
	maxName    = 63
	hashLength = 6
	// digestLabelLength is how much of the hex digest goes into the label.
	digestLabelLength = 32
)

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Slug lowercases s and replaces every run of other characters with a dash.
// The result is safe in object names and label values.
func Slug(s string) string {
	s = strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > maxName {
		s = strings.TrimRight(s[:maxName], "-")
	}
	return s
}

// Name returns <skill>-<version with dashes>-<model>. A name longer than 63
// characters is cut short and ends in a 6 character hash of the full name,
// so the result still fits and two long names stay distinct.
func Name(skill, version, model string) string {
	full := Slug(skill) + "-" + Slug(version) + "-" + Slug(model)
	if len(full) <= maxName {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	keep := maxName - hashLength - 1
	return strings.TrimRight(full[:keep], "-") + "-" + hex.EncodeToString(sum[:])[:hashLength]
}

// DigestLabel turns sha256:<hex> into the label value: the first 32 hex characters.
func DigestLabel(digest string) string {
	hexPart := strings.TrimPrefix(digest, "sha256:")
	if len(hexPart) > digestLabelLength {
		hexPart = hexPart[:digestLabelLength]
	}
	return hexPart
}

// Labels returns the labels every SkillEval carries.
func Labels(skill, digest, agent, model string) map[string]string {
	return map[string]string{
		v1alpha1.LabelSkill:  Slug(skill),
		v1alpha1.LabelDigest: DigestLabel(digest),
		v1alpha1.LabelAgent:  Slug(agent),
		v1alpha1.LabelModel:  Slug(model),
	}
}

// Request describes the SkillEval to create for one skill digest and one model.
type Request struct {
	// Skill is the repository name.
	Skill string
	// Version is the semver tag.
	Version string
	// Digest is the manifest digest.
	Digest string
	// Model is the agent/model pair to evaluate with.
	Model models.Model
	// Attempts is the number of trials per arm.
	Attempts int
	// Trigger records why the eval is created.
	Trigger v1alpha1.Trigger
}

// New builds the SkillEval for a request.
func New(namespace string, r Request) *v1alpha1.SkillEval {
	return &v1alpha1.SkillEval{
		Name:      Name(r.Skill, r.Version, r.Model.Model),
		Namespace: namespace,
		Labels:    Labels(r.Skill, r.Digest, r.Model.Agent, r.Model.Model),
		Spec: v1alpha1.SkillEvalSpec{
			Skill:    r.Skill,
			Version:  r.Version,
			Digest:   r.Digest,
			Agent:    r.Model.Agent,
			Model:    r.Model.Model,
			Required: r.Model.Required,
			Attempts: r.Attempts,
			Trigger:  r.Trigger,
		},
	}
}

// Ensure creates the SkillEval unless one already exists for the same digest
// and model. It reports whether it created one.
func Ensure(ctx context.Context, c client.Client, namespace string, r Request) (bool, error) {
	eval := New(namespace, r)
	var existing v1alpha1.SkillEvalList
	if err := c.List(ctx, &existing, client.InNamespace(namespace), client.MatchingLabels(eval.Labels)); err != nil {
		return false, fmt.Errorf("listing skillevals for %s: %w", eval.Name, err)
	}
	if len(existing.Items) > 0 {
		return false, nil
	}
	if err := c.Create(ctx, eval); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, fmt.Errorf("creating skilleval %s: %w", eval.Name, err)
	}
	return true, nil
}
