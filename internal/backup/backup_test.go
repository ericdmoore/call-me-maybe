package backup

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func house(t *testing.T) (Sources, string) {
	t.Helper()
	root := t.TempDir()
	w := func(rel, content string, mode os.FileMode) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), mode)
		return p
	}
	env := w("opt/.env", "ARI_PASSWORD=secret\n", 0o600)
	pol := w("opt/policy.toml", "[house]\nhandsets = []\n", 0o644)
	w("state/provision/cert.pem", "CERT", 0o600)
	w("state/phonebook/own/norah.vcf", "BEGIN:VCARD\nEND:VCARD\n", 0o640)
	vm := w("vm/household/family/INBOX/msg0000.wav", "RIFF", 0o644)
	w("spool/norah-1-9725550142.wav", "RIFF", 0o660)
	ext := w("etc/extensions.conf", "[internal]\n", 0o640)
	pjsip := w("etc/pjsip.conf", "[voipms]\npassword=x\n", 0o640)
	// A live journal with one row, to be snapshotted.
	j := filepath.Join(root, "state", "journal", "events.db")
	os.MkdirAll(filepath.Dir(j), 0o700)
	db, err := sql.Open("sqlite", j)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE e(id INTEGER PRIMARY KEY, t TEXT); INSERT INTO e(t) VALUES ('phonebook.added')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_ = vm
	return Sources{
		Config:   []string{env, pol, filepath.Join(root, "opt", "contacts.toml")},
		StateDir: filepath.Join(root, "state"), Journal: j,
		Voicemail: filepath.Join(root, "vm"), Spool: filepath.Join(root, "spool"),
		Asterisk: []string{ext, pjsip},
		Shipped: func(path string, content []byte) (bool, bool) {
			if strings.HasSuffix(path, "extensions.conf") {
				return string(content) == "[internal]\n", true // identical to shipped: out
			}
			return false, true // pjsip differs: in, customised
		},
	}, root
}

func TestCollectBundleOpenRoundTripIsByteIdentical(t *testing.T) {
	src, _ := house(t)
	rcpt, id, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 4, 10, 10, 0, 0, time.UTC)
	items, m, err := Collect(src, "0.14.0", "jepsen", now)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]File{}
	for _, f := range m.Files {
		paths[filepath.Base(f.Path)] = f
	}
	if _, has := paths["extensions.conf"]; has {
		t.Error("a shipped-identical Asterisk file must be excluded")
	}
	if f := paths["pjsip.conf"]; f.Note != "customised" || f.Tier != "asterisk" {
		t.Errorf("pjsip.conf = %+v", f)
	}
	if f := paths["events.db"]; f.Note != "snapshot" || f.Size == 0 {
		t.Errorf("journal = %+v", f)
	}
	if f := paths[".env"]; f.Tier != "house" || f.Mode != 0o600 {
		t.Errorf(".env = %+v", f)
	}
	if len(m.Excluded) != 1 || !strings.Contains(m.Excluded[0], "identical") {
		t.Errorf("excluded = %v", m.Excluded)
	}
	if n := len(m.Files); n != 8 {
		t.Errorf("files = %d, want 8 (.env, policy, cert, own book, journal, voicemail, spool clip, customised pjsip)", n)
	}

	var buf bytes.Buffer
	if err := Bundle(&buf, m, items, rcpt); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("ARI_PASSWORD")) || bytes.Contains(buf.Bytes(), []byte("secret")) {
		t.Fatal("the bundle must not contain plaintext")
	}
	if _, _, err := Open(bytes.NewReader(buf.Bytes()), mustOtherIdentity(t)); err == nil {
		t.Fatal("another identity must not open it")
	}
	m2, items2, err := Open(bytes.NewReader(buf.Bytes()), id)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Host != "jepsen" || m2.Doorman != "0.14.0" || !m2.CreatedAt.Equal(now) || len(items2) != len(items) {
		t.Errorf("manifest back = %+v", m2)
	}
	for i := range items {
		if items[i].Path != items2[i].Path || !bytes.Equal(items[i].Content, items2[i].Content) || items[i].Mode != items2[i].Mode {
			t.Errorf("%s differs after the round trip", items[i].Path)
		}
	}
	// The snapshot is a working database with the row.
	out := t.TempDir()
	written, err := Place(items2, out)
	if err != nil || len(written) != len(items2) {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(out, src.Journal))
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM e").Scan(&n); err != nil || n != 1 {
		t.Errorf("snapshot rows = %d, %v", n, err)
	}
	db.Close()
	if !strings.Contains(m2.Summary(), "8 files") || !strings.Contains(m2.Summary(), "2 house") {
		t.Errorf("summary = %q", m2.Summary())
	}
}

func TestATamperedBundleIsRefusedWhole(t *testing.T) {
	src, _ := house(t)
	rcpt, id, _ := NewKey()
	items, m, err := Collect(src, "0.14.0", "jepsen", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Lie in the manifest about one file: the hash check must catch it.
	m.Files[0].SHA256 = strings.Repeat("0", 64)
	var buf bytes.Buffer
	if err := Bundle(&buf, m, items, rcpt); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(bytes.NewReader(buf.Bytes()), id); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("err = %v", err)
	}
}

func TestNoVoicemailIsRecordedNotSilent(t *testing.T) {
	src, _ := house(t)
	src.NoVoicemail = true
	_, m, err := Collect(src, "x", "h", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range m.Files {
		if f.Tier == "voicemail" {
			t.Error("voicemail included despite --no-voicemail")
		}
	}
	found := false
	for _, e := range m.Excluded {
		if strings.Contains(e, "--no-voicemail") {
			found = true
		}
	}
	if !found {
		t.Error("the exclusion must be in the manifest")
	}
	if Name("jepsen", time.Date(2026, 10, 4, 10, 10, 0, 0, time.UTC)) != "callmemaybe-jepsen-20261004T101000Z.age" {
		t.Error("name")
	}
}

func mustOtherIdentity(t *testing.T) string {
	_, id, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
