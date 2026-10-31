package apiv1

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The golden test of package reportsite takes -update, and go test passes the
// flag to every package of ./internal/reportsite/..., so this one defines it too.
var _ = flag.Bool("update", false, "unused here: the reportsite golden test rewrites the fixtures")

// fixtures is where the UI reads its API fixtures (ui/src/api/fixtures).
const fixtures = "../../../../ui/src/api/fixtures"

// fixtureType maps a fixture file name prefix to the response type it holds.
// Longer prefixes come first.
var fixtureTypes = []struct {
	prefix string
	new    func() any
}{
	{"site", func() any { return new(Site) }},
	{"overview", func() any { return new(Overview) }},
	{"runs", func() any { return new(RunList) }},
	{"run-", func() any { return new(RunDetail) }},
	{"skills", func() any { return new(SkillList) }},
	{"skill-", func() any { return new(SkillDetail) }},
	{"models", func() any { return new(ModelList) }},
	{"trial-", func() any { return new(TrialSession) }},
	{"compare-", func() any { return new(Compare) }},
	{"error-", func() any { return new(Error) }},
}

// TestFixturesMatchTypes keeps ui/src/api/fixtures in line with these types:
// every fixture decodes without unknown fields, encodes back to the same JSON
// (so no field the Go types always write is missing, and lists are [] rather
// than null), and holds null only where a cell is missing.
func TestFixturesMatchTypes(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtures, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in %s: %v", fixtures, err)
	}
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			v := newFixture(name)
			if v == nil {
				t.Fatalf("no response type for %s; add its prefix to fixtureTypes", name)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			if err := dec.Decode(v); err != nil {
				t.Fatalf("does not decode into %T: %v", v, err)
			}
			again, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(again, &got); err != nil {
				t.Fatal(err)
			}
			if diff := firstDiff("", want, got); diff != "" {
				t.Errorf("does not round-trip through %T: %s", v, diff)
			}
			checkNulls(t, "", "", want)
		})
	}
}

func newFixture(name string) any {
	for _, f := range fixtureTypes {
		if strings.HasPrefix(name, f.prefix) {
			return f.new()
		}
	}
	return nil
}

// firstDiff returns the first difference between two decoded JSON values.
func firstDiff(path string, want, got any) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return path + ": not an object after encoding"
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok {
				return path + "/" + k + ": dropped by the Go type (omitempty on a value the fixture sets?)"
			}
			if d := firstDiff(path+"/"+k, wv, gv); d != "" {
				return d
			}
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				return path + "/" + k + ": missing in the fixture but always written by the Go type"
			}
		}
		return ""
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return path + ": list differs after encoding"
		}
		for i := range w {
			if d := firstDiff(path+"/"+itoa(i), w[i], g[i]); d != "" {
				return d
			}
		}
		return ""
	default:
		if !reflect.DeepEqual(want, got) {
			return path + ": value changes when encoded"
		}
		return ""
	}
}

// checkNulls fails on null anywhere except an element of a cells, withSkill
// or withoutSkill list (missing cells of the skill matrix and the trial grid).
func checkNulls(t *testing.T, path, parentKey string, v any) {
	t.Helper()
	switch x := v.(type) {
	case nil:
		t.Errorf("%s: null outside a cell list", path)
	case map[string]any:
		for k, c := range x {
			if c == nil {
				t.Errorf("%s/%s: null; leave the field out instead", path, k)
				continue
			}
			checkNulls(t, path+"/"+k, k, c)
		}
	case []any:
		for i, c := range x {
			if c == nil && (parentKey == "cells" || parentKey == "withSkill" || parentKey == "withoutSkill") {
				continue
			}
			checkNulls(t, path+"/"+itoa(i), "", c)
		}
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
