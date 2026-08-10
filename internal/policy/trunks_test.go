package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const oneTrunk = `
[[trunks]]
id = "voipms"
provider = "voip.ms"
host = "chicago.voip.ms"
username = "123456_home"
password_env = "TRUNK_VOIPMS_PASSWORD"
`

func mustTrunks(t *testing.T, body string) *Trunks {
	t.Helper()
	tr, err := TrunksFromTOML([]byte(body))
	if err != nil {
		t.Fatalf("TrunksFromTOML: %v", err)
	}
	return tr
}

func TestTrunksFillInTheDefaultsRenderNeeds(t *testing.T) {
	tr, ok := mustTrunks(t, oneTrunk).Lookup("voipms")
	if !ok {
		t.Fatal("voipms is missing")
	}
	// Defaults are resolved at compile time so that render and check never
	// have to remember what an empty field means.
	if tr.Context != "from-voipms" {
		t.Errorf("context = %q, want from-voipms", tr.Context)
	}
	if strings.Join(tr.Codecs, ",") != "ulaw,g722" {
		t.Errorf("codecs = %v", tr.Codecs)
	}
	if tr.FromUser != "123456_home" || tr.FromDomain != "chicago.voip.ms" {
		t.Errorf("from = %q@%q", tr.FromUser, tr.FromDomain)
	}
	if tr.Expiration != DefaultTrunkExpiration {
		t.Errorf("expiration = %d", tr.Expiration)
	}
}

// A port belongs in the AOR contact and the registration URIs, never in From.
func TestTrunkFromDomainDropsThePort(t *testing.T) {
	tr, _ := mustTrunks(t, `
[[trunks]]
id = "odd"
host = "sip.example.com:5061"
username = "u"
password_env = "TRUNK_ODD_PASSWORD"
`).Lookup("odd")
	if tr.FromDomain != "sip.example.com" {
		t.Errorf("from_domain = %q, want the host without its port", tr.FromDomain)
	}
}

// The one rule that keeps a secret out of a committed file. A password pasted
// where a variable name belongs does not look like a variable name.
func TestTrunkRefusesAPasswordWherePasswordEnvBelongs(t *testing.T) {
	_, err := TrunksFromTOML([]byte(`
[[trunks]]
id = "voipms"
host = "chicago.voip.ms"
username = "123456_home"
password_env = "hunter2-not-a-variable"
`))
	if err == nil || !strings.Contains(err.Error(), "never the password itself") {
		t.Fatalf("err = %v, want a refusal naming the mistake", err)
	}
}

// Balance credentials are optional, and both halves have to be present before
// anything will try to use them.
func TestTrunkBalanceCredentialsAreOptionalAndPaired(t *testing.T) {
	plain := mustTrunks(t, oneTrunk).All()[0]
	if plain.BalanceConfigured() {
		t.Error("a trunk with no API credentials must not claim to be checkable")
	}

	full := mustTrunks(t, oneTrunk+`
api_username = "owner@example.invalid"
api_password_env = "TRUNK_VOIPMS_API_PASSWORD"
balance_min = 25.0
`).All()[0]
	if !full.BalanceConfigured() {
		t.Error("a trunk with both halves is checkable")
	}
	if full.BalanceMin != 25 {
		t.Errorf("balance_min = %v, want 25", full.BalanceMin)
	}
	// A whole number is the natural thing to write, and refusing it over the
	// missing decimal point would be a papercut in a file people hand-edit.
	whole := mustTrunks(t, oneTrunk+`
api_username = "owner@example.invalid"
api_password_env = "TRUNK_VOIPMS_API_PASSWORD"
balance_min = 25
`).All()[0]
	if whole.BalanceMin != 25 {
		t.Errorf("balance_min = %v from an integer, want 25", whole.BalanceMin)
	}
}

