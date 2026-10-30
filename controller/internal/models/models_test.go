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
			[]Model{{"claude-code", "haiku", true}, {"codex", "gpt", false}}, false},
		{"empty list", "models: []\n", []Model{}, false},
		{"missing model", "models:\n- agent: claude-code\n", nil, true},
		{"duplicate", "models:\n- {agent: a, model: m}\n- {agent: a, model: m}\n", nil, true},
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

func TestAdded(t *testing.T) {
	a := Model{"claude-code", "haiku", true}
	b := Model{"claude-code", "sonnet", true}
	c := Model{"codex", "haiku", false}
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
		{"required flip is not an add", []Model{a}, []Model{{"claude-code", "haiku", false}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Added(tt.prev, tt.current); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Added() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	in := []Model{{"claude-code", "haiku", true}, {"codex", "gpt", false}}
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
