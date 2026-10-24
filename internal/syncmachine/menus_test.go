package syncmachine

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// menuFixture is a front menu, a PIN-less extension it names, and a
// recording: everything #150 adds to a policy file.
const menuFixture = `
[[extensions]]
id = "orders"
label = "Orders"
handsets = ["kitchen"]

[[menus]]
id = "front"
say = "For orders, press 1."

  [[menus.choices]]
  digit = "1"
  extension = "orders"

  [[menus.choices]]
  digit = "2"
  recording = "hours"
  challenge = true
`

// The sync wire does not carry menus yet (read-only sync is a later change),
// so the observer projects them away — and must still snapshot the file, not
// refuse it, and must say a PIN-less extension has no PIN rather than drop it.
// A file with no menus projects exactly as it always has.
func TestObserverSnapshotsAPolicyWithMenus(t *testing.T) {
	m, p, _ := fixture(t)
	before, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(before)
	if !bytes.Contains(plain, []byte(`"id":"extension:0"`)) || !bytes.Contains(plain, []byte(`"label":"Family"`)) {
		t.Fatalf("a file without menus no longer projects as it did: %s", plain)
	}

	src := "[line]\nmenu = \"front\"\n" + policyFixture + menuFixture
	if err := os.WriteFile(p.Policy, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := m.Observe()
	if err != nil {
		t.Fatalf("the observer refused a policy with menus: %v", err)
	}
	if after.Revision == before.Revision {
		t.Fatal("adding an extension did not change the revision")
	}
	data, _ := json.Marshal(after)
	if !bytes.Contains(data, []byte(`"pinSet":false`)) || !bytes.Contains(data, []byte(`"label":"Orders"`)) {
		t.Fatalf("the PIN-less extension is not on the wire as one: %s", data)
	}
	if bytes.Contains(data, []byte(`"choices"`)) || bytes.Contains(data, []byte(`For orders`)) {
		t.Fatalf("menus reached a wire that has no schema for them: %s", data)
	}
}
