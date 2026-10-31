package crds

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// countingReader answers Get from a set of names and counts the calls.
type countingReader struct {
	client.Reader
	present map[string]bool
	err     error
	calls   int
}

func (r *countingReader) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	if gvk := obj.GetObjectKind().GroupVersionKind(); gvk != crdGVK {
		return errors.New("unexpected kind " + gvk.String())
	}
	if !r.present[key.Name] {
		return apierrors.NewNotFound(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, key.Name)
	}
	obj.(*metav1.PartialObjectMetadata).SetName(key.Name)
	return nil
}

func TestDetector(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	r := &countingReader{present: map[string]bool{"ciliumnetworkpolicies.cilium.io": true}}
	d := &Detector{Reader: r, TTL: time.Minute, Now: func() time.Time { return now }}
	ctx := context.Background()

	steps := []struct {
		name      string
		crd       string
		advance   time.Duration
		install   bool
		want      bool
		wantCalls int
	}{
		{name: "present", crd: "ciliumnetworkpolicies.cilium.io", want: true, wantCalls: 1},
		{name: "cached", crd: "ciliumnetworkpolicies.cilium.io", want: true, wantCalls: 1},
		{name: "missing", crd: "sandboxwarmpools.extensions.agents.x-k8s.io", want: false, wantCalls: 2},
		{name: "missing is cached too", crd: "sandboxwarmpools.extensions.agents.x-k8s.io", install: true, want: false, wantCalls: 2},
		{name: "refreshed after the ttl", crd: "sandboxwarmpools.extensions.agents.x-k8s.io", advance: time.Minute, want: true, wantCalls: 3},
	}
	for _, s := range steps {
		now = now.Add(s.advance)
		if s.install {
			r.present[s.crd] = true
		}
		got, err := d.Has(ctx, s.crd)
		if err != nil || got != s.want || r.calls != s.wantCalls {
			t.Fatalf("%s: got %t, %v after %d calls; want %t after %d", s.name, got, err, r.calls, s.want, s.wantCalls)
		}
	}
}

func TestDetectorErrorIsNotCached(t *testing.T) {
	r := &countingReader{err: errors.New("forbidden")}
	d := &Detector{Reader: r}
	for range 2 {
		if _, err := d.Has(context.Background(), "x.example.com"); err == nil {
			t.Fatal("want an error")
		}
	}
	if r.calls != 2 {
		t.Fatalf("calls %d, want 2", r.calls)
	}
}

func TestHasAll(t *testing.T) {
	s := Static{"a": true, "b": true}
	tests := []struct {
		names []string
		want  bool
	}{
		{[]string{"a", "b"}, true},
		{[]string{"a", "c"}, false},
		{nil, true},
	}
	for _, tt := range tests {
		if got, err := HasAll(context.Background(), s, tt.names...); err != nil || got != tt.want {
			t.Errorf("%v: got %t, %v", tt.names, got, err)
		}
	}
	var none Static
	if ok, _ := none.Has(context.Background(), "a"); ok {
		t.Fatal("a nil Static has no CRDs")
	}
}
