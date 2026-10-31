// Package crds reports whether optional CustomResourceDefinitions (Cilium,
// Agent Sandbox extensions) are installed, caching each answer for a while so
// a reconcile does not ask the API server every time.
package crds

import (
	"context"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultTTL is how long an answer is reused before the CRD is looked up again.
const DefaultTTL = time.Minute

var crdGVK = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}

// Checker answers whether a CRD exists.
type Checker interface {
	// Has reports whether the CRD with the given name (plural.group) exists.
	Has(ctx context.Context, name string) (bool, error)
}

// Detector is a Checker that reads CRD metadata through an uncached reader.
type Detector struct {
	// Reader reads straight from the API server; it needs get on
	// customresourcedefinitions.
	Reader client.Reader
	// TTL is how long an answer is cached; zero means DefaultTTL.
	TTL time.Duration
	// Now returns the current time; nil means time.Now.
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	present bool
	at      time.Time
}

var _ Checker = (*Detector)(nil)

// Has implements Checker. A lookup error is returned and not cached.
func (d *Detector) Has(ctx context.Context, name string) (bool, error) {
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	ttl := d.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	d.mu.Lock()
	if e, ok := d.cache[name]; ok && now().Sub(e.at) < ttl {
		d.mu.Unlock()
		return e.present, nil
	}
	d.mu.Unlock()

	crd := &metav1.PartialObjectMetadata{}
	crd.SetGroupVersionKind(crdGVK)
	err := d.Reader.Get(ctx, client.ObjectKey{Name: name}, crd)
	present := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("looking up CRD %s: %w", name, err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cache == nil {
		d.cache = map[string]entry{}
	}
	d.cache[name] = entry{present: present, at: now()}
	return present, nil
}

// HasAll reports whether every named CRD exists.
func HasAll(ctx context.Context, c Checker, names ...string) (bool, error) {
	for _, n := range names {
		ok, err := c.Has(ctx, n)
		if err != nil {
			return false, fmt.Errorf("checking CRD %s: %w", n, err)
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// Static is a Checker with a fixed answer per CRD, for tests and for a
// controller without access to CRDs. A nil Static has no CRDs.
type Static map[string]bool

// Has implements Checker.
func (s Static) Has(_ context.Context, name string) (bool, error) { return s[name], nil }
