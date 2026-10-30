package reportsite

import (
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
)

func TestRetryNoteOnRunPage(t *testing.T) {
	const (
		skill   = "acme-release-notes"
		failure = "no scores in result.json: EnvironmentStartTimeoutError: Environment start timed out after 600.0 seconds"
	)
	push := v1alpha1.TriggerPush
	once := ok(skill, "v1.0.0", haiku, true, push, at(9, 0), "0.95", "0.36", "+0.59", v1alpha1.VerdictHelps)
	once.retries, once.retried = 1, failure
	twice := ok(skill, "v1.0.0", sonnet, true, push, at(9, 0), "0.90", "0.36", "+0.54", v1alpha1.VerdictHelps)
	twice.retries, twice.retried = 2, failure
	plain := ok(skill, "v1.1.0", haiku, true, push, at(9, 10), "0.95", "0.36", "+0.59", v1alpha1.VerdictHelps)
	failed := ok(skill, "v1.2.0", haiku, true, push, at(9, 20), "", "", "", "")
	failed.phase, failed.message, failed.retries, failed.retried = v1alpha1.PhaseFailed, failure, 1, "job failed: DeadlineExceeded"

	h := newHarness(t, []spec{once, twice, plain, failed})
	tests := []struct {
		name   string
		path   string
		want   []string
		absent []string
	}{
		{
			name: "retried once shows the earlier failure", path: runPath(skill, "v1.0.0", haiku),
			want:   []string{"Retried once after an infrastructure failure: " + failure},
			absent: []string{"times after infrastructure failures"},
		},
		{
			name: "retried twice says how often", path: runPath(skill, "v1.0.0", sonnet),
			want:   []string{"Retried 2 times after infrastructure failures; the last failure: " + failure},
			absent: []string{"Retried once"},
		},
		{
			name: "a run that was not retried has no note", path: runPath(skill, "v1.1.0", haiku),
			absent: []string{"after an infrastructure failure", "after infrastructure failures"},
		},
		{
			name: "a failed run shows the failure and the retry", path: runPath(skill, "v1.2.0", haiku),
			want: []string{"Retried once after an infrastructure failure: job failed: DeadlineExceeded", failure},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.get(tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			body := html.UnescapeString(rec.Body.String())
			for _, w := range tt.want {
				if !strings.Contains(body, w) {
					t.Errorf("body lacks %q", w)
				}
			}
			for _, a := range tt.absent {
				if strings.Contains(body, a) {
					t.Errorf("body contains %q", a)
				}
			}
		})
	}
}

func TestRetryNote(t *testing.T) {
	tests := []struct {
		name string
		run  Run
		want string
	}{
		{"never retried", Run{}, ""},
		{"retried once", Run{Retries: 1, Retried: "boom"}, "Retried once after an infrastructure failure: boom"},
		{"retried once without a condition", Run{Retries: 1}, "Retried once after an infrastructure failure."},
		{"retried three times", Run{Retries: 3, Retried: "boom"}, "Retried 3 times after infrastructure failures; the last failure: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.run.RetryNote(); got != tt.want {
				t.Errorf("RetryNote() = %q, want %q", got, tt.want)
			}
		})
	}
}
