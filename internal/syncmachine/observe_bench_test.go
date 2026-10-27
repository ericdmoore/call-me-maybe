package syncmachine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The watcher runs one observe pass a second. These measure a pass, which is
// the sync process's idle CPU: at 1 Hz, ns/op divided by 1e9 is the share of a
// core it uses doing nothing.
func benchPaths(b *testing.B, cards int) Paths {
	b.Helper()
	dir := b.TempDir()
	p := Paths{Policy: filepath.Join(dir, "policy.toml"), Handsets: filepath.Join(dir, "handsets.toml")}
	os.WriteFile(p.Policy, []byte(policyFixture), 0o600)
	os.WriteFile(p.Handsets, []byte(handsetsFixture), 0o600)
	if cards > 0 {
		p.Contacts = filepath.Join(dir, "contacts.toml")
		os.WriteFile(p.Contacts, []byte("[[sources]]\nid = \"family\"\npath = \"family.vcf\"\n"), 0o600)
		var sb strings.Builder
		for i := 0; i < cards; i++ {
			fmt.Fprintf(&sb, "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Person %d\r\nTEL:+1555%07d\r\nEND:VCARD\r\n", i, 100000+i)
		}
		os.WriteFile(filepath.Join(dir, "family.vcf"), []byte(sb.String()), 0o600)
	}
	return p
}

func BenchmarkObserveSmall(b *testing.B) {
	p := benchPaths(b, 0)
	for i := 0; i < b.N; i++ {
		if _, err := observe(p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkObserve500Cards(b *testing.B) {
	p := benchPaths(b, 500)
	for i := 0; i < b.N; i++ {
		if _, err := observe(p); err != nil {
			b.Fatal(err)
		}
	}
}
