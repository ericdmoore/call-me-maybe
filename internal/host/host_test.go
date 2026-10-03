package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeExec records every command and answers `id -u` for the users it
// knows; nothing touches the real host.
type fakeExec struct {
	users map[string]bool
	ran   [][]string
	// enabled/active answer systemctl is-enabled / is-active.
	enabled, active map[string]bool
}

func (f *fakeExec) Run(_ context.Context, argv ...string) (string, error) {
	f.ran = append(f.ran, argv)
	switch {
	case argv[0] == "id":
		if f.users[argv[2]] {
			return "1000\n", nil
		}
		return "", os.ErrNotExist
	case argv[0] == "systemctl" && argv[1] == "is-enabled":
		if f.enabled[argv[2]] {
			return "enabled\n", nil
		}
		return "disabled\n", os.ErrNotExist
	case argv[0] == "systemctl" && argv[1] == "is-active":
		if f.active[argv[2]] {
			return "active\n", nil
		}
		return "inactive\n", os.ErrNotExist
	case argv[0] == "install" && len(argv) > 2 && argv[1] != "-d":
		// install SRC DST: copy, as the real one would.
		src, dst := argv[len(argv)-2], argv[len(argv)-1]
		b, err := os.ReadFile(src)
		if err != nil {
			return "", err
		}
		return "", os.WriteFile(dst, b, 0o644)
	}
	return "", nil
}

func (f *fakeExec) count(prefix ...string) int {
	n := 0
	for _, r := range f.ran {
		if len(r) >= len(prefix) && strings.Join(r[:len(prefix)], " ") == strings.Join(prefix, " ") {
			n++
		}
	}
	return n
}

func layout(t *testing.T) Layout {
	d := t.TempDir()
	return Layout{UnitDir: filepath.Join(d, "units"), StateDir: filepath.Join(d, "state"), Spool: filepath.Join(d, "spool"),
		DoormanUser: "doorman", DoormanGroup: "doorman", AsteriskUser: "asterisk"}
}

func TestEveryEmbeddedUnitHasARuleAndTheDaemonComesFirst(t *testing.T) {
	units, err := Units()
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 11 || units[0].Name != "doorman.service" {
		t.Fatalf("units = %d, first %s", len(units), units[0].Name)
	}
	for _, u := range units {
		if u.Timer != strings.HasSuffix(u.Name, ".timer") {
			t.Errorf("%s timer flag wrong", u.Name)
		}
		if !strings.Contains(string(u.Content), "ExecStart=") && !u.Timer {
			t.Errorf("%s has no ExecStart", u.Name)
		}
	}
}

// First run installs everything and enables what the config wants; a
// second run with nothing changed does nothing but reload and re-enable.
func TestPlanInstallsEnablesAndIsIdempotent(t *testing.T) {
	lay := layout(t)
	os.MkdirAll(lay.UnitDir, 0o755)
	x := &fakeExec{users: map[string]bool{"asterisk": true}}
	w := Wants{Inbox: true, Why: map[string]string{"doorman-inbox.service": "INBOX_URL and messages.toml", "doorman-digest.timer": "no mail", "doorman-balance.timer": "no trunks", "doorman-directory.service": "no address"}}
	ctx := context.Background()

	p, err := Build(ctx, x, lay, w)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, x, p); err != nil {
		t.Fatal(err)
	}
	if n := x.count("install", "-d"); n != 7 {
		t.Errorf("directories created = %d, want 7 (six state dirs + the spool)", n)
	}
	if n := x.count("systemctl", "enable", "--now"); n != 3 {
		t.Errorf("enabled = %d, want the daemon, the *88 timer and the inbox: %v", n, p.Enable)
	}
	if n := x.count("systemctl", "disable", "--now"); n != 4 {
		t.Errorf("disabled = %d, want directory, digest, balance, backup: %v", n, p.Disable)
	}
	unit, err := os.ReadFile(filepath.Join(lay.UnitDir, "doorman-inbox.service"))
	if err != nil || !strings.HasPrefix(string(unit), managedMarker) || !strings.Contains(string(unit), "ExecStart=") {
		t.Fatalf("installed unit: %v\n%s", err, unit)
	}

	// Rerun: every unit is current; nothing is written again.
	y := &fakeExec{users: map[string]bool{"asterisk": true}}
	p2, err := Build(ctx, y, lay, w)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p2.Steps {
		if strings.HasPrefix(s.Describe, "install unit") || strings.HasPrefix(s.Describe, "replace unit") {
			t.Errorf("rerun wants to write %q", s.Describe)
		}
	}
	if err := Apply(ctx, y, p2); err != nil {
		t.Fatal(err)
	}
	if n := y.count("install", "-o", "root"); n != 0 {
		t.Errorf("rerun wrote %d units", n)
	}
}

