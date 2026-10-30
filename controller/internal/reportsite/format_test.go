package reportsite

import (
	"testing"
	"time"
)

func TestFormatting(t *testing.T) {
	now := time.Date(2026, 10, 30, 14, 30, 0, 0, time.UTC)
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"age seconds", age(now, now.Add(-30*time.Second)), "30 s ago"},
		{"age just now", age(now, now.Add(-2*time.Second)), "just now"},
		{"age minutes", age(now, now.Add(-12*time.Minute)), "12 min ago"},
		{"age hours", age(now, now.Add(-3*time.Hour)), "3 h ago"},
		{"age days", age(now, now.Add(-72*time.Hour)), "3 d ago"},
		{"age of zero time", age(now, time.Time{}), ""},
		{"short age minutes", ageShort(now, now.Add(-12*time.Minute)), "12m"},
		{"short age future", ageShort(now, now.Add(time.Minute)), "0s"},
		{"time", formatTime(now, "2006-01-02 15:04"), "2026-10-30 14:30"},
		{"zero time", formatTime(time.Time{}, "2006-01-02"), "–"},
		{"time in another zone is shown in UTC", formatTime(now.In(time.FixedZone("CET", 3600)), "15:04"), "14:30"},
		{"duration seconds", formatDuration(42 * time.Second), "42s"},
		{"duration minutes", formatDuration(4*time.Minute + 12*time.Second), "4m 12s"},
		{"duration hours", formatDuration(2*time.Hour + 5*time.Minute), "2h 05m"},
		{"duration zero", formatDuration(0), "–"},
		{"short digest", shortDigest("sha256:0123456789abcdef0123"), "sha256:0123456789ab"},
		{"short digest without algorithm", shortDigest("abc"), "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}
