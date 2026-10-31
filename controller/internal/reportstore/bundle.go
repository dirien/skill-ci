package reportstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
)

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
	_, m, err := s.open(ctx, skill, manifestDigest)
	if err != nil {
		return nil, err
	}
	if m.Annotations == nil {
		return map[string]string{}, nil
	}
	return m.Annotations, nil
}

// Gunzip returns one gzip layer of the manifest with the given digest, found
// by its title like File, decompressed. It reads at most limit decompressed
// bytes and returns ErrTooLarge past them, so a small layer cannot expand
// into a large allocation. Concurrent calls for the same layer share one
// fetch. It returns ErrNotFound when the manifest or the layer is gone.
//
// The decompressed bytes are kept in the trial document cache, separate from
// the report cache, and are shared with it: the result must not be modified.
func (s *OCI) Gunzip(ctx context.Context, skill, manifestDigest, name string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("gunzip %s: limit %d is not positive", name, limit)
	}
	repo, manifest, err := s.open(ctx, skill, manifestDigest)
	if err != nil {
		return nil, err
	}
	layer, err := findLayer(manifest, manifestDigest, name)
	if err != nil {
		return nil, err
	}
	key := layer.Digest.String()
	data, ok := s.trials.get(key)
	if !ok {
		// The limit is part of the flight key: a caller with a larger limit
		// must not inherit ErrTooLarge from a caller with a smaller one.
		flight := "gunzip " + skill + " " + key + " " + strconv.FormatInt(limit, 10)
		data, err = s.shared(ctx, flight, func(ctx context.Context) ([]byte, error) {
			if data, ok := s.trials.get(key); ok {
				return data, nil
			}
			compressed, err := s.fetchBlob(ctx, repo, layer)
			if err != nil {
				return nil, err
			}
			data, err := gunzip(compressed, limit)
			if err != nil {
				return nil, fmt.Errorf("file %s in manifest %s: %w", name, manifestDigest, err)
			}
			s.trials.put(key, data)
			return data, nil
		})
		if err != nil {
			return nil, err
		}
	}
	// A cached document may come from a caller with a larger limit.
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file %s in manifest %s decompresses to %d bytes, over the %d byte limit: %w",
			name, manifestDigest, len(data), limit, ErrTooLarge)
	}
	return data, nil
}

// gunzip decompresses data, reading at most limit bytes of output. Reading up
// to the end of the stream also checks the gzip checksum.
func gunzip(data []byte, limit int64) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("opening gzip stream: %w", err)
	}
	defer func() { _ = zr.Close() }()
	n := limit
	if n < math.MaxInt64 {
		n++ // one byte past the limit tells "exactly limit" from "more"
	}
	out, err := io.ReadAll(io.LimitReader(zr, n))
	if err != nil {
		return nil, fmt.Errorf("decompressing gzip stream: %w", err)
	}
	if int64(len(out)) > limit {
		return nil, fmt.Errorf("decompresses to more than %d bytes: %w", limit, ErrTooLarge)
	}
	return out, nil
}
