package reportsite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dirien/skill-ci/controller/internal/session"
)

const (
	// DefaultLLMKeysSecret is the Secret whose values the site redacts at render time.
	DefaultLLMKeysSecret = "llm-keys"
	// secretTTL is how long the values of llm-keys are reused before they are read again.
	secretTTL = time.Minute
	// secretTimeout bounds one read of llm-keys.
	secretTimeout = 5 * time.Second
	// secretRetry is how long a failed read of llm-keys is remembered before
	// the next request tries again.
	secretRetry = 10 * time.Second
)

// notSecretKeys are llm-keys keys whose values are not credentials: redacting
// them would only hide hosts in the session.
var notSecretKeys = []string{"openai-base-url"}

// redactorSource builds the render-time Redactor from the values of the
// llm-keys Secret and the Harbor credentials, and reads the Secret again
// after a minute so a rotated key is redacted too.
type redactorSource struct {
	reader               client.Reader
	namespace, secret    string
	harborUser, harborPw string
	now                  func() time.Time

	mu   sync.Mutex
	r    *session.Redactor
	fp   string
	at   time.Time
	good bool
	// failed and failedAt remember the last failed read while no read succeeded.
	failed   error
	failedAt time.Time
}

// redactor returns the Redactor and a fingerprint of its values for cache
// keys. The error is set when llm-keys could not be read and no earlier
// read succeeded; the Redactor then holds the Harbor credentials only.
func (s *redactorSource) redactor(ctx context.Context) (*session.Redactor, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.good && s.now().Sub(s.at) < secretTTL {
		return s.r, s.fp, nil
	}
	if !s.good && s.failed != nil && s.now().Sub(s.failedAt) < secretRetry {
		return s.r, s.fp, s.failed
	}
	values, err := s.read(ctx)
	if err != nil {
		if s.good {
			// Keep the last values that were read; try again after the TTL.
			s.at = s.now()
			return s.r, s.fp, nil
		}
		s.r, s.fp = s.build(nil)
		s.failed, s.failedAt = err, s.now()
		return s.r, s.fp, err
	}
	s.r, s.fp = s.build(values)
	s.at, s.good, s.failed = s.now(), true, nil
	return s.r, s.fp, nil
}

// build makes a Redactor from the llm-keys values plus the Harbor credentials.
func (s *redactorSource) build(values []string) (*session.Redactor, string) {
	all := slices.Clone(values)
	if s.harborPw != "" {
		all = append(all, s.harborPw)
		if s.harborUser != "" {
			// The registry auth value is base64 of user:password; the Redactor adds the base64 forms.
			all = append(all, s.harborUser+":"+s.harborPw)
		}
	}
	slices.Sort(all)
	h := sha256.New()
	for _, v := range all {
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	return session.NewRedactor(all), hex.EncodeToString(h.Sum(nil))[:16]
}

// read returns the credential values of llm-keys; a missing Secret has none.
func (s *redactorSource) read(ctx context.Context) ([]string, error) {
	if s.reader == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, secretTimeout)
	defer cancel()
	var sec corev1.Secret
	err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: s.secret}, &sec)
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("reading Secret %s/%s for render-time redaction: %w", s.namespace, s.secret, err)
	}
	var values []string
	for key, v := range sec.Data {
		if !slices.Contains(notSecretKeys, key) {
			values = append(values, string(v))
		}
	}
	for key, v := range sec.StringData {
		if !slices.Contains(notSecretKeys, key) {
			values = append(values, v)
		}
	}
	return values, nil
}
