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

// The menu walker follows the story interpreter's rules, not its code: story
// brings a parser into the call path's import graph (#150, R8).
func TestLobbyDoesNotImportTheStoryInterpreter(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range strings.Fields(string(out)) {
		if name == "callmemaybe/internal/story" || name == "gopkg.in/yaml.v3" || name == "callmemaybe/internal/ari" {
			t.Fatalf("the call state machine depends on %s", name)
		}
	}
}
