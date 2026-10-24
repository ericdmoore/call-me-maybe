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
