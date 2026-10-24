package syncmachine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/configcheck"
	"callmemaybe/internal/syncwire"
)

// disabledSchedule is the file found live on a box (call-me-maybe#131): a
// schedule that is switched off and that nothing references, with a start
// that is not a time of day.
func disabledSchedule(start string) string {
	return policyFixture + fmt.Sprintf(`
[[schedules]]
id = "holiday"
enabled = false
start = %q
end = "07:00"
`, start)
}

// oneTrunk is a trunk with nothing secret in it: the password is named, as
// trunks.toml always names it, never written.
const oneTrunk = `[[trunks]]
id = "voipms"
host = "h"
username = "u"
password_env = "P"
`

func statusTransport(t *testing.T, statuses *[]syncwire.Status) Transport {
	links := map[string]any{"self": map[string]any{"href": "/v1/sync/"}, "up": map[string]any{"href": "/v1/"}}
	return func(_ context.Context, endpoint string, body []byte) ([]byte, error) {
		var result any
		switch endpoint {
		case "status":
			var status syncwire.Status
			if err := syncwire.Decode("Status", body, &status); err != nil {
				t.Fatalf("status report fails the wire schema: %v", err)
			}
			*statuses = append(*statuses, status)
			result = map[string]any{"status": "accepted", "_links": links}
		case "snapshot":
			var s syncwire.Snapshot
			if err := syncwire.Decode("Snapshot", body, &s); err != nil {
				t.Fatal(err)
			}
			result = map[string]any{"status": "accepted", "generation": s.Generation, "revision": s.Revision, "_links": links}
		case "queue":
			result = map[string]any{"items": []any{}, "pollAfterMs": 5000, "_links": links}
		default:
			t.Fatalf("unexpected endpoint %s", endpoint)
		}
		return json.Marshal(result)
	}
}

func TestRejectedFileIsLoggedOnceReportedInStatusAndCleared(t *testing.T) {
	m, p, _ := fixture(t)
	var logged []string
	m.Warn = func(err error) { logged = append(logged, err.Error()) }
	m.SampleStatus = func() syncwire.Status {
		return syncwire.Status{Schema: 1, Version: "test", SampledAt: time.Now().UnixMilli()}
	}
	var statuses []syncwire.Status
	transport := statusTransport(t, &statuses)

	good, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Policy, []byte(disabledSchedule("25:99")), 0o600); err != nil {
		t.Fatal(err)
	}
	// The watcher looks every second; a broken file is one log line.
	for range 5 {
		_, err := m.Observe()
		var r *Rejection
		if !errors.As(err, &r) || !errors.Is(err, ErrFiles) {
			t.Fatalf("observe err = %v, want a rejection", err)
		}
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "policy.toml") || !strings.Contains(logged[0], `"25:99"`) {
		t.Fatalf("logged %q, want one line naming the file and the time", logged)
	}
	rejection := m.Rejection()
	if rejection == nil || rejection.File != "policy.toml" || rejection.At <= 0 ||
		!strings.Contains(rejection.Message, `schedule holiday start "25:99" is not a time of day`) {
		t.Fatalf("rejection = %+v", rejection)
	}
	if last, err := m.Latest(); err != nil || last.Revision != good.Revision {
		t.Fatal("a rejected file replaced the last good revision")
	}
	if err := m.Exchange(context.Background(), transport); err != nil {
		t.Fatal(err)
	}
	got := statuses[len(statuses)-1].ConfigRejection
	if got == nil || *got != *rejection {
		t.Fatalf("status configRejection = %+v, want %+v", got, rejection)
	}
	body, _ := json.Marshal(statuses[len(statuses)-1])
	if !strings.Contains(string(body), `"configRejection":{"at":`) || !strings.Contains(string(body), `"file":"policy.toml"`) {
		t.Fatalf("wire shape changed: %s", body)
	}

	// A different broken content is a new rejection, logged once more.
	if err := os.WriteFile(p.Policy, []byte(disabledSchedule("7:60")), 0o600); err != nil {
		t.Fatal(err)
	}
	m.Observe()
	m.Observe()
	if len(logged) != 2 || !strings.Contains(m.Rejection().Message, `"7:60" is not a time of day`) {
		t.Fatalf("logged %q, rejection %+v", logged, m.Rejection())
	}

	// The fix: the observer accepts at once and the field goes away.
	if err := os.WriteFile(p.Policy, []byte(disabledSchedule("03:05")), 0o600); err != nil {
		t.Fatal(err)
	}
	fixed, err := m.Observe()
	if err != nil || fixed.Revision == good.Revision {
		t.Fatalf("valid edit not recorded: %v", err)
	}
	if m.Rejection() != nil {
		t.Fatal("rejection outlived a valid configuration")
	}
	if err := m.Exchange(context.Background(), transport); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(statuses[len(statuses)-1])
	if statuses[len(statuses)-1].ConfigRejection != nil || strings.Contains(string(body), "configRejection") {
		t.Fatalf("status still carries a rejection: %s", body)
	}
	if len(logged) != 3 || !strings.Contains(logged[2], "accepted again") {
		t.Fatalf("logged %q", logged)
	}
}

