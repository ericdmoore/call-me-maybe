package policy

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DefaultLine is the line served by the unsuffixed policy file. It is what a
// call with no Stasis line argument gets, which is every call on every install
// that predates multiple lines.
const DefaultLine = "default"

// LineFile pairs the name the dialplan uses — `Stasis(app,line,<name>)` — with
// the policy file that configures it.
type LineFile struct {
	Name string
	Path string
}

// IgnoredLineFile is a sibling that looks like a line policy but cannot be
// one. Reported rather than skipped in silence: a file called
// policy.default.toml is somebody expecting it to configure the default line,
// and saying nothing there is the same defect as swallowing an unknown key.
type IgnoredLineFile struct {
	Path   string
	Reason string
}

// lineName is deliberately narrower than what a filesystem allows. The name
// travels through a dialplan argument and into log fields, and the set of
// characters that survives both unchanged is small.
var lineName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// DiscoverLines lists the lines this install serves: the default line, whose
// file is policyPath itself, plus one for every sibling `policy.<name>.toml`.
// The default is always first; the rest are sorted, so logs and `doorman
// check` read the same way twice running.
//
// One file per line rather than [[lines]] sections in one file is invariant 4
// generalised — the caller opens one Store per file, so a stray bracket in the
// business policy cannot stop the house phone ringing. It also means the set
// of lines is discovered rather than declared: doorman cannot read the
// dialplan, so it can never know the authoritative set anyway.
//
// Nothing here reads or validates a file. A line whose policy will not load is
// the caller's problem to report, because what to do about it differs between
// the daemon (carry on without that line) and `doorman check` (say so and exit
// non-zero).
func DiscoverLines(policyPath string) ([]LineFile, []IgnoredLineFile) {
	lines := []LineFile{{Name: DefaultLine, Path: policyPath}}

	base := filepath.Base(policyPath)
	stem, ok := strings.CutSuffix(base, ".toml")
	if !ok {
		// A policy file not named *.toml has no sibling pattern to look for.
		return lines, nil
	}
	// Keep whatever the operator wrote — "./", "/etc/call-me-maybe/", or
	// nothing at all — so every path printed back at them is in the same
	// shape as the one they configured.
	dir := policyPath[:len(policyPath)-len(base)]

	entries, err := os.ReadDir(filepath.Dir(policyPath))
	if err != nil {
		// An unreadable directory is not this function's error to raise: the
		// default line's own file is about to be opened by the caller, and
		// that failure carries a far better message than this one could.
		return lines, nil
	}

	prefix := stem + "."
	var found []LineFile
	var ignored []IgnoredLineFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok {
			continue
		}
		name, ok = strings.CutSuffix(name, ".toml")
		if !ok {
			continue
		}
		path := dir + e.Name()
		switch {
		case name == DefaultLine:
			ignored = append(ignored, IgnoredLineFile{path,
				"the default line is configured by " + policyPath + " itself"})
		case !lineName.MatchString(name):
			ignored = append(ignored, IgnoredLineFile{path,
				"line names are letters, digits, - and _, starting with a letter or digit"})
		default:
			found = append(found, LineFile{Name: name, Path: path})
		}
	}

	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	sort.Slice(ignored, func(i, j int) bool { return ignored[i].Path < ignored[j].Path })
	return append(lines, found...), ignored
}
