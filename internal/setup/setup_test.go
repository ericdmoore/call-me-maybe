package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/policy"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Kitchen":      "kitchen",
		"Kids' Room":   "kids-room",
		"Living Room":  "living-room",
		"  Office  ":   "office",
		"Bed/Bath":     "bed-bath",
		"2nd Floor":    "2nd-floor",
		"-- weird --":  "weird",
		"Grace's Room": "grace-s-room",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvVarFor(t *testing.T) {
	if got := EnvVarFor("living-room"); got != "HANDSET_LIVING_ROOM_PASSWORD" {
		t.Errorf("got %q", got)
	}
}

// The whole point of init: what it writes must pass the same validator that
// guards the daemon, with no placeholders permitted.
func TestGeneratedConfigPassesStrictValidation(t *testing.T) {
	p, err := BuildPlan([]string{"Kitchen", "Living Room", "Kids' Room"}, DefaultPaths())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	problems := policy.LintSplit([]byte(p.PolicyTOML()), []byte(p.HandsetsTOML()))
	if len(problems) != 0 {
		t.Fatalf("generated config does not validate: %v", problems)
	}

	// Go through the real load path, not just the linter.
	dir := t.TempDir()
	pp := filepath.Join(dir, "policy.toml")
	hp := filepath.Join(dir, "handsets.toml")
	if err := WriteFile(pp, p.PolicyTOML()); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(hp, p.HandsetsTOML()); err != nil {
		t.Fatal(err)
	}
	compiled, err := policy.LoadSplit(pp, hp)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if compiled.ExtensionCount() != 4 { // one per handset plus the whole house
		t.Errorf("extensions = %d, want 4", compiled.ExtensionCount())
	}
	if compiled.PinLength != 6 {
		t.Errorf("PinLength = %d, want a uniform 6", compiled.PinLength)
	}
}

// Generated PINs are credentials. They must satisfy the floor, be unique, and
// never be the shipped sentinel.
func TestGeneratedPINsAreUsableCredentials(t *testing.T) {
	p, err := BuildPlan([]string{"a", "b", "c", "d", "e"}, DefaultPaths())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	seen := map[string]bool{}
	all := append([]string{p.HousePIN}, p.VoicemailPINs[p.Mailbox])
	for _, pin := range p.ExtensionPINs {
		all = append(all, pin)
	}
	for _, pin := range all {
		if len(pin) < policy.MinPINLength {
			t.Errorf("pin %d digits, below the floor of %d", len(pin), policy.MinPINLength)
		}
		if pin == policy.PlaceholderPIN {
			t.Error("generated the placeholder sentinel")
		}
		for _, r := range pin {
			if r < '0' || r > '9' {
				t.Errorf("pin contains a non-digit: %q", r)
			}
		}
		if seen[pin] {
			t.Errorf("duplicate pin generated: two extensions would collide")
		}
		seen[pin] = true
	}
}

func TestSecretsAreDistinctPerHandset(t *testing.T) {
	p, err := BuildPlan([]string{"kitchen", "office", "study"}, DefaultPaths())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	seen := map[string]bool{}
	for id, pw := range p.HandsetPasswords {
		if len(pw) < 18 {
			t.Errorf("%s password is only %d characters", id, len(pw))
		}
		if seen[pw] {
			t.Errorf("%s reuses another handset's password", id)
		}
		seen[pw] = true
	}
	if p.ARIPassword == "" || seen[p.ARIPassword] {
		t.Error("ARI password missing or shared with a handset")
	}
}

