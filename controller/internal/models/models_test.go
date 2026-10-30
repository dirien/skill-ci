package models

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []Model
		wantErr bool
	}{
		{"valid", "models:\n- agent: claude-code\n  model: haiku\n  required: true\n- agent: codex\n  model: gpt\n",
			[]Model{{Agent: "claude-code", Model: "haiku", Required: true}, {Agent: "codex", Model: "gpt"}}, false},
		{"agent version", "models:\n- {agent: claude-code, agentVersion: 2.1.292, model: opus, required: true}\n",
			[]Model{{Agent: "claude-code", AgentVersion: "2.1.292", Model: "opus", Required: true}}, false},
		{"same model, two versions", "models:\n- {agent: a, agentVersion: '1.0', model: m}\n- {agent: a, agentVersion: '1.1', model: m}\n",
			[]Model{{Agent: "a", AgentVersion: "1.0", Model: "m"}, {Agent: "a", AgentVersion: "1.1", Model: "m"}}, false},
		{"same model, pinned and unpinned", "models:\n- {agent: a, agentVersion: '1.0', model: m}\n- {agent: a, model: m}\n",
			[]Model{{Agent: "a", AgentVersion: "1.0", Model: "m"}, {Agent: "a", Model: "m"}}, false},
		{"empty list", "models: []\n", []Model{}, false},
		{"missing model", "models:\n- agent: claude-code\n", nil, true},
		{"duplicate", "models:\n- {agent: a, model: m}\n- {agent: a, model: m}\n", nil, true},
		{"duplicate with version", "models:\n- {agent: a, agentVersion: '1.0', model: m}\n- {agent: a, agentVersion: '1.0', model: m}\n", nil, true},
		{"version with a space", "models:\n- {agent: a, agentVersion: '1 0', model: m}\n", nil, true},
		{"version with shell syntax", "models:\n- {agent: a, agentVersion: '1.0;id', model: m}\n", nil, true},
		{"version ends in a dash", "models:\n- {agent: a, agentVersion: '1.0-', model: m}\n", nil, true},
		{"unknown field", "models:\n- {agent: a, model: m, extra: 1}\n", nil, true},
		{"not yaml", "models: [", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKey(t *testing.T) {
	tests := []struct {
		m         Model
		key, harn string
	}{
		{Model{Agent: "claude-code", Model: "opus"}, "claude-code/opus", "claude-code"},
		{Model{Agent: "claude-code", AgentVersion: "2.1.292", Model: "opus"}, "claude-code@2.1.292/opus", "claude-code@2.1.292"},
	}
	for _, tt := range tests {
		if got := tt.m.Key(); got != tt.key {
			t.Errorf("Key() = %q, want %q", got, tt.key)
		}
		if got := tt.m.Harness(); got != tt.harn {
			t.Errorf("Harness() = %q, want %q", got, tt.harn)
		}
	}
}

func TestAdded(t *testing.T) {
	a := Model{Agent: "claude-code", Model: "haiku", Required: true}
	b := Model{Agent: "claude-code", Model: "sonnet", Required: true}
	c := Model{Agent: "codex", Model: "haiku"}
	pinned := Model{Agent: "claude-code", AgentVersion: "2.1.292", Model: "haiku", Required: true}
	bumped := Model{Agent: "claude-code", AgentVersion: "2.1.300", Model: "haiku", Required: true}
	tests := []struct {
		name          string
		prev, current []Model
		want          []Model
	}{
		{"nothing changed", []Model{a, b}, []Model{a, b}, nil},
		{"model added", []Model{a}, []Model{a, b}, []Model{b}},
		{"first sight adds all", nil, []Model{a, b}, []Model{a, b}},
		{"model removed", []Model{a, b}, []Model{a}, nil},
		{"same model other agent is new", []Model{a}, []Model{a, c}, []Model{c}},
		{"required flip is not an add", []Model{a}, []Model{{Agent: "claude-code", Model: "haiku"}}, nil},
		{"pinning an unpinned entry is new", []Model{a}, []Model{pinned}, []Model{pinned}},
		{"a bumped agent version is new", []Model{pinned}, []Model{bumped}, []Model{bumped}},
		{"a bumped version kept next to the old one", []Model{pinned}, []Model{pinned, bumped}, []Model{bumped}},
		{"unchanged version is not new", []Model{pinned}, []Model{pinned}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Added(tt.prev, tt.current); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Added() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRequiredChanged(t *testing.T) {
	optional := Model{Agent: "codex", AgentVersion: "0.161.0", Model: "gpt"}
	required := optional
	required.Required = true
	other := Model{Agent: "claude-code", Model: "opus", Required: true}
	tests := []struct {
		name          string
		prev, current []Model
		want          []Model
	}{
		{"nothing changed", []Model{optional, other}, []Model{optional, other}, nil},
		{"made required", []Model{optional, other}, []Model{required, other}, []Model{required}},
		{"made optional", []Model{required}, []Model{optional}, []Model{optional}},
		{"a new entry is not a flip", []Model{other}, []Model{other, required}, nil},
		{"another version is another entry", []Model{optional}, []Model{{Agent: "codex", AgentVersion: "0.162.0", Model: "gpt", Required: true}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RequiredChanged(tt.prev, tt.current); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RequiredChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	in := []Model{{Agent: "claude-code", AgentVersion: "2.1.292", Model: "haiku", Required: true}, {Agent: "codex", Model: "gpt"}}
	b, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip = %v, want %v", out, in)
	}
}
