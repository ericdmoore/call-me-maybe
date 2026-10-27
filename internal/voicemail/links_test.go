package voicemail

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokensAre128RandomBitsInBase64URL(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := NewToken(nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(tok) != 22 || !ValidToken(tok) || strings.ContainsAny(tok, "=+/") {
			t.Fatalf("token %q", tok)
		}
		if seen[tok] {
			t.Fatal("a repeated token")
		}
		seen[tok] = true
	}
	if _, err := NewToken(bytes.NewReader([]byte{1, 2, 3})); err == nil {
		t.Error("a short read must not become a short token")
	}
}

func TestValidTokenRefusesEverythingElse(t *testing.T) {
	good, _ := NewToken(bytes.NewReader(make([]byte, 16)))
	if good != "AAAAAAAAAAAAAAAAAAAAAA" || !ValidToken(good) {
		t.Fatalf("zero token = %q", good)
	}
	for _, bad := range []string{
		"", "short", good + "A", good[:21],
		"../../../../etc/passwd", "AAAAAAAAAAAAAAAAAAAA/A", "AAAAAAAAAAAAAAAAAAAA.A",
		"AAAAAAAAAAAAAAAAAAAAA=", "AAAAAAAAAAAAAAAAAAAA+A",
		"AAAAAAAAAAAAAAAAAAAAAB", // stray trailing bits: decodes only without Strict
		"AAAAAAAAAAAAAAAAAAAAA\x00",
	} {
		if ValidToken(bad) {
			t.Errorf("ValidToken(%q) = true", bad)
		}
		if _, ok := LinkPath("/x", bad); ok {
			t.Errorf("LinkPath accepted %q", bad)
		}
	}
}

func TestMakeLinkHardLinksAndTheLinkOutlivesTheMessage(t *testing.T) {
	spool := t.TempDir()
	wav := filepath.Join(spool, "msg0000.wav")
	os.WriteFile(wav, []byte("RIFF-pcm"), 0o644)
	dir := filepath.Join(t.TempDir(), "state", "links")
	tok, err := MakeLink(dir, wav, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o750 {
		t.Errorf("links directory mode = %v, %v; want 0750", st.Mode().Perm(), err)
	}
	p, _ := LinkPath(dir, tok)
	a, _ := os.Stat(wav)
	b, err := os.Stat(p)
	if err != nil || !os.SameFile(a, b) {
		t.Fatalf("not a hard link: %v", err)
	}
	if a.Mode().Perm() != 0o644 {
		t.Errorf("making the link changed the message's own mode to %v", a.Mode().Perm())
	}
	// app_voicemail deletes msg0000 and renumbers msg0001 into its place:
	// the link still plays the message it was made for.
	os.Remove(wav)
	os.WriteFile(wav, []byte("RIFF-somebody-else"), 0o644)
	if got, _ := os.ReadFile(p); string(got) != "RIFF-pcm" {
		t.Errorf("the link now plays %q", got)
	}
}

func TestCopyIsTheFallbackAndIsPrivate(t *testing.T) {
	src := filepath.Join(t.TempDir(), "msg.wav")
	os.WriteFile(src, []byte("RIFF"), 0o644)
	dst := filepath.Join(t.TempDir(), "copy.wav")
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(dst)
	if st.Mode().Perm() != 0o640 {
		t.Errorf("copy mode = %v", st.Mode().Perm())
	}
	if err := copyFile(src, dst); err == nil {
		t.Error("a copy must never overwrite an existing link")
	}
	if err := copyFile(filepath.Join(t.TempDir(), "absent"), filepath.Join(t.TempDir(), "x")); err == nil {
		t.Error("copying nothing succeeded")
	}
	if _, err := MakeLink(filepath.Join(t.TempDir(), "links"), filepath.Join(t.TempDir(), "absent.wav"), nil); err == nil {
		t.Error("a link to no recording succeeded")
	}
}

func TestPruneRemovesOnlyExpiredLinks(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("x"), 0o640)
		os.Chtimes(p, now.Add(-age), now.Add(-age))
		return p
	}
	oldTok, _ := NewToken(nil)
	newTok, _ := NewToken(nil)
	old := mk(oldTok+".wav", 15*24*time.Hour)
	fresh := mk(newTok+".wav", 13*24*time.Hour)
	stranger := mk("notes.wav", 400*24*time.Hour)
	n, err := Prune(dir, 14*24*time.Hour, now)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Error("an expired link survived")
	}
	for _, p := range []string{fresh, stranger} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed", filepath.Base(p))
		}
	}
	if n, err := Prune(filepath.Join(dir, "absent"), time.Hour, now); n != 0 || err != nil {
		t.Errorf("no directory yet = %d, %v", n, err)
	}
}
