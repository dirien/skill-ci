package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdata is the repository's shared fixture directory.
const testdata = "../../../testdata"

// decodeStrict decodes data into v and fails on fields v does not have.
func decodeStrict(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s does not match the session types: %v", path, err)
	}
}

// TestFixturesMatchTypes ties the golden bundles to the Go types: every
// index.json and trial document decodes without unknown fields, ids are
// valid, and the index and its trial documents agree.
func TestFixturesMatchTypes(t *testing.T) {
	indexes, err := filepath.Glob(filepath.Join(testdata, "trials*", "*", "bundle*", IndexFile))
	if err != nil || len(indexes) < 5 {
		t.Fatalf("found %d bundle fixtures (%v), want at least 5", len(indexes), err)
	}
	for _, path := range indexes {
		var idx Index
		decodeStrict(t, path, &idx)
		if idx.Schema != SchemaIndex {
			t.Errorf("%s: schema %q", path, idx.Schema)
		}
		for _, entry := range idx.Trials {
			if !TrialID.MatchString(entry.ID) || entry.Layer != TrialLayer(entry.ID) {
				t.Errorf("%s: trial %q has layer %q", path, entry.ID, entry.Layer)
			}
			var doc Trial
			decodeStrict(t, filepath.Join(filepath.Dir(path), strings.TrimSuffix(entry.Layer, ".gz")), &doc)
			if doc.Schema != SchemaTrial || doc.ID != entry.ID || doc.Arm != entry.Arm || doc.Case != entry.Case ||
				doc.Attempt != entry.Attempt || doc.Status != entry.Status {
				t.Errorf("%s: trial document %s does not match its index entry", path, entry.ID)
			}
			if doc.Trajectory == nil && doc.TrajectoryOmitted == "" {
				t.Errorf("%s: trial %s has neither a trajectory nor a reason", path, entry.ID)
			}
			if doc.Trajectory != nil && len(doc.Trajectory.Steps) != entry.Steps {
				t.Errorf("%s: trial %s has %d steps, the index says %d", path, entry.ID, len(doc.Trajectory.Steps), entry.Steps)
			}
		}
	}
}