func TestTrunkProblems(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"no trunks", "", "at least one [[trunks]] entry"},
		{"bad id", "[[trunks]]\nid = \"VoIP MS\"\nhost = \"h\"\nusername = \"u\"\npassword_env = \"P\"\n", "must be lowercase"},
		{"duplicate id", oneTrunk + oneTrunk, "duplicate trunk id"},
		{"no host", "[[trunks]]\nid = \"a\"\nusername = \"u\"\npassword_env = \"P\"\n", "needs a host"},
		{"host is a uri", "[[trunks]]\nid = \"a\"\nhost = \"sip:h.example.com\"\nusername = \"u\"\npassword_env = \"P\"\n", "not a sip: URI"},
		{"no username", "[[trunks]]\nid = \"a\"\nhost = \"h\"\npassword_env = \"P\"\n", "needs a username"},
		{"no password_env", "[[trunks]]\nid = \"a\"\nhost = \"h\"\nusername = \"u\"\n", "needs password_env"},
		{"reserved suffix", "[[trunks]]\nid = \"voipms_reg\"\nhost = \"h\"\nusername = \"u\"\npassword_env = \"P\"\n", "may not end in"},
		{"shared context", oneTrunk + "\n[[trunks]]\nid = \"other\"\nhost = \"h\"\nusername = \"u\"\npassword_env = \"P\"\ncontext = \"from-voipms\"\n", "share the context"},
		{"bad codec", oneTrunk + "codecs = [\"G.729\"]\n", "must be an Asterisk codec"},
		{"duplicate codec", oneTrunk + "codecs = [\"ulaw\", \"ulaw\"]\n", "twice"},
		{"silly expiration", oneTrunk + "expiration = 2\n", "between 30 and 86400"},
		{"unknown emergency trunk", "emergency_trunk = \"telnyx\"\n" + oneTrunk, "is not one of the trunks"},
		{"unknown key", oneTrunk + "hostname = \"x\"\n", "unknown key"},
		{"api key pasted", oneTrunk + "api_username = \"a@b.invalid\"\napi_password_env = \"live-key-abc123\"\n", "never the key itself"},
		{"api username alone", oneTrunk + "api_username = \"a@b.invalid\"\n", "no api_password_env"},
		{"api password alone", oneTrunk + "api_password_env = \"TRUNK_VOIPMS_API_PASSWORD\"\n", "no api_username"},
		{"threshold nothing can check", oneTrunk + "balance_min = 20.0\n", "nothing can ever check it"},
		{"negative threshold", oneTrunk + "api_username = \"a@b.invalid\"\napi_password_env = \"K\"\nbalance_min = -1.0\n", "must not be negative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := TrunksFromTOML([]byte(c.body))
			if err == nil {
				t.Fatalf("expected a problem containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// The emergency trunk is the most consequential setting in the file, so its
// resolution is stated as a rule rather than left to whoever reads the code.
func TestEmergencyTrunkChosenBeatsInferred(t *testing.T) {
	designated := mustTrunks(t, "emergency_trunk = \"voipms\"\n"+oneTrunk)
	if id, chosen := designated.Emergency("something-else"); id != "voipms" || !chosen {
		t.Errorf("designated: got %q chosen=%v, want voipms chosen", id, chosen)
	}

	// Unset falls back to the primary line's trunk — policy.toml, the file
	// that is already the default for everything unqualified.
	inferred := mustTrunks(t, oneTrunk)
	if id, chosen := inferred.Emergency("voipms"); id != "voipms" || chosen {
		t.Errorf("inferred: got %q chosen=%v, want voipms inferred", id, chosen)
	}

	// And with neither there is no answer, which callers must report rather
	// than invent. Notably NOT "the only trunk declared": that default would
	// move silently the day a second block was added above it.
	if id, _ := inferred.Emergency(""); id != "" {
		t.Errorf("with nothing set, got %q — nothing may be inferred from declaration order", id)
	}
}

func TestLoadTrunksTreatsAMissingFileAsAbsent(t *testing.T) {
	tr, err := LoadTrunks(filepath.Join(t.TempDir(), "trunks.toml"))
	if err != nil {
		t.Fatalf("a missing trunks.toml is the normal state, not an error: %v", err)
	}
	if tr.Present() || tr.Len() != 0 {
		t.Error("a missing file must compile to an absent inventory")
	}
	// Nil-safety matters: the daemon passes nil into policy compilation.
	var nilTrunks *Trunks
	if nilTrunks.Present() || nilTrunks.Len() != 0 || nilTrunks.IDs() != nil {
		t.Error("a nil inventory must be usable")
	}
	if id, chosen := nilTrunks.Emergency("voipms"); id != "" || chosen {
		t.Errorf("nil inventory answered %q", id)
	}
}

func TestLoadTrunksReportsAFileItCannotCompile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trunks.toml")
	if err := os.WriteFile(path, []byte("[[trunks]]\nid = \"a\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrunks(path); err == nil {
		t.Fatal("a trunks.toml that exists and is wrong is not the same as no trunks.toml")
	}
}

// ── [line] trunk, the cross-file reference ───────────────────────────────

const trunkLineHandsets = `
[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
`

func loadLineWithTrunks(t *testing.T, line string, trunks *Trunks) error {
	t.Helper()
	_, err := fromSplitTOML([]byte(line+"\n[house]\nhandsets = [\"kitchen\"]\n"),
		[]byte(trunkLineHandsets), Options{Trunks: trunks})
	return err
}

func TestLineTrunkIsCheckedAgainstTheInventory(t *testing.T) {
	trunks := mustTrunks(t, oneTrunk)

	if err := loadLineWithTrunks(t, "[line]\ntrunk = \"voipms\"\n", trunks); err != nil {
		t.Fatalf("a line naming a declared trunk should load: %v", err)
	}

	err := loadLineWithTrunks(t, "[line]\ntrunk = \"telnyx\"\n", trunks)
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("err = %v, want the unknown reference reported", err)
	}
	if !strings.Contains(err.Error(), "voipms") {
		t.Errorf("err = %v, want it to list what IS declared", err)
	}
}

// Naming a trunk with no trunks.toml at all is a different mistake with a
// different fix, and saying "not declared in trunks.toml" would send someone
// looking for a file that is not there.
func TestLineTrunkWithNoInventorySaysSo(t *testing.T) {
	absent := &Trunks{}
	err := loadLineWithTrunks(t, "[line]\ntrunk = \"voipms\"\n", absent)
	if err == nil || !strings.Contains(err.Error(), "there is no trunks.toml") {
		t.Fatalf("err = %v, want the missing file named", err)
	}
}

// The daemon's case, and the reason Options.Trunks is a pointer. Nothing on
// the call path routes on a trunk yet, so refusing to load a policy over a
// reference nothing reads would be exactly the trade invariant 4 forbids.
func TestLineTrunkIsUncheckedWhenNoInventoryIsPassed(t *testing.T) {
	if err := loadLineWithTrunks(t, "[line]\ntrunk = \"telnyx\"\n", nil); err != nil {
		t.Fatalf("the daemon must load a policy naming an unknown trunk: %v", err)
	}
}

func TestLineTrunkSurvivesCompilation(t *testing.T) {
	p, err := fromSplitTOML([]byte("[line]\ntrunk = \"voipms\"\n[house]\nhandsets = [\"kitchen\"]\n"),
		[]byte(trunkLineHandsets), Options{Trunks: mustTrunks(t, oneTrunk)})
	if err != nil {
		t.Fatal(err)
	}
	if p.Line().Trunk != "voipms" {
		t.Errorf("compiled trunk = %q", p.Line().Trunk)
	}
}

// The compatibility gate for this key: a policy with no [line] trunk compiles
// to exactly what it compiled to before the key existed, inventory or not.
func TestPolicyWithoutATrunkIsUnaffected(t *testing.T) {
	body := []byte("[house]\nhandsets = [\"kitchen\"]\n")
	for _, trunks := range []*Trunks{nil, {}, mustTrunks(t, oneTrunk)} {
		p, err := fromSplitTOML(body, []byte(trunkLineHandsets), Options{Trunks: trunks})
		if err != nil {
			t.Fatalf("Trunks=%v: %v", trunks, err)
		}
		if p.Line().Trunk != "" {
			t.Errorf("Trunks=%v gave a line a trunk nobody wrote", trunks)
		}
	}
}

// ── each file owns its sections ──────────────────────────────────────────

func TestSectionsInTheWrongFileSayWhichFileTheyBelongIn(t *testing.T) {
	// "unknown section [[trunks]]" is true and unhelpful when trunks.toml is
	// sitting right beside the file it was written in.
	_, err := fromSplitTOML([]byte("[house]\nhandsets = [\"kitchen\"]\n\n[[trunks]]\nid = \"voipms\"\n"),
		[]byte(trunkLineHandsets), Options{StrictUnknownKeys: true})
	if err == nil || !strings.Contains(err.Error(), "belongs in trunks.toml") {
		t.Fatalf("err = %v, want the right file named", err)
	}

	_, terr := TrunksFromTOML([]byte(oneTrunk + "\n[line]\nlabel = \"Home\"\n"))
	if terr == nil || !strings.Contains(terr.Error(), "belongs in policy.toml") {
		t.Fatalf("err = %v, want the right file named", terr)
	}
}

func TestLintTrunksReportsEveryProblemAtOnce(t *testing.T) {
	problems := LintTrunks([]byte("[[trunks]]\nid = \"a\"\nhostname = \"h\"\n"))
	if len(problems) < 2 {
		t.Fatalf("problems = %v, want the unknown key and the missing host both", problems)
	}
	if LintTrunks([]byte(oneTrunk)) != nil {
		t.Error("a valid file should lint clean")
	}
	if got := LintTrunks([]byte("[[trunks]\n")); len(got) != 1 {
		t.Errorf("undecodable TOML should be one problem, got %v", got)
	}
}
