package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rotateFixture = `# House policy — the marker comment below must survive rotation.
[house]
handsets = ["kitchen", "office"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[handsets]]
id = "office"
endpoint = "PJSIP/office"

# MARKER: comments survive
[[extensions]]
pin = "428917"  # was 111111 once
label = "Kitchen"
handsets = ["kitchen"]

[[extensions]]
pin = "310244"
label = "Office"
handsets = ["office"]

[[extensions]]
pin = "902118"
label = "Shop"
handsets = ["office"]
enabled = false
`

func writeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(path, []byte(rotateFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRotateAllChangesEveryPinIncludingDisabled(t *testing.T) {
	path := writeFixture(t)

	rot, err := RotatePins(path, nil)
	if err != nil {
		t.Fatalf("RotatePins: %v", err)
	}
	if len(rot.Changes) != 3 {
		t.Fatalf("rotated %d, want 3 (disabled extensions are still credentials)", len(rot.Changes))
	}

	after, _ := os.ReadFile(path)
	text := string(after)
	for _, old := range []string{`pin = "428917"`, `pin = "310244"`, `pin = "902118"`} {
		if strings.Contains(text, old) {
			t.Errorf("old pin line still present: %s", old)
		}
	}
	for _, c := range rot.Changes {
		if len(c.NewPIN) != 6 {
			t.Errorf("%s: new pin %q is not 6 digits", c.Label, c.NewPIN)
		}
		if !strings.Contains(text, `pin = "`+c.NewPIN+`"`) {
			t.Errorf("%s: new pin not written to file", c.Label)
		}
	}

	// The file must still be a valid policy afterwards.
	if _, err := Load(path); err != nil {
		t.Fatalf("rotated file no longer valid: %v", err)
	}
}

func TestRotatePreservesCommentsAndUnrelatedLines(t *testing.T) {
	path := writeFixture(t)
	if _, err := RotatePins(path, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	for _, keep := range []string{
		"# MARKER: comments survive",
		"# was 111111 once",
		"# House policy — the marker comment below must survive rotation.",
	} {
		if !strings.Contains(string(after), keep) {
			t.Errorf("lost during rotation: %q", keep)
		}
	}
}

func TestRotateByLabelTouchesOnlyThatExtension(t *testing.T) {
	path := writeFixture(t)

	rot, err := RotatePins(path, []string{"kitchen"}) // case-insensitive
	if err != nil {
		t.Fatal(err)
	}
	if len(rot.Changes) != 1 || rot.Changes[0].Label != "Kitchen" {
		t.Fatalf("changes = %+v", rot.Changes)
	}

	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), `pin = "428917"`) {
		t.Error("Kitchen pin unchanged")
	}
	for _, untouched := range []string{`pin = "310244"`, `pin = "902118"`} {
		if !strings.Contains(string(after), untouched) {
			t.Errorf("unrelated pin was changed: %s", untouched)
		}
	}
}

func TestRotateUnknownLabelFailsAndTouchesNothing(t *testing.T) {
	path := writeFixture(t)
	before, _ := os.ReadFile(path)

	if _, err := RotatePins(path, []string{"Garage"}); err == nil {
		t.Fatal("expected error for unknown label")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("file modified despite the error")
	}
}

func TestRotateRefusesAnInvalidStartingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.toml")
	broken := strings.Replace(rotateFixture, `pin = "310244"`, `pin = "428917"`, 1) // duplicate
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RotatePins(path, nil); err == nil {
		t.Fatal("expected refusal to rotate an invalid policy")
	}
}

func TestRotatePreservesFileMode(t *testing.T) {
	path := writeFixture(t)
	if _, err := RotatePins(path, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 — rotation must not loosen credential file permissions", info.Mode().Perm())
	}
}

func TestRotateDoesNotEditPinsQuotedInComments(t *testing.T) {
	path := writeFixture(t)
	if _, err := RotatePins(path, []string{"Kitchen"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	// The inline comment "# was 111111 once" quotes a pin-looking string; the
	// anchored pattern must never have touched it.
	if !strings.Contains(string(after), "# was 111111 once") {
		t.Error("comment containing a pin-like string was modified")
	}
}