func TestRejectionNeverCarriesANumberOrAPIN(t *testing.T) {
	const pin, number = "846213", "5550100777123"
	for name, body := range map[string]string{
		"unparseable number": strings.Replace(policyFixture, `numbers = ["+15550100003"]`, `numbers = ["+1 555 0100 777123 99999"]`, 1),
		"duplicate pin": policyFixture + `
[[extensions]]
label = "Again"
pin = "846213"
handsets = ["kitchen"]
`,
		"webhook token": policyFixture + `
[[actions]]
id = "garage"
webhook = "http://ha.local/api/webhook/s3cr3t-t0ken-value-0123456789"
people = ["nobody-here"]
`,
		"line number": "[line]\nnumber = \"+1 (555) 010-0777-99999\"\n" + policyFixture,
	} {
		t.Run(name, func(t *testing.T) {
			m, p, _ := fixture(t)
			var logged []string
			m.Warn = func(err error) { logged = append(logged, err.Error()) }
			if err := os.WriteFile(p.Policy, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Observe(); err == nil {
				t.Fatal("accepted an invalid file")
			}
			r := m.Rejection()
			if r == nil || r.File != "policy.toml" || len(logged) != 1 {
				t.Fatalf("rejection %+v, logged %q", r, logged)
			}
			for _, leak := range []string{pin, number, "0100", "0777", "777123", "99999", "s3cr3t", "0123456789", "ha.local"} {
				if strings.Contains(r.Message, leak) || strings.Contains(logged[0], leak) {
					t.Fatalf("%q leaked into %q / %q", leak, r.Message, logged[0])
				}
			}
			if len([]rune(r.Message)) > 300 || r.Message == "" {
				t.Fatalf("message %q", r.Message)
			}
		})
	}
}

func TestRejectionMessageIsBounded(t *testing.T) {
	m, p, _ := fixture(t)
	var b strings.Builder
	b.WriteString(policyFixture)
	for i := range 40 {
		fmt.Fprintf(&b, "\n[[schedules]]\nid = \"broken-%d\"\nenabled = false\nstart = \"99:99\"\nend = \"07:00\"\n", i)
	}
	os.WriteFile(p.Policy, []byte(b.String()), 0o600)
	m.Observe()
	r := m.Rejection()
	if r == nil || len(r.Message) == 0 || !strings.HasSuffix(r.Message, "…") {
		t.Fatalf("rejection %+v", r)
	}
	var s syncwire.Status
	body, _ := json.Marshal(syncwire.Status{Schema: 1, Version: "t", SampledAt: 1, ConfigRejection: r})
	if err := syncwire.Decode("Status", body, &s); err != nil {
		t.Fatalf("a long rejection does not fit the wire: %v", err)
	}
}

// The observer and the shared validator — the one `doorman check` and the
// daemon use — must agree on every file. Each case is a spelling where they
// used to differ (call-me-maybe#131), or a bound that used to exist only in
// the wire schema.
func TestObserverAcceptsExactlyWhatTheSharedValidatorAccepts(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	handsets := func(n int) string {
		var b strings.Builder
		b.WriteString(handsetsFixture)
		for i := range n {
			fmt.Fprintf(&b, "\n[[handsets]]\nid = \"h%d\"\nendpoint = \"PJSIP/h%d\"\n", i, i)
		}
		return b.String()
	}
	numbers := func(n int) string {
		var out []string
		for i := range n {
			out = append(out, fmt.Sprintf(`"+1555010%04d"`, i))
		}
		return strings.Join(out, ", ")
	}
	type files struct{ policy, handsets, trunks string }
	sched := func(extra string) files {
		return files{policy: policyFixture + "\n[[schedules]]\nid = \"x\"\n" + extra + "\n", handsets: handsetsFixture}
	}
	pol := func(extra string) files {
		return files{policy: policyFixture + "\n" + extra + "\n", handsets: handsetsFixture}
	}
	for _, c := range []struct {
		name  string
		f     files
		valid bool
	}{
		{"disabled schedule 25:99", sched("enabled = false\nstart = \"25:99\"\nend = \"07:00\""), false},
		{"enabled schedule 25:99", sched("start = \"25:99\"\nend = \"07:00\""), false},
		{"disabled schedule missing start", sched("enabled = false\nend = \"07:00\""), false},
		{"one-digit hour", sched("start = \"7:00\"\nend = \"8:30\""), true},
		{"one-digit hour, bad minutes", sched("start = \"7:60\"\nend = \"08:00\""), false},
		{"three-digit hour", sched("start = \"007:00\"\nend = \"08:00\""), false},
		{"midnight and 23:59", sched("start = \"00:00\"\nend = \"23:59\""), true},
		{"lower-case day", sched("start = \"21:00\"\nend = \"07:00\"\ndays = [\"mo\", \"Tu\"]"), true},
		{"repeated day", sched("start = \"21:00\"\nend = \"07:00\"\ndays = [\"MO\", \"mo\", \"MO\", \"TU\", \"WE\", \"TH\", \"FR\", \"SA\", \"SU\"]"), true},
		{"unknown day", sched("start = \"21:00\"\nend = \"07:00\"\ndays = [\"XX\"]"), false},
		{"schedule id of 61", pol("[[schedules]]\nid = \"" + long(61) + "\"\nstart = \"21:00\"\nend = \"07:00\""), false},
		{"schedule id of 60", pol("[[schedules]]\nid = \"" + long(60) + "\"\nstart = \"21:00\"\nend = \"07:00\""), true},
		{"empty voicemail", pol("[[extensions]]\nlabel = \"Spare\"\npin = \"935172\"\nhandsets = [\"kitchen\"]\nvoicemail = \"\""), true},
		{"empty handsets beside steps", pol("[[extensions]]\nlabel = \"Ladder\"\npin = \"935172\"\nhandsets = []\n[[extensions.steps]]\nhandsets = [\"kitchen\"]\nseconds = 10"), true},
		{"empty afterhours_ring", pol("[[extensions]]\nlabel = \"Spare\"\npin = \"935172\"\nhandsets = [\"kitchen\"]\nafterhours_ring = []"), true},
		{"zero rings beside seconds", pol("[[extensions]]\nlabel = \"Ladder\"\npin = \"935172\"\n[[extensions.steps]]\nhandsets = [\"kitchen\"]\nrings = 0\nseconds = 10"), true},
		{"negative rings", pol("[[extensions]]\nlabel = \"Ladder\"\npin = \"935172\"\n[[extensions.steps]]\nhandsets = [\"kitchen\"]\nrings = -1\nseconds = 10"), false},
		{"extension label of 129", pol("[[extensions]]\nlabel = \"" + long(129) + "\"\npin = \"935172\"\nhandsets = [\"kitchen\"]"), false},
		{"extension label of 128", pol("[[extensions]]\nlabel = \"" + long(128) + "\"\npin = \"935172\"\nhandsets = [\"kitchen\"]"), true},
		{"empty confirm", pol("[[actions]]\nid = \"hello\"\nreply = \"Hello\"\npeople = [\"grandma\"]\nconfirm = \"\""), true},
		{"empty person id", files{policy: strings.Replace(policyFixture, `id = "grandma"`, `id = ""`, 1), handsets: handsetsFixture}, true},
		{"person with 33 numbers", pol("[[people]]\nname = \"Many\"\nnumbers = [" + numbers(33) + "]"), false},
		{"person with 32 numbers", pol("[[people]]\nname = \"Many\"\nnumbers = [" + numbers(32) + "]"), true},
		{"person name of 257", pol("[[people]]\nname = \"" + long(257) + "\"\nnumbers = [\"+15550100009\"]"), false},
		{"line label of 129", files{policy: "[line]\nlabel = \"" + long(129) + "\"\n" + policyFixture, handsets: handsetsFixture}, false},
		{"unknown key", pol("[house.extra]\nx = 1"), false},
		// #150: sync carries menus read-only later; until then it must not
		// refuse a file that has them, and refuses exactly what check does.
		{"menu with a PIN-less extension", files{policy: "[line]\nmenu = \"front\"\non_no_input = \"voicemail\"\nchallenge_no_input = true\n" +
			strings.Replace(policyFixture, "[house]\nhandsets = [\"kitchen\"]\n", "[house]\nhandsets = [\"kitchen\"]\nvoicemail = \"family\"\n", 1) +
			"\n[challenge]\nfactors = [2, 9]\n" + menuFixture, handsets: handsetsFixture}, true},
		{"menu naming an unknown extension", files{policy: "[line]\nmenu = \"front\"\n" + policyFixture +
			strings.Replace(menuFixture, `extension = "orders"`, `extension = "ghost"`, 1), handsets: handsetsFixture}, false},
		{"PIN-less extension no menu names", files{policy: policyFixture + "\n[[extensions]]\nid = \"orders\"\nlabel = \"Orders\"\nhandsets = [\"kitchen\"]\n", handsets: handsetsFixture}, false},
		{"string of 2049", pol("[[people]]\nname = \"Note\"\nnumbers = [\"+15550100009\"]\nnotes = \"" + long(2049) + "\""), false},
		{"line trunk without trunks.toml", files{policy: "[line]\ntrunk = \"voipms\"\n" + policyFixture, handsets: handsetsFixture}, false},
		{"handset number 0", files{policy: policyFixture, handsets: handsetsFixture + "number = 0\n"}, true},
		{"handset label of 129", files{policy: policyFixture, handsets: strings.Replace(handsetsFixture, `label = "Kitchen"`, `label = "`+long(129)+`"`, 1)}, false},
		{"handset id of 61", files{policy: policyFixture, handsets: handsetsFixture + "\n[[handsets]]\nid = \"" + long(61) + "\"\nendpoint = \"PJSIP/x\"\n"}, false},
		{"101 handsets", files{policy: policyFixture, handsets: handsets(100)}, false},
		{"100 handsets", files{policy: policyFixture, handsets: handsets(99)}, true},
		{"mac with doubled separators", files{policy: policyFixture, handsets: handsetsFixture + "mac = \"00::0b:82:01:02:03\"\nmodel = \"grandstream-wp826\"\n"}, true},
		{"unknown model", files{policy: policyFixture, handsets: handsetsFixture + "mac = \"000b82010203\"\nmodel = \"acme-phone\"\n"}, false},
		{"trunks with empty codecs and zero expiration", files{policy: "[line]\ntrunk = \"voipms\"\n" + policyFixture, handsets: handsetsFixture,
			trunks: oneTrunk + "codecs = []\nexpiration = 0\n"}, true},
		{"trunk balance_min nan", files{policy: policyFixture, handsets: handsetsFixture,
			trunks: oneTrunk + "api_username = \"owner@example.invalid\"\napi_password_env = \"P\"\nbalance_min = nan\n"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			p := Paths{Policy: filepath.Join(dir, "policy.toml"), Handsets: filepath.Join(dir, "handsets.toml"), Trunks: filepath.Join(dir, "trunks.toml")}
			os.WriteFile(p.Policy, []byte(c.f.policy), 0o600)
			os.WriteFile(p.Handsets, []byte(c.f.handsets), 0o600)
			var trunks []byte
			if c.f.trunks != "" {
				trunks = []byte(c.f.trunks)
				os.WriteFile(p.Trunks, trunks, 0o600)
			}
			problems := configcheck.Problems([]byte(c.f.policy), []byte(c.f.handsets), trunks)
			_, err := observe(p)
			if (len(problems) == 0) != c.valid {
				t.Fatalf("shared validator: valid=%v, want %v (%q)", len(problems) == 0, c.valid, problems)
			}
			if (err == nil) != c.valid {
				t.Fatalf("observer: err=%v, want valid=%v", err, c.valid)
			}
			var r *Rejection
			if err != nil && !errors.As(err, &r) {
				t.Fatalf("observer refused without a reason: %v", err)
			}
			if r != nil && strings.Contains(r.Message, "sync cannot represent") {
				t.Fatalf("the validator accepted what the wire cannot carry: %s", r.Message)
			}
		})
	}
}
