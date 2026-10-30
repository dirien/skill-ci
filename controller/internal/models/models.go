// Package models parses the model list ConfigMap and diffs two versions of it.
package models

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// ConfigKey is the ConfigMap key that holds the model list.
const ConfigKey = "models.yaml"

// Model is one agent/model pair to evaluate skills with.
type Model struct {
	// Agent is the harness, for example claude-code.
	Agent string `json:"agent"`
	// Model is the pinned model name.
	Model string `json:"model"`
	// Required marks models whose result gates promotion.
	Required bool `json:"required,omitempty"`
}

// Key is the identity of a model entry.
func (m Model) Key() string {
	return m.Agent + "/" + m.Model
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

// Added returns the entries of current whose agent/model pair is not in previous.
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
