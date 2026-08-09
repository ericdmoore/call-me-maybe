package policy

// Trunks: the provider inventory.
//
// A trunk is one registration this box opens outbound to a provider; inbound
// calls arrive back down the pipe it already established, which is what lets
// the whole system work with no port forward and no static IP. With one
// provider that fits comfortably in a hand-written pjsip.conf — the shipped
// example is exactly that, edited once and never again.
//
// Several providers turn it into a data problem: the same handful of fields
// per provider, one inbound dialplan context each, and one route per DID,
// which grows as providers × numbers. `doorman render` generates all of it
// from this file, the way it already generates the phone plant from
// handsets.toml. The symmetry is the point — same inventory shape, same
// secrets rule, same "generated files are outputs" rule.
//
// trunks.toml is optional and its absence is the compatibility gate: no file
// means nothing is generated, the hand-written pjsip.conf keeps working, and a
// single-provider install notices nothing at all. That is every install that
// exists today.
//
// Secrets are named here, never written here. `password_env` holds the name of
// a .env variable and `doorman render` substitutes the value at render time —
// the same rule handsets.toml has always had. This loader goes one step
// further than that one and refuses a value that does not look like a variable
// name, so a password pasted into the field fails loudly instead of being
// committed to a repository.

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// TrunkFile is the raw shape of trunks.toml.
type TrunkFile struct {
	// EmergencyTrunk designates which trunk carries 911. It lives here rather
	// than in a policy file because it is a property of the inventory, not of
	// one line: whoever grabs the nearest handset has no idea which line they
	// are on, and E911 is registered per DID against a street address. Unset is
	// never undefined — it falls back to the primary line's trunk, and every
	// tool that prints it says which of the two happened.
	EmergencyTrunk string  `toml:"emergency_trunk"`
	Trunks         []Trunk `toml:"trunks"`
}

// Trunk is one provider registration, as written in trunks.toml. The compiled
// form is the same struct with defaults filled in, so `doorman render` and
// `doorman check` never have to remember what an empty field means.
type Trunk struct {
	// ID names the PJSIP endpoint and every object generated beside it —
	// <id>_auth, <id>_reg, <id>_aor. It is what [line] trunk references, and
	// it is also what the outbound dial string in the dialplan already says
	// (PJSIP/1${EXTEN}@voipms). That is the bridge from a hand-written trunk to
	// a generated one: keep the id and the dialplan needs no edit at all.
	ID string `toml:"id"`
	// Provider is the company, for display. Nothing routes on it — it is what
	// makes a table of short ids readable in `doorman check`.
	Provider string `toml:"provider"`
	// Host is the POP or SIP host this box registers with, optionally with a
	// port. Use the same host everywhere: mixing POPs between the registration
	// and the AOR causes flapping that looks like a network problem.
	Host string `toml:"host"`
	// Username is the sub-account this box registers as — never the main
	// account login. A compromised Pi should cost a sub-account, not a balance
	// and a DID.
	Username string `toml:"username"`
	// PasswordEnv names the .env variable holding the registration password.
	// The secret itself never appears in this file.
	PasswordEnv string `toml:"password_env"`
	// Context is the inbound dialplan context calls on this trunk land in.
	// Defaults to from-<id>. One context per trunk is the load-bearing half of
	// supporting several providers: each registration binds to its own endpoint
	// with its own context, so one [inbound-trunk] becomes one per provider.
	Context string `toml:"context"`
	// Codecs are the endpoint's allow= lines, in order. Defaults to ulaw then
	// g722 — the pair that needs no transcoding on a Pi.
	Codecs []string `toml:"codecs"`
	// FromUser and FromDomain override what this box puts in From. They default
	// to the username and the host, which is what a registration trunk almost
	// always wants; providers that authenticate as one name and expect another
	// in From are the reason these exist.
	FromUser   string `toml:"from_user"`
	FromDomain string `toml:"from_domain"`
	// Expiration is the registration lifetime in seconds; defaults to 300.
	// Some providers throttle short ones, which is why it is a knob.
	Expiration int `toml:"expiration"`
	// E911 records whether a street address is registered against this trunk's
	// DIDs. It is the one fact the emergency-trunk decision turns on and the
	// one doorman cannot discover for itself, so it is declared rather than
	// inferred. Unset means unknown, and `doorman check` says "unknown" rather
	// than guessing either way.
	E911 *bool `toml:"e911"`
}

// DefaultTrunkCodecs is what a trunk allows when it says nothing. ulaw first
// because every provider speaks it, g722 second because it is free quality on
// a call that never leaves the box's own codec.
var DefaultTrunkCodecs = []string{"ulaw", "g722"}

// DefaultTrunkExpiration is the registration lifetime, in seconds, when a
// trunk does not choose one.
const DefaultTrunkExpiration = 300

