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

// ── the [line] section ───────────────────────────────────────────────────

// Line is the raw [line] section: what this number is, and how it treats a
// caller who says nothing.
//
// Every key is optional, and a file with no [line] section at all compiles to
// exactly the behaviour every install had before lines existed. That is the
// compatibility gate for this section, and it is why nothing here is required
// and nothing here defaults to anything but today.
type Line struct {
	Label     string `toml:"label"`
	Number    string `toml:"number"`
	Prompts   string `toml:"prompts"`
	OnNoInput string `toml:"on_no_input"`
}

// NoInput is what happens to a caller who reaches the end of the dial window
// having entered nothing at all. It is the disposition knob: a curt doorman
// and a courteous concierge are the same engine with opposite settings, so
// neither has to be a special case in code.
//
// The values are the TOML strings themselves, so `doorman check` and the logs
// print what an operator wrote rather than a translation of it.
type NoInput string

const (
	// NoInputDismiss is the historical behaviour and the default: the parting
	// prompt, then a hangup.
	NoInputDismiss NoInput = "dismiss"
	// NoInputRingHouse puts a silent caller through to the house ring group.
	// Worth choosing deliberately: it makes the allow-list a shortcut rather
	// than a gate, because anyone patient enough to say nothing gets in.
	NoInputRingHouse NoInput = "ring-house"
	// NoInputVoicemail hands a silent caller to the house mailbox. "Never drop
	// a lead" is the whole reason a business line wants a second number.
	NoInputVoicemail NoInput = "voicemail"
)

// LineIdentity is the compiled [line] section.
type LineIdentity struct {
	// Label is a human name for the number — "Mertaugh Enterprises".
	Label string
	// Number is the DID, normalised to E.164. Identity, not routing: the
	// dialplan already said which line a call arrived on, so nothing matches
	// against this.
	Number string
	// Prompts overrides PROMPT_MEDIA_PREFIX for calls on this line. Empty
	// means the environment's prefix — the pack every other line uses.
	Prompts string
	// OnNoInput is never empty once compiled; unset resolves to NoInputDismiss.
	OnNoInput NoInput
}

// promptPrefix is what an Asterisk media prefix may look like: path segments
// under /var/lib/asterisk/sounds/, and nothing that could climb out of them.
// doorman never opens these files — Asterisk resolves the prefix — which is
// precisely why "../.." is worth refusing here rather than discovering what
// Asterisk makes of it.
var promptPrefix = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*(/[A-Za-z0-9][A-Za-z0-9_-]*)*$`)

// compileLine validates and compiles the [line] section. houseMailbox is
// [house] voicemail: on_no_input = "voicemail" has nowhere to send a caller
// without one, which is the same rule afterhours already applies per
// extension, for the same reason.
func compileLine(l Line, houseMailbox string, fail func(string, ...any)) LineIdentity {
	out := LineIdentity{Label: l.Label, Prompts: l.Prompts, OnNoInput: NoInputDismiss}

	if l.Number != "" {
		// Same treatment as an allow-listed number: unparseable is a load
		// error, never a value quietly kept as written.
		n := NormaliseCallerID(l.Number, "1")
		if n.Kind != KindE164 {
			fail("[line] number %q is not a valid phone number", l.Number)
		} else {
			out.Number = n.Value
		}
	}

	if l.Prompts != "" && !promptPrefix.MatchString(l.Prompts) {
		fail("[line] prompts %q must be an Asterisk media prefix such as \"concierge\" — "+
			"letters, digits, - and _, optionally in / separated segments. It names a "+
			"directory under /var/lib/asterisk/sounds/, not a filesystem path", l.Prompts)
	}

	switch NoInput(l.OnNoInput) {
	case "":
		// Unset is dismiss, which is what the lobby has always done.
	case NoInputDismiss, NoInputRingHouse, NoInputVoicemail:
		out.OnNoInput = NoInput(l.OnNoInput)
	default:
		fail("[line] on_no_input %q must be one of %s, %s or %s",
			l.OnNoInput, NoInputDismiss, NoInputRingHouse, NoInputVoicemail)
	}

	if out.OnNoInput == NoInputVoicemail && houseMailbox == "" {
		fail("[line] on_no_input = %q but [house] has no voicemail — "+
			"a caller who says nothing needs a mailbox to land in", NoInputVoicemail)
	}
	return out
}

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
