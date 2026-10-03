package ownbook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileRenameAndReload(t *testing.T) {
	dir := t.TempDir()
	b, err := Load(dir, "norah")
	if err != nil || len(b.Entries) != 0 {
		t.Fatalf("empty book: %+v, %v", b, err)
	}
	added := time.Date(2026, 10, 2, 21, 14, 0, 0, time.UTC)
	if !b.Upsert(Entry{UID: "norah-1759457640", Name: "(972) 555-0142", E164: "+19725550142", Added: added}) {
		t.Fatal("first filing must be new")
	}
	if b.Upsert(Entry{UID: "norah-1759457700", Name: "(972) 555-0142", E164: "+19725550142", Added: added}) {
		t.Error("the same number filed twice must not duplicate")
	}
	if err := Save(dir, b); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(Path(dir, "norah"))
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %o, want 0640", info.Mode().Perm())
	}

	again, err := Load(dir, "norah")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Entries) != 1 || again.Entries[0].Named || again.Entries[0].Name != "(972) 555-0142" || !again.Entries[0].Added.Equal(added) {
		t.Fatalf("reloaded = %+v", again.Entries)
	}
	if !again.Rename("norah-1759457640", "Maddie, from school") || again.Rename("nope", "x") {
		t.Error("rename by uid")
	}
	if err := Save(dir, again); err != nil {
		t.Fatal(err)
	}
	third, _ := Load(dir, "norah")
	if e, ok := third.Find("norah-1759457640"); !ok || e.Name != "Maddie, from school" || !e.Named {
		t.Errorf("after rename = %+v", e)
	}
	raw, _ := os.ReadFile(Path(dir, "norah"))
	if !strings.Contains(string(raw), "FN:Maddie\\, from school\r\n") || !strings.Contains(string(raw), "TEL;TYPE=cell:+19725550142\r\n") {
		t.Errorf("file is not the vCard expected:\n%s", raw)
	}
	hs, _ := Handsets(dir)
	if len(hs) != 1 || hs[0] != "norah" {
		t.Errorf("Handsets = %v", hs)
	}
	if _, err := os.Stat(filepath.Join(dir, "norah.vcf.tmp")); err == nil {
		t.Error("temp file left behind")
	}
}

func TestACorruptBookIsAnErrorNotAnEmptyBook(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(Path(dir, "grace"), []byte("BEGIN:VCARD\r\nFN:x\r\n"), 0o640)
	if _, err := Load(dir, "grace"); err == nil {
		t.Error("an unterminated file must not read as empty — that would be a silent loss")
	}
}