// Trunks is a validated trunks.toml: every provider this box registers with,
// defaults filled in, plus which of them carries 911.
//
// A nil *Trunks and an absent one are both usable and mean different things.
// Nil is "the caller did not consult the inventory" — the daemon's case, since
// nothing routes on a trunk yet. Absent (Present() == false) is "there is no
// trunks.toml", which is what `doorman check` needs to tell a line naming a
// trunk that the file it should be in does not exist.
type Trunks struct {
	// Path is the file these came from, for messages. Empty when they were
	// compiled from bytes rather than read from disk, which is the language
	// server's case and every test's.
	Path string

	present   bool
	list      []Trunk
	byID      map[string]Trunk
	emergency string
}

// Present reports whether there is a trunks.toml at all. Deliberately not
// "declares at least one trunk": a file that exists and a file that does not
// are different answers to "why is this [line] trunk unknown", with different
// fixes.
func (t *Trunks) Present() bool { return t != nil && t.present }

// Where names the file for a message, falling back to the conventional name
// when the inventory did not come from disk.
func (t *Trunks) Where() string {
	if t == nil || t.Path == "" {
		return "trunks.toml"
	}
	return t.Path
}

// Len is how many trunks were declared.
func (t *Trunks) Len() int {
	if t == nil {
		return 0
	}
	return len(t.list)
}

// All returns every trunk in file order. File order is preserved rather than
// sorted because it is the order an operator wrote them in and the order every
// generated block and printed table follows — and, deliberately, because
// nothing safety-critical depends on it: the emergency trunk is named or
// inherited from policy.toml, never "whichever sorts first".
func (t *Trunks) All() []Trunk {
	if t == nil {
		return nil
	}
	return t.list
}

// Lookup resolves a trunk id.
func (t *Trunks) Lookup(id string) (Trunk, bool) {
	if t == nil {
		return Trunk{}, false
	}
	tr, ok := t.byID[id]
	return tr, ok
}

// IDs lists every declared trunk id, sorted, for "did you mean" style messages
// and for the editor's completions.
func (t *Trunks) IDs() []string {
	if t == nil {
		return nil
	}
	out := make([]string, 0, len(t.list))
	for _, tr := range t.list {
		out = append(out, tr.ID)
	}
	sort.Strings(out)
	return out
}

// Designated is the explicit emergency_trunk, or "" when nobody set one.
func (t *Trunks) Designated() string {
	if t == nil {
		return ""
	}
	return t.emergency
}

// Emergency answers which trunk a 911 call leaves by, and whether that was
// chosen or inferred.
//
// The rule, from .plans/s01-multiple-DIDs: an explicit designation wins;
// unset, it is the primary line's trunk — policy.toml, the unsuffixed file,
// which is already the default for everything unqualified. Both chains bottom
// out in the same place, so "which line am I on when I have not said" has one
// answer rather than two that can drift apart. Nothing here depends on file
// order or on where a block sits in a file, which is the whole reason the
// primary line is named rather than picked.
//
// An empty id means neither was set. That is a real state — a box with trunks
// whose policy.toml names none — and the callers report it rather than
// inventing an answer.
func (t *Trunks) Emergency(primaryLineTrunk string) (id string, chosen bool) {
	if t == nil {
		return "", false
	}
	if t.emergency != "" {
		return t.emergency, true
	}
	return primaryLineTrunk, false
}