func TestBuildPlanRejectsBadInput(t *testing.T) {
	if _, err := BuildPlan(nil, DefaultPaths()); err == nil {
		t.Error("no handsets should be an error")
	}
	if _, err := BuildPlan([]string{"Kitchen", "kitchen"}, DefaultPaths()); err == nil {
		t.Error("two rooms slugging to the same id should be an error")
	}
	if _, err := BuildPlan([]string{"!!!"}, DefaultPaths()); err == nil {
		t.Error("a room name with no usable characters should be an error")
	}
	many := make([]string, 120)
	for i := range many {
		many[i] = "room" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if _, err := BuildPlan(many, DefaultPaths()); err == nil {
		t.Error("exceeding the 101-199 internal number range should be an error")
	}
}

// The .env example ships handset variables for rooms this install may not
// have. Left empty they make `doorman render` fail on a handset nobody owns.
func TestEnvFileSubstitutesAndPrunes(t *testing.T) {
	p, err := BuildPlan([]string{"Kitchen"}, DefaultPaths())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	base := strings.Join([]string{
		"ARI_PASSWORD=change-me-to-something-long",
		"HANDSET_KITCHEN_PASSWORD=",
		"HANDSET_OFFICE_PASSWORD=",
		"LOG_FORMAT=json",
	}, "\n") + "\n"

	out := p.EnvFile(base)
	if strings.Contains(out, "change-me-to-something-long") {
		t.Error("the ARI placeholder survived")
	}
	if !strings.Contains(out, "HANDSET_KITCHEN_PASSWORD="+p.HandsetPasswords["kitchen"]) {
		t.Error("the kitchen password was not substituted")
	}
	if !strings.Contains(out, "HANDSET_KITCHEN_ADMIN_PASSWORD="+p.HandsetAdminPasswords["kitchen"]) ||
		!strings.Contains(out, "HANDSET_KITCHEN_PROVISION_PASSWORD="+p.HandsetProvisionPasswords["kitchen"]) {
		t.Error("the provisioning secrets should be written beside the SIP password")
	}
	if strings.Contains(out, "HANDSET_OFFICE_PASSWORD=") {
		t.Error("an empty variable for a handset that does not exist was left behind")
	}
	if !strings.Contains(out, "LOG_FORMAT=json") {
		t.Error("unrelated settings should be preserved")
	}
}

func TestWriteFileIsPrivateAndBackupPreserves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.toml")

	if err := WriteFile(path, "first"); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// policy.toml carries PINs; .env carries every password in the house.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}

	bak, err := Backup(path)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if bak == "" {
		t.Fatal("an existing file should have been backed up")
	}
	data, err := os.ReadFile(bak)
	if err != nil || string(data) != "first" {
		t.Errorf("backup did not preserve the original: %v %q", err, data)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the original should have been moved aside")
	}

	// Nothing to back up is not an error.
	if b, err := Backup(filepath.Join(dir, "absent.toml")); err != nil || b != "" {
		t.Errorf("backup of a missing file: %q %v", b, err)
	}
}

func TestAdminSecretSatisfiesPhonePasswordRules(t *testing.T) {
	for i := 0; i < 20; i++ {
		s, err := AdminSecret()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyz") || !strings.ContainsAny(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") || !strings.ContainsAny(s, "0123456789") {
			t.Fatalf("AdminSecret %q lacks a class a phone's web UI insists on", s)
		}
	}
	if AdminEnvVarFor("living-room") != "HANDSET_LIVING_ROOM_ADMIN_PASSWORD" || ProvisionEnvVarFor("living-room") != "HANDSET_LIVING_ROOM_PROVISION_PASSWORD" {
		t.Error("env var naming")
	}
}

func TestEnvFileTurnsTheJournalOnOnlyWhereThePlacesExist(t *testing.T) {
	p, err := BuildPlan([]string{"Kitchen"}, DefaultPaths())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	base := "# journal\n# EVENT_JOURNAL_PATH=/var/lib/doorman/journal/events.db\n# capture\n# CEL_SPOOL_PATH=/var/log/asterisk/master.db\n"

	// A workstation: nothing to point at, the two lines stay as shipped
	// (the secrets are appended below them, as always).
	if out := p.EnvFile(base); !strings.HasPrefix(out, base) {
		t.Errorf("without paths the example's lines should be untouched:\n%s", out)
	}
	// A prepared host: uncommented in place, comment kept beside the value.
	p.JournalPath = "/var/lib/doorman/journal/events.db"
	p.CELSpoolPath = "/var/log/asterisk/master.db"
	out := p.EnvFile(base)
	for _, want := range []string{
		"# journal\nEVENT_JOURNAL_PATH=/var/lib/doorman/journal/events.db\n",
		"# capture\nCEL_SPOOL_PATH=/var/log/asterisk/master.db\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "# EVENT_JOURNAL_PATH") || strings.Contains(out, "# CEL_SPOOL_PATH") {
		t.Errorf("the commented lines should have been replaced, not duplicated:\n%s", out)
	}
	// Journal only: the spool line stays commented rather than pointing at
	// a file that does not exist.
	p.CELSpoolPath = ""
	if out := p.EnvFile(base); !strings.Contains(out, "# CEL_SPOOL_PATH=") {
		t.Errorf("CEL_SPOOL_PATH should stay commented without a spool:\n%s", out)
	}
}
