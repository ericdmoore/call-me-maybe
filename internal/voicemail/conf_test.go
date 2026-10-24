package voicemail

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeConf(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The shipped example, plus the generated file it #tryincludes: a box
// rendered from handsets.toml is found through the include, in the context
// the (+) appends to.
func TestLookupBoxFollowsTheGeneratedInclude(t *testing.T) {
	dir := t.TempDir()
	example, err := os.ReadFile("../../asterisk/voicemail.conf.example")
	if err != nil {
		t.Fatal(err)
	}
	conf := writeConf(t, dir, "voicemail.conf", string(example))
	writeConf(t, dir, "voicemail_handsets.conf", "; GENERATED\n[household](+)\nmaster-bed => 918273,Master bedroom,caroline@example.com\nkitchen => 102938,Kitchen\n")

	b, ok, err := LookupBox(conf, "household", "master-bed")
	if err != nil || !ok || !reflect.DeepEqual(b.Emails, []string{"caroline@example.com"}) || b.FullName != "Master bedroom" {
		t.Fatalf("master-bed = %+v, %v, %v", b, ok, err)
	}
	if b, ok, _ := LookupBox(conf, "household", "kids"); !ok || b.Emails[0] != "kids@example.com" {
		t.Errorf("hand-written kids = %+v, %v", b, ok)
	}
	if b, ok, _ := LookupBox(conf, "household", "kitchen"); !ok || len(b.Emails) != 0 {
		t.Errorf("a box with no email = %+v, %v; want found, no address", b, ok)
	}
	// The hunt's boxes are commented out in the example.
	if _, ok, _ := LookupBox(conf, "household", "6"); ok {
		t.Error("a commented-out line was read")
	}
	if _, ok, _ := LookupBox(conf, "other", "kids"); ok {
		t.Error("a box was found in a context that does not declare it")
	}
}

func TestLookupBoxParsesDefensively(t *testing.T) {
	dir := t.TempDir()
	writeConf(t, dir, "extra.conf", "[biz]\nsales = 4242,Sales Line,sales@example.com|owner@example.com,pager@example.com,attach=no\n")
	writeConf(t, dir, "glob-a.conf", "[biz]\nglobbed => 1,Globbed,g@example.com\n")
	conf := writeConf(t, dir, "voicemail.conf", strings.Join([]string{
		"[general]",
		"format = wav49|wav",
		"family => 9999,not a mailbox,general@example.com ; [general] is not a box context",
		"[zonemessages]",
		"eastern=America/New_York|'vm-received' Q 'digits/at' IMp",
		"[household]",
		"; family => 1111,Commented,commented@example.com",
		"family => 4242,Family \\; Friends,family@example.com ; trailing comment",
		";-- a block comment",
		"kids => 1,Block,block@example.com",
		"--;",
		"twice => 1,First,first@example.com",
		"twice => 2,Second,second@example.com",
		"bad => 1,Bad,-oProxyCommand=evil@example.com",
		"spaces => 1,Spaces,not an address",
		"#exec /bin/false",
		`#include "extra.conf"`,
		"#tryinclude missing.conf",
		"#include glob-*.conf",
		"#include voicemail.conf",
	}, "\n"))

	cases := []struct {
		ctx, box string
		ok       bool
		emails   []string
		name     string
	}{
		{"household", "family", true, []string{"family@example.com"}, "Family ; Friends"},
		{"household", "kids", false, nil, ""},
		{"household", "twice", true, []string{"second@example.com"}, "Second"},
		{"household", "bad", true, nil, "Bad"},
		{"household", "spaces", true, nil, "Spaces"},
		{"biz", "sales", true, []string{"sales@example.com", "owner@example.com"}, "Sales Line"},
		{"biz", "globbed", true, []string{"g@example.com"}, "Globbed"},
		{"general", "family", true, []string{"general@example.com"}, "not a mailbox"},
	}
	for _, c := range cases {
		b, ok, err := LookupBox(conf, c.ctx, c.box)
		if err != nil || ok != c.ok || !reflect.DeepEqual(b.Emails, c.emails) || b.FullName != c.name {
			t.Errorf("%s@%s = %+v, %v, %v; want ok=%v emails=%v name=%q", c.box, c.ctx, b, ok, err, c.ok, c.emails, c.name)
		}
	}
	if _, _, err := LookupBox(filepath.Join(dir, "absent.conf"), "household", "family"); err == nil {
		t.Error("an unreadable top-level file must be an error, so the caller says why it fell back")
	}
}

// The PIN is the first field, and Box has nowhere to keep it.
func TestABoxNeverCarriesThePIN(t *testing.T) {
	dir := t.TempDir()
	conf := writeConf(t, dir, "voicemail.conf", "[household]\nfamily => 864213,Family,family@example.com\n")
	b, ok, _ := LookupBox(conf, "household", "family")
	if !ok {
		t.Fatal("not found")
	}
	if s := strings.Join([]string{b.Context, b.Mailbox, b.FullName, strings.Join(b.Emails, ",")}, " "); strings.Contains(s, "864213") {
		t.Errorf("the PIN came out of the parser: %q", s)
	}
	if b.Refused != 0 {
		t.Errorf("Refused = %d for a line with one good address", b.Refused)
	}
	// Refused is a count of addresses, the only number Box holds.
	if n := reflect.TypeOf(Box{}).NumField(); n != 5 {
		t.Errorf("Box has %d fields; adding one is how a PIN would get in — keep it to what a mail needs", n)
	}
}

func TestReadEnvFileReadsWhatAShellWouldSource(t *testing.T) {
	dir := t.TempDir()
	p := writeConf(t, dir, "cmm-mail.env", strings.Join([]string{
		"# the house mailbox",
		"MAIL_TO=midbury@example.com",
		"export MAIL_HOOK=/opt/call-me-maybe/scripts/mail-hook-bullmoose",
		`VOICEMAIL_LINK_BASE="http://jepsen.tail0000.ts.net:8095 http://jepsen.local:8095"`,
		"STT_MODEL='whisper-large-v3' # the big one",
		"VOICEMAIL_LINK_DAYS=7 # a week",
		"not a line",
		"BAD KEY=x",
	}, "\n"))
	got, err := ReadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"MAIL_TO":             "midbury@example.com",
		"MAIL_HOOK":           "/opt/call-me-maybe/scripts/mail-hook-bullmoose",
		"VOICEMAIL_LINK_BASE": "http://jepsen.tail0000.ts.net:8095 http://jepsen.local:8095",
		"STT_MODEL":           "whisper-large-v3",
		"VOICEMAIL_LINK_DAYS": "7",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if m, err := ReadEnvFile(filepath.Join(dir, "absent")); err != nil || len(m) != 0 {
		t.Errorf("a missing file is an empty map: %v, %v", m, err)
	}
	if _, err := ReadEnvFile(dir); err == nil {
		t.Error("a file that cannot be read must say so")
	}
}