var (
	trunkHostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:\d{1,5})?$`)
	envVarPattern    = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	codecPattern     = regexp.MustCompile(`^[a-z0-9]+$`)
)

// reservedTrunkSuffixes are the object names render derives from an id. A
// trunk called "voipms_auth" would generate a block that collides with the one
// belonging to a trunk called "voipms", and PJSIP would load whichever it read
// last with no complaint worth reading.
var reservedTrunkSuffixes = []string{"_auth", "_reg", "_aor"}

// LoadTrunks reads and compiles trunks.toml.
//
// A missing file is not an error: it is the default state of every install
// that predates this, and it compiles to an absent inventory that generates
// nothing and validates nothing. Any other read error is returned, because a
// trunks.toml that exists and cannot be read is a different problem entirely
// and silently treating it as absent would generate a phone plant with no
// trunks in it.
func LoadTrunks(path string) (*Trunks, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Trunks{}, nil
	}
	if err != nil {
		return nil, err
	}
	t, terr := TrunksFromTOML(data)
	if terr != nil {
		return nil, terr
	}
	t.Path = path
	return t, nil
}

// TrunksFromTOML compiles trunks.toml content. The returned Trunks has an
// empty Path; LoadTrunks fills it in, which is what makes Present() mean "read
// from a file" rather than "non-empty".
func TrunksFromTOML(data []byte) (*Trunks, error) {
	f, unknown, err := decodeTrunks(data)
	if err != nil {
		return nil, err
	}
	// Unknown keys are refused outright here, unlike the daemon's policy load.
	// Nothing on the call path reads this file: its audience is an operator at
	// a terminal about to overwrite the Asterisk config, and a dropped
	// `password_env` produces a trunk that will not register with nothing at
	// runtime to suggest why. Same reasoning as LoadHandsets.
	msgs := make([]string, 0, len(unknown))
	for _, u := range unknown {
		msgs = append(msgs, u.File+": "+u.String())
	}
	t, problems := compileTrunks(f)
	msgs = append(msgs, problems...)
	if len(msgs) > 0 {
		return nil, errors.New(strings.Join(msgs, "\n"))
	}
	return t, nil
}

// LintTrunks returns every problem in trunks.toml, one string per issue; empty
// means valid. The language server consumes this, so unknown keys are among
// the problems rather than a separate error — there is a person looking at the
// result who would rather know.
func LintTrunks(data []byte) []string {
	f, unknown, err := decodeTrunks(data)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, u := range unknown {
		problems = append(problems, u.String())
	}
	_, semantic := compileTrunks(f)
	return append(problems, semantic...)
}

func compileTrunks(f TrunkFile) (*Trunks, []string) {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	t := &Trunks{present: true, byID: map[string]Trunk{}, emergency: f.EmergencyTrunk}
	if len(f.Trunks) == 0 {
		fail("at least one [[trunks]] entry is required — " +
			"delete trunks.toml entirely to go back to a hand-written pjsip.conf")
	}

	contexts := map[string]string{}
	for _, tr := range f.Trunks {
		if !handsetIDPattern.MatchString(tr.ID) {
			fail("trunk id %q must be lowercase alphanumeric/dash/underscore", tr.ID)
			continue
		}
		if _, dup := t.byID[tr.ID]; dup {
			fail("duplicate trunk id %q", tr.ID)
			continue
		}
		for _, suffix := range reservedTrunkSuffixes {
			if strings.HasSuffix(tr.ID, suffix) {
				fail("trunk id %q may not end in %q — `doorman render` derives "+
					"<id>_auth, <id>_reg and <id>_aor from the id, and this one would collide",
					tr.ID, suffix)
			}
		}

		where := fmt.Sprintf("trunk %q", tr.ID)
		if tr.Host == "" {
			fail("%s needs a host (the provider's POP, e.g. chicago.voip.ms)", where)
		} else if !trunkHostPattern.MatchString(tr.Host) {
			fail("%s host %q must be a hostname or address, optionally with a port — "+
				"not a URL and not a sip: URI", where, tr.Host)
		}
		if tr.Username == "" {
			fail("%s needs a username (the provider sub-account this box registers as)", where)
		}
		switch {
		case tr.PasswordEnv == "":
			fail("%s needs password_env (the .env variable holding its registration password)", where)
		case !envVarPattern.MatchString(tr.PasswordEnv):
			// The one rule worth being strict about in a file that must never
			// hold a secret: a password pasted here fails this pattern, so the
			// mistake is loud instead of committed.
			fail("%s password_env %q must be the NAME of a .env variable such as "+
				"TRUNK_%s_PASSWORD — never the password itself",
				where, tr.PasswordEnv, strings.ToUpper(strings.NewReplacer("-", "_").Replace(tr.ID)))
		}

		if tr.Context == "" {
			tr.Context = TrunkContext(tr.ID)
		} else if !lineName.MatchString(tr.Context) {
			fail("%s context %q must be an Asterisk context name — "+
				"letters, digits, - and _", where, tr.Context)
		}
		if owner, taken := contexts[tr.Context]; taken {
			// Two trunks sharing a context would pool their DIDs, and a DID
			// route generated for one would answer calls arriving on the other.
			fail("%s and trunk %q share the context %q — each registration needs "+
				"its own, or inbound calls cannot be told apart", where, owner, tr.Context)
		}
		contexts[tr.Context] = tr.ID

		if len(tr.Codecs) == 0 {
			tr.Codecs = append([]string(nil), DefaultTrunkCodecs...)
		} else {
			seen := map[string]bool{}
			for _, c := range tr.Codecs {
				if !codecPattern.MatchString(c) {
					fail("%s codec %q must be an Asterisk codec name such as ulaw or g722", where, c)
					continue
				}
				if seen[c] {
					fail("%s lists codec %q twice", where, c)
				}
				seen[c] = true
			}
		}

		if tr.FromUser == "" {
			tr.FromUser = tr.Username
		}
		if tr.FromDomain == "" {
			// The domain, not the host:port — a port in From is wrong even when
			// it is right in the AOR contact.
			tr.FromDomain, _, _ = strings.Cut(tr.Host, ":")
		}
		if tr.Expiration == 0 {
			tr.Expiration = DefaultTrunkExpiration
		} else if tr.Expiration < 30 || tr.Expiration > 86400 {
			fail("%s expiration %d must be between 30 and 86400 seconds", where, tr.Expiration)
		}

		t.list = append(t.list, tr)
		t.byID[tr.ID] = tr
	}

	if f.EmergencyTrunk != "" {
		if _, ok := t.byID[f.EmergencyTrunk]; !ok {
			fail("emergency_trunk %q is not one of the trunks declared here (%s) — "+
				"this is the one setting that must never be a typo",
				f.EmergencyTrunk, strings.Join(t.IDs(), ", "))
		}
	}

	if len(problems) > 0 {
		return nil, problems
	}
	return t, nil
}

// TrunkContext is the inbound dialplan context a trunk's calls land in when it
// does not name one. Derived rather than required because the name is
// bookkeeping: what matters is that each registration has its own, and one per
// trunk id guarantees that without anybody having to invent a convention.
func TrunkContext(id string) string { return "from-" + id }
