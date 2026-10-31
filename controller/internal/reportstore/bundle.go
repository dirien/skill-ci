package reportstore

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned by the trial bundle methods until they are built.
var ErrNotImplemented = errors.New("reportstore: not implemented")

// ErrTooLarge means a file is over the size limit the caller gave; for a gzip
// layer the limit applies to the decompressed bytes.
var ErrTooLarge = errors.New("file over the size limit")

// Annotations returns the annotations of the referrer manifest with the given
// digest: a report (skillci.dev/trials, skillci.dev/trials-withheld) or a
// trial bundle (skillci.dev/trials-schema, skillci.dev/redaction). The manifest
// is the one File reads and caches, so asking for the annotations of a report
// whose result.json was read costs no request. The map must not be modified.
// It returns ErrNotFound when the manifest is gone.
func (s *OCI) Annotations(ctx context.Context, skill, manifestDigest string) (map[string]string, error) {
	return nil, ErrNotImplemented
}

// Gunzip returns one gzip layer of the manifest with the given digest, found
// by its title like File, decompressed. It reads at most limit decompressed
// bytes and returns ErrTooLarge past them, so a small layer cannot expand
// into a large allocation. Concurrent calls for the same layer share one
// fetch. It returns ErrNotFound when the manifest or the layer is gone.
func (s *OCI) Gunzip(ctx context.Context, skill, manifestDigest, name string, limit int64) ([]byte, error) {
	return nil, ErrNotImplemented
}