func TestAnOutOfDateManagedUnitIsReplacedAndAForeignOneIsLeft(t *testing.T) {
	lay := layout(t)
	os.MkdirAll(lay.UnitDir, 0o755)
	os.WriteFile(filepath.Join(lay.UnitDir, "doorman.service"), []byte(managedMarker+"[Service]\nExecStart=/old\n"), 0o644)
	os.WriteFile(filepath.Join(lay.UnitDir, "doorman-inbox.service"), []byte("[Service]\nExecStart=/mine\n"), 0o644)
	// Units the shell installer copied before the marker existed: ours only
	// when byte-identical to something a release shipped. The timer is a
	// real legacy copy (registered by hash below); the directory unit runs
	// our binary but was edited by the operator, so it is theirs.
	legacyTimer := []byte("[Unit]\nDescription=Call Me Maybe — the morning digest\n[Timer]\nOnCalendar=daily\n")
	sum := sha256.Sum256(legacyTimer)
	legacyUnits["doorman-digest.timer"] = append(legacyUnits["doorman-digest.timer"], hex.EncodeToString(sum[:]))
	os.WriteFile(filepath.Join(lay.UnitDir, "doorman-digest.timer"), legacyTimer, 0o644)
	os.WriteFile(filepath.Join(lay.UnitDir, "doorman-directory.service"), []byte("[Service]\nUser=custom-user\nWorkingDirectory=/srv/my-phone\nExecStart=/usr/local/bin/doorman provision directory\n"), 0o644)
	x := &fakeExec{users: map[string]bool{"asterisk": true}}
	w := Wants{Inbox: true, Why: map[string]string{}}
	p, err := Build(context.Background(), x, lay, w)
	if err != nil {
		t.Fatal(err)
	}
	var replaced, left, adopted int
	for _, s := range p.Steps {
		if strings.HasPrefix(s.Describe, "replace unit doorman.service") {
			replaced++
		}
		if strings.HasPrefix(s.Describe, "replace unit doorman-digest.timer") {
			adopted++
		}
		if strings.HasPrefix(s.Describe, "replace unit doorman-directory.service") {
			t.Error("an operator's edited unit must not be adopted just because it runs our binary")
		}
		if strings.HasPrefix(s.Describe, "leave ") {
			left++
		}
	}
	sort.Strings(p.Foreign)
	if replaced != 1 || left != 2 || adopted != 1 || len(p.Foreign) != 2 || p.Foreign[0] != "doorman-directory.service" || p.Foreign[1] != "doorman-inbox.service" {
		t.Errorf("replaced=%d left=%d adopted=%d foreign=%v — only a byte-identical shipped copy is ours to adopt", replaced, left, adopted, p.Foreign)
	}
	for _, e := range p.Enable {
		if e == "doorman-inbox.service" {
			t.Error("a foreign unit must not be enabled or disabled by us")
		}
	}
	if err := Apply(context.Background(), x, p); err != nil {
		t.Fatal(err)
	}
	mine, _ := os.ReadFile(filepath.Join(lay.UnitDir, "doorman-inbox.service"))
	if string(mine) != "[Service]\nExecStart=/mine\n" {
		t.Error("the foreign unit was touched")
	}
	ours, _ := os.ReadFile(filepath.Join(lay.UnitDir, "doorman.service"))
	if strings.Contains(string(ours), "/old") {
		t.Error("the out-of-date managed unit was not replaced")
	}
}

