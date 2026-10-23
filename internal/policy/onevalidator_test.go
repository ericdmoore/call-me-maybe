package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validatorHandsets = "[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\n"
const validatorPolicy = "[house]\nhandsets = [\"kitchen\"]\n"

func withSchedule(fields string) string {
	return validatorPolicy + "\n[[schedules]]\nid = \"holiday\"\n" + fields + "\n"
}

// call-me-maybe#131: a disabled schedule that nothing references is still
// configuration, and an impossible time in it is refused by every load path.
func TestScheduleTimesAreValidatedEvenWhenDisabled(t *testing.T) {
	for _, c := range []struct {
		fields string
		want   string // "" means valid
	}{
		{"enabled = false\nstart = \"25:99\"\nend = \"07:00\"", `start "25:99" is not a time of day`},
		{"start = \"25:99\"\nend = \"07:00\"", `start "25:99" is not a time of day`},
		{"enabled = false\nstart = \"21:00\"\nend = \"24:00\"", `end "24:00" is not a time of day`},
		{"enabled = false\nstart = \"21:60\"\nend = \"07:00\"", `"21:60" is not a time of day`},
		{"enabled = false\nstart = \"7:00\"\nend = \"8:30\"", ""},
		{"enabled = false\nstart = \"7:60\"\nend = \"08:00\"", `"7:60" is not a time of day`},
		{"enabled = false\nstart = \"7\"\nend = \"08:00\"", `"7" must be HH:MM`},
		{"enabled = false\nend = \"08:00\"", `start "" must be HH:MM`},
		{"enabled = false\nstart = \"21:00\"\nend = \"07:00\"\ndays = [\"MO\", \"mo\"]", ""},
		{"enabled = false\nstart = \"00:00\"\nend = \"23:59\"\ndays = [\"mo\", \"TU\"]", ""},
		{"enabled = false\nstart = \"03:05\"\nend = \"07:00\"", ""},
	} {
		data := []byte(withSchedule(c.fields))
		lint := LintSplitWith(data, []byte(validatorHandsets), Options{StrictUnknownKeys: true})
		_, err := fromSplitTOML(data, []byte(validatorHandsets), Options{StrictUnknownKeys: true})
		if c.want == "" {
			if len(lint) != 0 || err != nil {
				t.Errorf("%q: refused a valid schedule: %v %v", c.fields, lint, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: load err = %v, want %q", c.fields, err, c.want)
		}
		if len(lint) == 0 || !strings.Contains(strings.Join(lint, "\n"), c.want) {
			t.Errorf("%q: lint = %q, want %q", c.fields, lint, c.want)
		}
	}
}

func TestDuplicatePINMessageDoesNotCarryThePIN(t *testing.T) {
	data := validatorPolicy + "[[extensions]]\nlabel = \"A\"\npin = \"428917\"\nhandsets = [\"kitchen\"]\n[[extensions]]\nlabel = \"B\"\npin = \"428917\"\nhandsets = [\"kitchen\"]\n"
	_, err := fromSplitTOML([]byte(data), []byte(validatorHandsets), Options{})
	if err == nil || !strings.Contains(err.Error(), "duplicate pin") || strings.Contains(err.Error(), "428917") {
		t.Fatalf("err = %v", err)
	}
}

func TestBoundsAreEnforcedOnTheLoadPath(t *testing.T) {
	long := strings.Repeat("a", 61)
	for name, data := range map[string]string{
		"id":     validatorPolicy + "[[schedules]]\nid = \"" + long + "\"\nstart = \"21:00\"\nend = \"07:00\"\n",
		"label":  "[line]\nlabel = \"" + strings.Repeat("é", 129) + "\"\n" + validatorPolicy,
		"string": validatorPolicy + "[[people]]\nname = \"N\"\nnumbers = [\"+15550100009\"]\nnotes = \"" + strings.Repeat("x", 2049) + "\"\n",
		"step":   validatorPolicy + "[[extensions]]\nlabel = \"L\"\npin = \"935172\"\n[[extensions.steps]]\nhandsets = [\"kitchen\"]\nrings = -2\nseconds = 5\n",
	} {
		if _, err := fromSplitTOML([]byte(data), []byte(validatorHandsets), Options{}); err == nil {
			t.Errorf("%s: accepted an out-of-bounds value", name)
		}
	}
	// Emoji count as the wire counts them: two UTF-16 units each.
	if textLength("😀") != 2 || textLength("é") != 1 {
		t.Fatal("textLength does not count UTF-16 units")
	}
}

func TestRedactProblem(t *testing.T) {
	for in, want := range map[string]string{
		`schedule holiday start "25:99" is not a time of day`:           `schedule holiday start "25:99" is not a time of day`,
		`number "+1 (555) 010-0123" for "Grandma" is not a valid phone`: `number "<redacted>" for "Grandma" is not a valid phone`,
		`[line] number "5550100123" is not a valid phone number`:        `[line] number "<redacted>" is not a valid phone number`,
		`pin "4289" is wrong`:                                        `pin "<redacted>" is wrong`,
		`handset "kitchen" number 250 must be 100-199`:               `handset "kitchen" number 250 must be 100-199`,
		`call 555-010-0123 back`:                                     `call <redacted> back`,
		`webhook https://ha.local/api/webhook/abc must be reachable`: `webhook <url> must be reachable`,
		"token abcdefghijklmnopqrstuvwxyz0123 leaked\n\tagain":       "token <redacted> leaked again",
		`seconds must be between 1 and 86400`:                        `seconds must be between 1 and 86400`,
	} {
		if got := RedactProblem(in, 0); got != want {
			t.Errorf("RedactProblem(%q) = %q, want %q", in, got, want)
		}
	}
	long := RedactProblem(strings.Repeat("é", 400), MaxProblemLength)
	if textLength(long) != MaxProblemLength || !strings.HasSuffix(long, "…") {
		t.Fatalf("not bounded: %d", textLength(long))
	}
}

// The daemon's half: a reload the shared rules refuse is reported and the
// previous policy stays in service (invariant 4).
func TestStoreKeepsLastGoodWhenADisabledScheduleIsInvalid(t *testing.T) {
	dir := t.TempDir()
	policyPath, handsetsPath := filepath.Join(dir, "policy.toml"), filepath.Join(dir, "handsets.toml")
	good := withSchedule("enabled = false\nstart = \"03:05\"\nend = \"07:00\"")
	os.WriteFile(policyPath, []byte(good), 0o600)
	os.WriteFile(handsetsPath, []byte(validatorHandsets), 0o600)
	failed := make(chan error, 4)
	reloaded := make(chan *Policy, 4)
	store, err := OpenStoreWith(policyPath, handsetsPath, true, Options{StrictUnknownKeys: true, TrunksPath: filepath.Join(dir, "trunks.toml")},
		func(p *Policy) { reloaded <- p }, func(err error) { failed <- err })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before := store.Current()
	os.WriteFile(policyPath, []byte(withSchedule("enabled = false\nstart = \"25:99\"\nend = \"07:00\"")), 0o600)
	select {
	case err := <-failed:
		if !strings.Contains(err.Error(), `"25:99" is not a time of day`) {
			t.Fatalf("reload error = %v", err)
		}
	case <-reloaded:
		t.Fatal("the daemon loaded a file the shared validator refuses")
	case <-time.After(4 * time.Second):
		t.Fatal("invalid reload was never reported")
	}
	if store.Current() != before {
		t.Fatal("last good policy was replaced")
	}
}

// [line] trunk is checked against trunks.toml on disk at every reload, so an
// edit to that file alone re-validates the policy.
func TestStoreRevalidatesWhenTrunksChange(t *testing.T) {
	dir := t.TempDir()
	policyPath, handsetsPath, trunksPath := filepath.Join(dir, "policy.toml"), filepath.Join(dir, "handsets.toml"), filepath.Join(dir, "trunks.toml")
	os.WriteFile(policyPath, []byte("[line]\ntrunk = \"second\"\n"+validatorPolicy), 0o600)
	os.WriteFile(handsetsPath, []byte(validatorHandsets), 0o600)
	trunk := func(id string) string {
		return "[[trunks]]\nid = \"" + id + "\"\nhost = \"sip.example.net\"\nusername = \"box\"\npassword_env = \"TRUNK_PASSWORD\"\n"
	}
	if _, err := OpenStoreWith(policyPath, handsetsPath, false, Options{StrictUnknownKeys: true, TrunksPath: trunksPath}, nil, nil); err == nil {
		t.Fatal("loaded a line naming a trunk with no trunks.toml")
	}
	os.WriteFile(trunksPath, []byte(trunk("first")+trunk("second")), 0o600)
	reloaded := make(chan *Policy, 4)
	failed := make(chan error, 4)
	store, err := OpenStoreWith(policyPath, handsetsPath, true, Options{StrictUnknownKeys: true, TrunksPath: trunksPath},
		func(p *Policy) { reloaded <- p }, func(err error) { failed <- err })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	os.WriteFile(trunksPath, []byte(trunk("first")), 0o600)
	select {
	case err := <-failed:
		if !strings.Contains(err.Error(), `trunk "second" is not declared`) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("a trunks.toml edit did not re-validate the policy")
	}
}
