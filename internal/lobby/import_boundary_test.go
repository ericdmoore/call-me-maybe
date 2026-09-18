package lobby

import (
	"os/exec"
	"strings"
	"testing"
)

func TestLobbyCannotReadJournal(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range strings.Fields(string(out)) {
		if name == "callmemaybe/internal/events" || name == "database/sql" || strings.HasPrefix(name, "modernc.org/") {
			t.Fatalf("call state machine depends on journal storage: %s", name)
		}
	}
}
