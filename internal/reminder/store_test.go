package reminder

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func testJob(t *testing.T, handset string, now time.Time) Job {
	t.Helper()
	j, err := NewJob(handset, "81", "call-me-maybe/system", now)
	if err != nil {
		t.Fatal(err)
	}
	j.Due = now.Add(time.Hour)
	return j
}
func mustCommit(t *testing.T, s *Store, j Job, now time.Time) {
	t.Helper()
	if err := s.Commit(j, now); err != nil {
		t.Fatal(err)
	}
}

func TestDurableQueueAndHandsetIsolation(t *testing.T) {
	s := testStore(t)
	now := time.Now().Truncate(time.Second)
	j := testJob(t, "kids-room", now)
	mustCommit(t, s, j, now)
	b, err := os.ReadFile(s.queue("outgoing", j.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Channel: PJSIP/kids-room\n", "WaitTime: 30\n", "MaxRetries: 2\n", "RetryTime: 180\n", "Account: cmm-reminder\n", "Context: cmm-reminder-deliver\n", "Archive: yes\n"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("queue lacks %q", want)
		}
	}
	fi, _ := os.Stat(s.queue("outgoing", j.ID))
	if !fi.ModTime().Equal(j.Due) || fi.Mode().Perm() != 0o600 {
		t.Fatalf("queue date/mode: %s %v", fi.ModTime(), fi.Mode())
	}
	reopened, err := Open(s.Spool)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := reopened.Pending(j.Handset)
	if err != nil || len(jobs) != 1 || jobs[0].ID != j.ID {
		t.Fatalf("restart: %v %v", jobs, err)
	}
	others, _ := reopened.Pending("kitchen")
	if len(others) != 0 {
		t.Fatal("other handset saw a reminder")
	}
	if err := reopened.Cancel("kitchen", j.ID); !errors.Is(err, ErrMissing) {
		t.Fatalf("cross-phone cancel: %v", err)
	}
	if _, err := reopened.Claim("kitchen", j.ID); !errors.Is(err, ErrMissing) {
		t.Fatalf("cross-phone playback: %v", err)
	}
	if err := reopened.Cancel(j.Handset, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Claim(j.Handset, j.ID); !errors.Is(err, ErrMissing) {
		t.Fatalf("cancelled job played: %v", err)
	}
	jobs, _ = reopened.Pending(j.Handset)
	if len(jobs) != 0 {
		t.Fatal("cancelled job remains")
	}
}

func TestConcurrentMenusRespectQuotaAndCancellationWinsOrAnswerWins(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for range 25 {
		j := testJob(t, "kitchen", now)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.Commit(j, now)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrFull) {
				t.Errorf("commit: %v", err)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != MaxPending {
		t.Fatalf("accepted %d", accepted.Load())
	}
	jobs, _ := s.Pending("kitchen")
	j := jobs[0]
	var cancelErr, answerErr error
	wg.Add(2)
	go func() { defer wg.Done(); cancelErr = s.Cancel(j.Handset, j.ID) }()
	go func() { defer wg.Done(); _, answerErr = s.Claim(j.Handset, j.ID) }()
	wg.Wait()
	if (cancelErr == nil) == (answerErr == nil) {
		t.Fatalf("both/neither won: cancel %v answer %v", cancelErr, answerErr)
	}
	if _, err := s.Claim(j.Handset, j.ID); !errors.Is(err, ErrMissing) {
		t.Fatal("claimed twice")
	}
}

func TestRefuseInvalidOrUnpublishedJobs(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	for _, handset := range []string{"../kitchen", "kitchen\nContext:evil", "kitchen&trunk", ""} {
		if _, err := NewJob(handset, "80", "system", now); err == nil {
			t.Fatal("accepted injected destination")
		}
	}
	if _, err := Open("relative"); err == nil {
		t.Fatal("relative spool")
	}
	if _, err := NewJob("kitchen", "83", "system", now); err == nil {
		t.Fatal("unknown code")
	}
	if _, err := NewJob("kitchen", "80", "system\nEXEC", now); err == nil {
		t.Fatal("injected media")
	}
	j := testJob(t, "kitchen", now)
	j.Due = now
	if !errors.Is(s.Commit(j, now), ErrPast) {
		t.Fatal("accepted past")
	}
	j.Due = now.Add(time.Hour)
	j.Audio = true
	if s.Commit(j, now) == nil {
		t.Fatal("accepted absent recording")
	}
	j.Audio = false
	if err := os.Remove(filepath.Join(s.Spool, "outgoing")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Spool, "outgoing"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s.Commit(j, now) == nil {
		t.Fatal("acknowledged failed queue publication")
	}
	if _, err := os.Stat(s.meta(j.ID)); !os.IsNotExist(err) {
		t.Fatal("left false scheduled state")
	}
}

func TestPruneFinishedAudioCrashDraftsAndOnlyOurFiles(t *testing.T) {
	s := testStore(t)
	now := time.Now().Truncate(time.Second)
	j := testJob(t, "kitchen", now)
	j.Audio = true
	if err := os.WriteFile(s.audio(j.ID), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, s, j, now)
	fi, _ := os.Stat(s.audio(j.ID))
	if fi.Mode().Perm() != 0o600 {
		t.Fatal("recording not private")
	}
	if err := s.Prune(now.Add(48 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.audio(j.ID)); err != nil {
		t.Fatal("pruned queued recording")
	}
	if err := os.Rename(s.queue("outgoing", j.ID), s.queue("outgoing_done", j.ID)); err != nil {
		t.Fatal(err)
	}
	draft := testJob(t, "kitchen", now)
	if err := os.WriteFile(s.audio(draft.ID), []byte("draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(s.audio(draft.ID), now.Add(-25*time.Hour), now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(s.Spool, "outgoing_done", "another-app.call")
	_ = os.WriteFile(foreign, []byte("do not touch"), 0o600)
	if err := s.Prune(now); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.meta(j.ID), s.audio(j.ID), s.audio(draft.ID), s.queue("outgoing_done", j.ID)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("left private data at %s", p)
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("deleted foreign call file")
	}
}
