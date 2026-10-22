package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreReloadsEditsWithUnchangedSizeAndMtime(t *testing.T) {
	dir := t.TempDir()
	policyPath, handsetsPath := filepath.Join(dir, "policy.toml"), filepath.Join(dir, "handsets.toml")
	pdata := []byte("[house]\nhandsets = [\"kitchen\"]\n")
	hdata := []byte("[[handsets]]\nid = \"kitchen\"\nlabel = \"Kitchen\"\nendpoint = \"PJSIP/kitchen\"\n")
	for path, data := range map[string][]byte{policyPath: pdata, handsetsPath: hdata} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reloaded := make(chan *Policy, 2)
	store, err := OpenStore(policyPath, handsetsPath, true, func(p *Policy) { reloaded <- p }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err := os.Stat(handsetsPath)
	if err != nil {
		t.Fatal(err)
	}
	next := []byte(strings.Replace(string(hdata), "Kitchen", "Counter", 1))
	if err := os.WriteFile(handsetsPath, next, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(handsetsPath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-reloaded:
		if p.SourceDigest() != SourceDigest(pdata, next) {
			t.Fatal("reload evidence does not identify the edited bytes")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("same-size edit with preserved mtime did not activate")
	}
}
