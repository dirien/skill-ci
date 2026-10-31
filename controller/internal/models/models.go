// Package models parses the model list ConfigMap and diffs two versions of it.
package models

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
)

// ConfigKey is the ConfigMap key that holds the model list.
const ConfigKey = "models.yaml"

// Model is one agent/model pair to evaluate skills with.
type Model struct {
	// Agent is the harness, for example claude-code.
	Agent string `json:"agent"`
	// AgentVersion is the release of the agent CLI, for example 2.1.292. Empty
	// means the runner's default pin for the agent.
	AgentVersion string `json:"agentVersion,omitempty"`
	// Model is the pinned model name.
	Model string `json:"model"`
	// Required marks models whose result gates promotion.
	Required bool `json:"required,omitempty"`
	// SandboxProfile names the SandboxProfile the trials run in; empty means
	// the SkillCIConfig spec.defaultSandboxProfile. It is not part of the
	// identity: changing it affects new runs only.
	SandboxProfile string `json:"sandboxProfile,omitempty"`
}

// Harness is agent@version, or the agent alone when no version is pinned.
func (m Model) Harness() string {
	if m.AgentVersion == "" {
		return m.Agent
	}
	return m.Agent + "@" + m.AgentVersion
}

// Key is the identity of a model entry: agent, agent version and model. A
// changed agent version is a different entry.
func (m Model) Key() string {
	return m.Harness() + "/" + m.Model
}

type list struct {
	Models []Model `json:"models"`
}

// Parse reads a model list. JSON is accepted too, since it is valid YAML.
func Parse(data []byte) ([]Model, error) {
	var l list
	if err := yaml.UnmarshalStrict(data, &l); err != nil {
		return nil, fmt.Errorf("parsing model list: %w", err)
	}
	seen := map[string]bool{}
	for _, m := range l.Models {
		if m.Agent == "" || m.Model == "" {
			return nil, fmt.Errorf("model entry %+v needs both agent and model", m)
		}
		if m.AgentVersion != "" && !v1alpha1.ValidAgentVersion(m.AgentVersion) {
			return nil, fmt.Errorf("model entry %s: agentVersion %q must be a release like 2.1.292", m.Key(), m.AgentVersion)
		}
		if m.SandboxProfile != "" {
			if errs := validation.IsDNS1123Subdomain(m.SandboxProfile); len(errs) > 0 {
				return nil, fmt.Errorf("model entry %s: sandboxProfile %q: %s", m.Key(), m.SandboxProfile, strings.Join(errs, "; "))
			}
		}
		if seen[m.Key()] {
			return nil, fmt.Errorf("duplicate model entry %s", m.Key())
		}
		seen[m.Key()] = true
	}
	return l.Models, nil
}

// Marshal renders a model list the way Parse reads it.
func Marshal(ms []Model) ([]byte, error) {
	b, err := yaml.Marshal(list{Models: ms})
	if err != nil {
		return nil, fmt.Errorf("marshalling model list: %w", err)
	}
	return b, nil
}

// FromConfigMap reads the model list out of the ConfigMap.
func FromConfigMap(cm *corev1.ConfigMap) ([]Model, error) {
	data, ok := cm.Data[ConfigKey]
	if !ok {
		return nil, fmt.Errorf("configmap %s has no key %s", cm.Name, ConfigKey)
	}
	return Parse([]byte(data))
}

// Added returns the entries of current whose agent, agent version and model are not in previous.
func Added(previous, current []Model) []Model {
	known := map[string]bool{}
	for _, m := range previous {
		known[m.Key()] = true
	}
	var added []Model
	for _, m := range current {
		if !known[m.Key()] {
			added = append(added, m)
		}
	}
	return added
}

// RequiredChanged returns the entries of current whose required flag differs
// from the entry of previous with the same identity. Entries that are new are
// not included: Added covers them.
func RequiredChanged(previous, current []Model) []Model {
	was := map[string]bool{}
	for _, m := range previous {
		was[m.Key()] = m.Required
	}
	var changed []Model
	for _, m := range current {
		if req, ok := was[m.Key()]; ok && req != m.Required {
			changed = append(changed, m)
		}
	}
	return changed
}
