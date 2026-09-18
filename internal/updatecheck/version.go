package updatecheck

import (
	"regexp"
	"strings"
)

var semver = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
var sourceStamp = regexp.MustCompile(`-g[0-9a-fA-F]+(?:-|$)`)

func parseVersion(s string) []string {
	m := semver.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	for _, id := range strings.Split(m[4], ".") {
		if numeric(id) && len(id) > 1 && id[0] == '0' {
			return nil
		}
	}
	return m
}
func numeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func compareNumber(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
func newer(latest, current string) bool {
	a, b := parseVersion(latest), parseVersion(current)
	if a == nil || b == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		if c := compareNumber(a[i], b[i]); c != 0 {
			return c > 0
		}
	}
	if a[4] == b[4] {
		return false
	}
	if a[4] == "" {
		return true
	}
	if b[4] == "" {
		return false
	}
	x, y := strings.Split(a[4], "."), strings.Split(b[4], ".")
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] == y[i] {
			continue
		}
		nx, ny := numeric(x[i]), numeric(y[i])
		if nx && ny {
			return compareNumber(x[i], y[i]) > 0
		}
		if nx != ny {
			return !nx
		}
		return x[i] > y[i]
	}
	return len(x) > len(y)
}
