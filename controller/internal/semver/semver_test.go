package semver

import "testing"

func TestIsRelease(t *testing.T) {
	tests := map[string]bool{
		"v1.0.0":   true,
		"v10.20.3": true,
		"1.0.0":    false,
		"v1.0":     false,
		"v1.0.0-1": false,
		"beta":     false,
		"stable":   false,
		"":         false,
	}
	for tag, want := range tests {
		if got := IsRelease(tag); got != want {
			t.Errorf("IsRelease(%q) = %v, want %v", tag, got, want)
		}
	}
}

func TestCompareAndNewest(t *testing.T) {
	tests := []struct {
		name string
		tags []string
		want string
	}{
		{"numeric not lexical", []string{"v1.9.0", "v1.10.0"}, "v1.10.0"},
		{"major wins", []string{"v2.0.0", "v1.99.99"}, "v2.0.0"},
		{"ignores non releases", []string{"beta", "v0.1.0", "stable"}, "v0.1.0"},
		{"none", []string{"beta"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Newest(tt.tags); got != tt.want {
				t.Errorf("Newest(%v) = %q, want %q", tt.tags, got, tt.want)
			}
		})
	}
	if Compare("v1.0.0", "v1.0.0") != 0 || Compare("v1.0.1", "v1.0.0") != 1 || Compare("v1.0.0", "v1.0.1") != -1 {
		t.Error("Compare returned the wrong ordering")
	}
}
