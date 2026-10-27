package configcheck

import (
	"strings"
	"testing"
)

const handsets = "[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\n"

func TestProblems(t *testing.T) {
	ok := "[house]\nhandsets = [\"kitchen\"]\n"
	if p := Problems([]byte(ok), []byte(handsets), nil); len(p) != 0 {
		t.Fatalf("valid files refused: %q", p)
	}
	// No trunks.toml is an absent inventory, not an unchecked reference.
	if p := Problems([]byte("[line]\ntrunk = \"t\"\n"+ok), []byte(handsets), nil); len(p) == 0 {
		t.Fatal("a line naming a trunk with no trunks.toml was accepted")
	}
	trunks := []byte("[[trunks]]\nid = \"t\"\nhost = \"h\"\nusername = \"u\"\npassword_env = \"P\"\n")
	if p := Problems([]byte("[line]\ntrunk = \"t\"\n"+ok), []byte(handsets), trunks); len(p) != 0 {
		t.Fatalf("declared trunk refused: %q", p)
	}
	if p := Problems([]byte(ok), []byte(handsets), []byte("[[trunks]]\nid = \"t\"\n")); len(p) != 1 || !strings.HasPrefix(p[0], "trunks: ") {
		t.Fatalf("invalid trunks.toml: %q", p)
	}
	if p := Problems([]byte(ok+"typo = 1\n"), []byte(handsets), nil); len(p) == 0 {
		t.Fatal("unknown key accepted")
	}
	if p := Problems([]byte(ok), []byte(handsets+"model = \"acme\"\nmac = \"000b82010203\"\n"), nil); len(p) == 0 {
		t.Fatal("unknown model accepted")
	}
}
