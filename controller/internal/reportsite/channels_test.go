package reportsite

import (
	"context"
	"testing"
	"time"

	"github.com/dirien/skill-ci/controller/internal/harbor"
)

// ctxHarbor fails with the context's error, as the real client does once its
// request context is done.
type ctxHarbor struct{ harbor.Client }

func (c ctxHarbor) ListRepositories(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []string{"acme"}, nil
}

func (c ctxHarbor) ListArtifacts(ctx context.Context, _ string) ([]harbor.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []harbor.Artifact{{Digest: "sha256:1", Tags: []string{"v1.0.0", "stable"}}}, nil
}

// TestChannelsOutliveACanceledRequest keeps a canceled request from caching
// "context canceled" for every other request.
func TestChannelsOutliveACanceledRequest(t *testing.T) {
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	src := &channelSource{client: ctxHarbor{}, now: func() time.Time { return now }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ch := src.get(ctx); ch.Err != nil {
		t.Fatalf("a canceled request loaded %v", ch.Err)
	}
	if ch := src.get(context.Background()); ch.Err != nil || ch.OnStable() != 1 {
		t.Errorf("the next request got %+v", ch)
	}
}