func TestTheBackupReadACLsAreGrantedWhereAsteriskLives(t *testing.T) {
	lay := layout(t)
	os.MkdirAll(lay.UnitDir, 0o755)
	lay.AsteriskDir = filepath.Join(t.TempDir(), "etc")
	lay.VoicemailDir = filepath.Join(t.TempDir(), "spool", "voicemail")
	os.MkdirAll(lay.AsteriskDir, 0o755)
	os.MkdirAll(lay.VoicemailDir, 0o755)
	os.WriteFile(filepath.Join(lay.AsteriskDir, "pjsip.conf"), []byte("x"), 0o640)
	x := &fakeExec{users: map[string]bool{"asterisk": true}}
	p, err := Build(context.Background(), x, lay, Wants{Why: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	acl, chmod := 0, 0
	for _, s := range p.Steps {
		if len(s.Argv) > 0 && s.Argv[0] == "setfacl" {
			acl++
		}
		if len(s.Argv) > 0 && (s.Argv[0] == "chmod" || s.Argv[0] == "chown") {
			chmod++
		}
	}
	if acl != 4 || chmod != 0 {
		t.Errorf("setfacl steps = %d (want the dir, pjsip.conf, the spool parent, the spool), chmod/chown = %d (want none): %+v", acl, chmod, p.Steps)
	}
}

func TestNoAsteriskUserMeansTheSpoolWaits(t *testing.T) {
	lay := layout(t)
	os.MkdirAll(lay.UnitDir, 0o755)
	x := &fakeExec{users: map[string]bool{}}
	p, err := Build(context.Background(), x, lay, Wants{Why: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range p.Steps {
		if strings.HasPrefix(s.Describe, "skip ") && strings.Contains(s.Describe, "no user asterisk") {
			found = true
		}
		if len(s.Argv) > 0 && s.Argv[len(s.Argv)-1] == lay.Spool {
			t.Error("the spool must not be created without its owner")
		}
	}
	if !found {
		t.Error("the plan should say the spool waits")
	}
}

func TestInspectReportsWantAgainstHost(t *testing.T) {
	lay := layout(t)
	os.MkdirAll(lay.UnitDir, 0o755)
	os.WriteFile(filepath.Join(lay.UnitDir, "doorman.service"), []byte(managedMarker+"x"), 0o644)
	x := &fakeExec{enabled: map[string]bool{"doorman.service": true}, active: map[string]bool{"doorman.service": true, "doorman-inbox.service": true}}
	st, err := Inspect(context.Background(), x, lay, Wants{Inbox: true, Why: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Status{}
	for _, s := range st {
		by[s.Name] = s
	}
	if d := by["doorman.service"]; !d.Want || !d.Enabled || !d.Active || !d.Present {
		t.Errorf("daemon = %+v", d)
	}
	if i := by["doorman-inbox.service"]; !i.Want || i.Enabled || !i.Active || i.Present {
		t.Errorf("inbox = %+v (active but not enabled, not installed)", i)
	}
	if _, has := by["doorman-phonebook.service"]; has {
		t.Error("a timer's service is not its own status line")
	}
}

func TestEveryUnitHasShippedLegacyContentRegistered(t *testing.T) {
	units, _ := Units()
	for _, u := range units {
		if u.Name == "doorman-backup.service" || u.Name == "doorman-backup.timer" {
			continue // born with the marker; never shipped under scripts/
		}
		if len(legacyUnits[u.Name]) == 0 {
			t.Errorf("%s has no legacy hash: an installer-era copy could never be adopted", u.Name)
		}
	}
}
