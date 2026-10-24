package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/policy"
)

// `doorman check` on a public line: the challenge, and the clips it needs.

// pointSounds aims the clip checks at dir for one test.
func pointSounds(t *testing.T, dir string) {
	t.Helper()
	saved := soundsDir
	t.Cleanup(func() { soundsDir = saved })
	soundsDir = dir
}

// installClips writes empty stand-ins for clips under the sounds tree.
func installClips(t *testing.T, prefix string, clips ...string) {
	t.Helper()
	dir := filepath.Join(soundsDir, prefix)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, clip := range clips {
		if err := os.WriteFile(filepath.Join(dir, clip+".wav"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const menuLine = `
[line]
label       = "Blue Door Bakery"
menu        = "front"
on_no_input = "voicemail"
prompts     = "bluedoor"

[house]
handsets  = ["kitchen"]
voicemail = "bakery"

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[extensions]]
id        = "orders"
label     = "Orders"
handsets  = ["kitchen"]
voicemail = "orders"

[[extensions]]
id        = "baker"
label     = "The baker"
handsets  = ["kitchen"]

[[extensions]]
label    = "Old lobby PIN"
pin      = "428917"
handsets = ["kitchen"]

[[menus]]
id  = "front"
say = "Thanks for calling Blue Door Bakery."

  [[menus.choices]]
  digit     = "1"
  extension = "orders"

  [[menus.choices]]
  digit     = "2"
  recording = "hours"

  [[menus.choices]]
  digit     = "3"
  extension = "baker"
  challenge = true

  [[menus.choices]]
  digit = "4"
  menu  = "more"

[[menus]]
id = "more"

  [[menus.choices]]
  digit     = "1"
  voicemail = "general"

  [[menus.choices]]
  digit = "0"
  menu  = "front"

[[menus]]
id = "orphan"

  [[menus.choices]]
  digit     = "1"
  recording = "never"
`

// The R11 done-when: check describes the menu, and which choices are behind
// the challenge — and which are not, as plainly.
func TestCheckDescribesTheMenu(t *testing.T) {
	pol, err := policy.FromTOML([]byte(menuLine))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	pointSounds(t, t.TempDir())
	installClips(t, "bluedoor/menus", "front")
	installClips(t, "call-me-maybe/challenge", "explain", "what-is", "times", "right", "try-again",
		"0", "1", "2", "3", "4", "5", "6", "7", "8", "9")

	out := capture(t, func() { describeLine("./policy.bakery.toml", pol, false) })
	for _, want := range []string{
		"front menu           : front",
		"PIN lobby never runs",
		"skip the menu and the challenge",
		`Menu "front" (the front menu), clip bluedoor/menus/front`,
		`"Thanks for calling Blue Door Bakery."`,
		"1  extension orders",
		"Orders: rings PJSIP/kitchen",
		"then mailbox orders — no challenge",
		"2  recording hours",
		"back to menu front",
		"3  extension baker",
		"behind the challenge",
		"4  menu more",
		`Menu "more" (one level down)`,
		"takes a message in mailbox general — no challenge",
		"0  menu front",
		"*  repeats the menu",
		`plays it 1 more time, then on_no_input: mailbox "bakery"`,
		"6 plays or 3m0s a call",
		`Menu "orphan" (not reachable`,
		`! menu "orphan" is not reachable`,
		`! extension "Old lobby PIN" has a pin, but this line answers with menu "front"`,
		"(none — reached only through a menu)",
		"id           orders",
		"challenge            : \"what is A times B\", factors 2–9",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check should say %q:\n%s", want, out)
		}
	}
	// On the box: the menu clip is there, the recording is not; the orphan's
	// recording is nobody's business.
	if !strings.Contains(out, "has no recordings/hours, menus/more.") || strings.Contains(out, "no recordings/never") {
		t.Errorf("want a warning for recordings/hours and none for an unreachable menu:\n%s", out)
	}
	if strings.Contains(out, "challenge clips are not all installed") {
		t.Errorf("warned about installed challenge clips:\n%s", out)
	}
	if strings.Contains(out, "428917") {
		t.Fatal("check printed a PIN")
	}
}

// challenge_no_input is said in the same breath as the disposition it guards,
// with who it shuts out — and the clips are looked for only where there is a
// sounds tree to look in.
func TestCheckDescribesTheChallengeOnSilence(t *testing.T) {
	pol, err := policy.FromTOML([]byte(`
[line]
on_no_input = "voicemail"
challenge_no_input = true

[challenge]
factors = [3, 7]

[house]
handsets = ["kitchen"]
voicemail = "main"

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
`))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}

	pointSounds(t, filepath.Join(t.TempDir(), "absent"))
	out := capture(t, func() { describeLine("./policy.biz.toml", pol, false) })
	for _, want := range []string{"behind the challenge", "rotary phone", `"main" mailbox`, "factors 3–7", "forgives no PIN failure"} {
		if !strings.Contains(out, want) {
			t.Errorf("the description should mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not all installed") {
		t.Errorf("warned about clips with no sounds tree to look in:\n%s", out)
	}

	// On the box, with all but one of the set installed: a warning naming
	// what is missing, never a failure.
	pointSounds(t, t.TempDir())
	installClips(t, "call-me-maybe/challenge",
		"explain", "what-is", "times", "right", "try-again", "0", "1", "2", "3", "4", "5", "6", "7", "8")
	out = capture(t, func() { describeLine("./policy.biz.toml", pol, false) })
	if !strings.Contains(out, "not all installed") || !strings.Contains(out, "has no 9") {
		t.Errorf("want a warning naming the missing clip 9:\n%s", out)
	}
	installClips(t, "call-me-maybe/challenge", "9")
	out = capture(t, func() { describeLine("./policy.biz.toml", pol, false) })
	if strings.Contains(out, "not all installed") {
		t.Errorf("warned with every clip installed:\n%s", out)
	}
}
