// Package semver handles the vX.Y.Z tags that mark skill releases.
package semver

import (
	"regexp"
	"strconv"
)

var release = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// IsRelease reports whether tag is a plain vX.Y.Z release tag.
func IsRelease(tag string) bool {
	return release.MatchString(tag)
}

// Compare returns -1, 0 or 1 when a is older than, equal to or newer than b.
// Tags that are not releases sort before every release.
func Compare(a, b string) int {
	pa, pb := parse(a), parse(b)
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Newest returns the newest release tag in tags, or "" if there is none.
func Newest(tags []string) string {
	newest := ""
	for _, t := range tags {
		if IsRelease(t) && (newest == "" || Compare(t, newest) > 0) {
			newest = t
		}
	}
	return newest
}

// parse returns major, minor and patch, or zeros for a non-release tag.
func parse(tag string) [3]int {
	var v [3]int
	m := release.FindStringSubmatch(tag)
	if m == nil {
		return v
	}
	for i := range v {
		// The regexp guarantees digits; a value too large for int stays 0.
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v
}
