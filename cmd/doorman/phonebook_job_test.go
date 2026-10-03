package main

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/events"
	"callmemaybe/internal/ownbook"
	"callmemaybe/internal/policy"
)

type fakeSTT struct {
	text string
	err  error
	n    int
}

func (f *fakeSTT) Transcribe(_ context.Context, _ string, _ []byte) (string, error) {
	f.n++
	return f.text, f.err
}

type fakeJournal struct{ types []events.Type }

func (f *fakeJournal) Append(_ context.Context, e events.Event) error {
	f.types = append(f.types, e.Type)
	return nil
}

func phonebookFixture(t *testing.T) (*phonebookJob, string, *fakeSTT, *fakeJournal) {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "spool"), 0o750) // a spool beside the books, for the test only
	s := &fakeSTT{text: " Maddie from school. "}
	j := &fakeJournal{}
	now := time.Date(2026, 10, 2, 21, 20, 0, 0, time.Local)
	job := &phonebookJob{
		dir: dir, spool: filepath.Join(dir, "spool"), countryCode: "1", stt: s, journal: j,
		handsets: map[string]policy.Handset{"norah": {ID: "norah"}},
		now:      func() time.Time { return now },
		out:      func(f string, a ...any) { t.Logf(f, a...) },
	}
	return job, dir, s, j
}

// The number is filed before the name is known, under its number; the name
// replaces it when the transcription lands; the audio goes; the journal
// hears both, by handset only.
func TestPhonebookFilesAtOnceAndNamesWhenHeard(t *testing.T) {
	job, dir, s, j := phonebookFixture(t)
	stamp := itoa(job.now().Add(-5 * time.Minute).Unix())
	rec := filepath.Join(dir, "spool", "norah-"+stamp+"-9725550142.wav")
	os.WriteFile(rec, []byte("RIFF"), 0o644)

	s.err = errors.New("whisper is down")
	if err := job.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ := ownbook.Load(filepath.Join(dir, "own"), "norah")
	if len(b.Entries) != 1 || b.Entries[0].Name != "(972) 555-0142" || b.Entries[0].E164 != "+19725550142" || b.Entries[0].Named {
		t.Fatalf("after filing: %+v", b.Entries)
	}
	if _, err := os.Stat(rec); err != nil {
		t.Fatal("the recording must wait for a transcription")
	}
	if len(j.types) != 1 || j.types[0] != events.PhonebookAdded {
		t.Errorf("journal = %v", j.types)
	}

	s.err = nil
	if err := job.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ = ownbook.Load(filepath.Join(dir, "own"), "norah")
	if b.Entries[0].Name != "Maddie from school" || !b.Entries[0].Named {
		t.Errorf("after naming: %+v", b.Entries[0])
	}
	if _, err := os.Stat(rec); err == nil {
		t.Error("a named recording must be deleted")
	}
	if len(j.types) != 2 || j.types[1] != events.PhonebookNamed {
		t.Errorf("journal = %v", j.types)
	}
	// A third run with nothing in the spool changes nothing and asks nobody.
	n := s.n
	job.run(context.Background())
	if s.n != n {
		t.Error("nothing to transcribe, nothing asked")
	}
}

func TestPhonebookGivesUpOnTheNameButKeepsTheNumber(t *testing.T) {
	job, dir, s, _ := phonebookFixture(t)
	s.err = errors.New("still down")
	old := time.Date(2026, 9, 20, 10, 0, 0, 0, time.Local) // 12 days before "now"
	rec := filepath.Join(dir, "spool", "norah-"+itoa(old.Unix())+"-9725550199.wav")
	os.WriteFile(rec, []byte("RIFF"), 0o644)
	job.run(context.Background())
	if _, err := os.Stat(rec); err == nil {
		t.Error("after seven days the audio is dropped")
	}
	b, _ := ownbook.Load(filepath.Join(dir, "own"), "norah")
	if len(b.Entries) != 1 || b.Entries[0].Named {
		t.Errorf("the number stays, unnamed: %+v", b.Entries)
	}
}

func TestPhonebookIgnoresWhatIsNotARecordingAndDropsNonNumbers(t *testing.T) {
	job, dir, s, _ := phonebookFixture(t)
	spool := filepath.Join(dir, "spool")
	os.WriteFile(filepath.Join(spool, "notes.wav"), []byte("x"), 0o644)
	fresh := itoa(job.now().Add(-time.Minute).Unix())
	os.WriteFile(filepath.Join(spool, "stranger-"+fresh+"-9725550142.wav"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(spool, "norah-"+fresh+"-123.wav"), []byte("x"), 0o644)
	job.run(context.Background())
	if _, err := os.Stat(filepath.Join(spool, "notes.wav")); err != nil {
		t.Error("a file that is not a *88 recording is left alone")
	}
	if _, err := os.Stat(filepath.Join(spool, "stranger-"+fresh+"-9725550142.wav")); err != nil {
		t.Error("an unknown handset's file is left alone, not deleted")
	}
	if _, err := os.Stat(filepath.Join(spool, "norah-"+fresh+"-123.wav")); err == nil {
		t.Error("digits that are not a phone number are dropped")
	}
	if s.n != 0 {
		t.Error("nothing was worth transcribing")
	}
	if _, err := os.Stat(filepath.Join(dir, "own", "norah.vcf")); err == nil {
		t.Error("nothing was filed")
	}
}

func TestCleanNameAndDisplayNumber(t *testing.T) {
	if got := cleanName("  Maddie\n from   school. "); got != "Maddie from school" {
		t.Errorf("cleanName = %q", got)
	}
	if got := cleanName(strings.Repeat("a", 60)); len(got) != 40 {
		t.Errorf("cleanName length = %d", len(got))
	}
	if got := cleanName(" ... "); got != "" {
		t.Errorf("cleanName of punctuation = %q", got)
	}
	if got := displayNumber("+19725550142"); got != "(972) 555-0142" {
		t.Errorf("displayNumber = %q", got)
	}
	if got := displayNumber("+442079460000"); got != "+442079460000" {
		t.Errorf("displayNumber non-NANP = %q", got)
	}
}

// Speech-to-text and the own books are off the call path: nothing the
// daemon runs on a call may import them. Same guard as provider and inbox.
func TestSpeechAndOwnBooksNeverReachTheCallPath(t *testing.T) {
	fset := token.NewFileSet()
	root := filepath.Join("..", "..", "internal")
	for _, pkgDir := range []string{"lobby", "ari", "policy", "render", "provision"} {
		pkgs, err := parser.ParseDir(fset, filepath.Join(root, pkgDir), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", pkgDir, err)
		}
		for _, pkg := range pkgs {
			for path, file := range pkg.Files {
				for _, imp := range file.Imports {
					if strings.Contains(imp.Path.Value, "internal/stt") || strings.Contains(imp.Path.Value, "internal/ownbook") {
						t.Errorf("%s imports %s — transcription and the *88 books belong to `doorman phonebook`, never to a call", path, imp.Path.Value)
					}
				}
			}
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
