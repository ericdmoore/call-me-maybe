package syncmachine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/syncwire"
	"github.com/google/uuid"
)

const handsetsFixture = `# This comment belongs to the operator.
[[handsets]]
id = "kitchen"
label = "Kitchen"
endpoint = "PJSIP/kitchen"
phonebook = ["house", "people"] # keep this selection comment
`
const policyFixture = `[house]
handsets = ["kitchen"]

[[people]]
id = "grandma"
name = "Grandma"
numbers = ["+15550100003"]

[[extensions]]
label = "Family"
pin = "846213"
handsets = ["kitchen"]

# Keep the bedtime explanation.
[[schedules]]
id = "bedtime"
start = "21:00" # don't lose this comment
end = "07:00"
days = ["MO", "TU"]
`

func fixture(t *testing.T) (*Machine, Paths, string) {
	t.Helper()
	dir := t.TempDir()
	p := Paths{Policy: filepath.Join(dir, "policy.toml"), Handsets: filepath.Join(dir, "handsets.toml")}
	if os.WriteFile(p.Policy, []byte(policyFixture), 0o600) != nil || os.WriteFile(p.Handsets, []byte(handsetsFixture), 0o600) != nil {
		t.Fatal("write fixture")
	}
	state := filepath.Join(dir, "sync")
	m, err := Open(state, "ac409cd3-8105-4200-9a32-42a064a98bb0", p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, p, state
}

func queue(t *testing.T, s syncwire.Snapshot, name string) syncwire.QueueItem {
	t.Helper()
	v := map[string]any{"name": "policy.setSchedule", "policy": "default", "schedule": map[string]any{"id": "bedtime", "start": "22:00", "end": "07:00", "days": []string{"MO", "TU"}, "enabled": true}}
	if name == "phonebook.assign" {
		v = map[string]any{"name": name, "handset": "kitchen", "phonebook": "house"}
	}
	raw, _ := json.Marshal(map[string]any{"id": uuid.NewString(), "base": map[string]any{"generation": s.Generation, "revision": s.Revision}, "notAfter": time.Now().Add(time.Hour).UnixMilli(), "createdAt": time.Now().UnixMilli(), "author": map[string]any{"kind": "person", "person": uuid.NewString()}, "verb": v})
	var q syncwire.QueueItem
	if err := syncwire.Decode("QueueItem", raw, &q); err != nil {
		t.Fatal(err)
	}
	return q
}

func TestObserveKeepsLastGoodAndDoesNotEchoSecrets(t *testing.T) {
	m, p, _ := fixture(t)
	first, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(first)
	if bytes.Contains(data, []byte("846213")) || bytes.Contains(data, []byte(`"pin":`)) || !bytes.Contains(data, []byte(`"pinSet":true`)) {
		t.Fatal("unsafe credential projection")
	}
	second, err := m.Observe()
	if err != nil || first.Revision != second.Revision {
		t.Fatal("unchanged values changed the revision")
	}
	os.WriteFile(p.Policy, []byte("# additional comment\n"+policyFixture), 0o600)
	second, err = m.Observe()
	if err != nil || first.Revision != second.Revision {
		t.Fatal("comment-only change changed the revision")
	}
	os.WriteFile(p.Policy, []byte("broken = ["), 0o600)
	if _, err = m.Observe(); err == nil {
		t.Fatal("accepted invalid policy")
	}
	last, err := m.Latest()
	if err != nil || last.Revision != first.Revision {
		t.Fatal("lost last good snapshot")
	}
}

func TestContactSourceEditsEchoAndInvalidSourceKeepsLastGood(t *testing.T) {
	m, p, _ := fixture(t)
	p.Contacts = filepath.Join(filepath.Dir(p.Policy), "contacts.toml")
	m.paths = p
	cardPath := filepath.Join(filepath.Dir(p.Policy), "family.vcf")
	card := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Alice\r\nTEL:+15550100004\r\nEND:VCARD\r\n"
	for path, data := range map[string]string{
		p.Contacts: "[[sources]]\nid = \"family\"\npath = \"family.vcf\"\n",
		cardPath:   card,
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cardPath, []byte(strings.Replace(card, "Alice", "Betty", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := m.Observe()
	if err != nil || next.Revision == first.Revision {
		t.Fatal("source edit did not enter the echo")
	}
	data, _ := json.Marshal(next)
	if !bytes.Contains(data, []byte("Betty")) || bytes.Contains(data, []byte("Alice")) {
		t.Fatal("source values did not refresh")
	}
	if err := os.WriteFile(cardPath, []byte("invalid card"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Observe(); err == nil {
		t.Fatal("invalid source replaced the echo")
	}
	last, err := m.Latest()
	if err != nil || last.Revision != next.Revision {
		t.Fatal("lost last good source values")
	}
}

func TestApplyReplayAndActivation(t *testing.T) {
	m, p, _ := fixture(t)
	s, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	q := queue(t, s, "policy.setSchedule")
	r, err := m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("apply: %s %v", r.Status, err)
	}
	after, _ := os.ReadFile(p.Policy)
	for _, want := range []string{`start = "22:00"`, "# don't lose this comment", "# Keep the bedtime explanation.", `pin = "846213"`, `enabled = true`} {
		if !bytes.Contains(after, []byte(want)) {
			t.Fatalf("lost expected content %q", want)
		}
	}
	r, err = m.Apply(q)
	again, _ := os.ReadFile(p.Policy)
	if err != nil || r.Status != syncwire.ReceiptStatusSaved || !bytes.Equal(again, after) {
		t.Fatal("replay repeated a write")
	}
	m.Applied = func(Paths, map[string][]byte) bool { return true }
	r, err = m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusApplied {
		t.Fatal("did not acknowledge activation")
	}
}

func TestLocalEditWinsAndCrashAfterRenameDoesNotReplay(t *testing.T) {
	m, p, state := fixture(t)
	s, _ := m.Observe()
	q := queue(t, s, "policy.setSchedule")
	os.WriteFile(p.Policy, []byte(strings.Replace(policyFixture, `"21:00"`, `"20:30"`, 1)), 0o600)
	r, err := m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusConflict {
		t.Fatal("overwrote intervening local edit")
	}
	s, _ = m.Observe()
	q = queue(t, s, "policy.setSchedule")
	r, err = m.Apply(q)
	if err != nil {
		t.Fatal(err)
	}
	// The process died after rename but before committing the receipt.
	if _, err = m.db.Exec("UPDATE commands SET receipt=NULL WHERE id=?", q.Id); err != nil {
		t.Fatal(err)
	}
	m.Close()
	m, err = Open(state, "ac409cd3-8105-4200-9a32-42a064a98bb0", p)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	replayed, err := m.Apply(q)
	if err != nil || replayed.Status != r.Status || *replayed.Revision != *r.Revision {
		t.Fatal("did not recover prepared write")
	}
}

func TestAssignmentAndMalformedWire(t *testing.T) {
	m, p, _ := fixture(t)
	s, _ := m.Observe()
	r, err := m.Apply(queue(t, s, "phonebook.assign"))
	if err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("assignment: %s %v", r.Status, err)
	}
	after, _ := os.ReadFile(p.Handsets)
	if !bytes.Contains(after, []byte(`phonebook = ["house"] # keep this selection comment`)) {
		t.Fatal("assignment lost comment or selection")
	}
	var out syncwire.QueueItem
	if syncwire.Decode("QueueItem", []byte(`{"verb":{"name":"shell","command":"anything"}}`), &out) == nil {
		t.Fatal("accepted unknown verb")
	}
	if syncwire.Decode("QueueItem", []byte(`{} {}`), &out) == nil {
		t.Fatal("accepted multiple values")
	}
}

func TestPatchIgnoresFakeHeadersAndHandlesMultilineArrays(t *testing.T) {
	input := []byte("title = '''\n[[schedules]]\nid = \"bedtime\"\n'''\n\n[[schedules]]\nid = 'bedtime'\nstart = '21:00' # note\ndays = [\n 'MO', # Monday\n 'TU'\n] # preserve\n")
	out, err := patchTable(input, "schedules", "bedtime", map[string]any{"start": "22:30", "days": []string{"FR"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("title = '''\n[[schedules]]\nid = \"bedtime\"\n'''")) || !bytes.Contains(out, []byte(`start = "22:30" # note`)) || !bytes.Contains(out, []byte(`days = ["FR"] # preserve`)) {
		t.Fatal("TOML edit did not preserve surrounding syntax")
	}
}
