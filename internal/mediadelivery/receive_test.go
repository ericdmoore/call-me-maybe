package mediadelivery

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

type fakeSource struct {
	offer       Offer
	events      []string
	failState   string
	reject      bool
	downloadErr error
	payload     []byte
	codes       []string
}

func (s *fakeSource) List(context.Context) ([]Offer, error) {
	return []Offer{s.offer}, nil
}
func (s *fakeSource) Download(context.Context, Offer) ([]byte, error) {
	if s.payload != nil {
		return s.payload, s.downloadErr
	}
	return []byte("archive"), s.downloadErr
}
func (s *fakeSource) Receipt(_ context.Context, p Offer, seq int64, state, code string) error {
	s.events = append(s.events, state)
	s.codes = append(s.codes, code)
	if s.failState == state {
		s.failState = ""
		return errors.New("network")
	}
	if s.reject {
		return errors.New("refused")
	}
	if seq <= s.offer.Sequence {
		return errors.New("stale")
	}
	s.offer.Sequence = seq
	s.offer.State = state
	return nil
}
func fake() *fakeSource {
	return &fakeSource{offer: Offer{Delivery: strings.Repeat("a", 43), SHA256: strings.Repeat("b", 64), State: "offered", Bytes: 7}}
}
func TestReceiveInstallationAndReplay(t *testing.T) {
	source := fake()
	dir := filepath.Join(t.TempDir(), "journal")
	installs := 0
	install := func(context.Context, []byte, string) error { installs++; return nil }
	if err := Receive(t.Context(), source, dir, install); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(source.events, []string{"downloading", "downloaded", "installing", "installed"}) || installs != 1 {
		t.Fatal(source.events, installs)
	}
	if err := Receive(t.Context(), source, dir, install); err != nil || installs != 1 {
		t.Fatal("reinstalled terminal delivery", err)
	}
}
func TestReceiveRetriesExactPendingReceipt(t *testing.T) {
	for _, step := range []string{"downloading", "downloaded", "installing", "installed"} {
		t.Run(step, func(t *testing.T) {
			source := fake()
			source.failState = step
			dir := filepath.Join(t.TempDir(), "journal")
			install := func(context.Context, []byte, string) error { return nil }
			if Receive(t.Context(), source, dir, install) == nil {
				t.Fatal("lost failed receipt")
			}
			var pendingFile string
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".json") {
					pendingFile = filepath.Join(dir, e.Name())
				}
			}
			if pendingFile == "" {
				t.Fatal("receipt not durable")
			}
			if err := Receive(t.Context(), source, dir, install); err != nil {
				t.Fatal(err)
			}
			if source.offer.State != "installed" {
				t.Fatal(source.offer)
			}
		})
	}
}
func TestReceiveNeverReportsInstalledAfterFailure(t *testing.T) {
	for _, download := range []bool{true, false} {
		source := fake()
		if download {
			source.downloadErr = ErrChecksum
		}
		err := Receive(t.Context(), source, filepath.Join(t.TempDir(), "journal"), func(context.Context, []byte, string) error { return errors.New("bad pack") })
		if err == nil || source.offer.State != "failed" {
			t.Fatal(err, source.offer)
		}
		for _, s := range source.events {
			if s == "installed" {
				t.Fatal("false installation")
			}
		}
	}
}
func TestReceiveRefusesReceiptConflictAndCorruptJournal(t *testing.T) {
	source := fake()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, source.offer.Delivery+".json")
	if err := save(file, pending{1, "downloaded", "", source.offer.SHA256}); err != nil {
		t.Fatal(err)
	}
	source.offer.Sequence = 1
	source.offer.State = "failed"
	if !errors.Is(Receive(t.Context(), source, dir, nil), ErrJournal) {
		t.Fatal("overwrote a conflicting transition")
	}
	if err := os.WriteFile(file, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(Receive(t.Context(), source, dir, nil), ErrJournal) {
		t.Fatal("ignored broken journal")
	}
	source.offer.Sequence = 0
	source.offer.State = "offered"
	os.Remove(file)
	source.reject = true
	if Receive(t.Context(), source, dir, nil) == nil {
		t.Fatal("installed after refusal")
	}
}

func TestReceiveRejectsSourceSizeMismatchBeforeInstallation(t *testing.T) {
	for _, size := range []int{0, 6, 8, MaxPreviewBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			source := fake()
			source.payload = make([]byte, size)
			installed := false
			err := Receive(t.Context(), source, filepath.Join(t.TempDir(), "journal"), func(context.Context, []byte, string) error { installed = true; return nil })
			if !errors.Is(err, ErrChecksum) || installed {
				t.Fatal("bad source bytes reached installer", err)
			}
			if !reflect.DeepEqual(source.events, []string{"downloading", "failed"}) || source.codes[1] != "checksum" {
				t.Fatal("invalid failure receipt", source.events, source.codes)
			}
		})
	}
}
func TestJournalErrorsDistinguishStorageLockAndInvalidDataWithoutPaths(t *testing.T) {
	assert := func(err error, operation string) {
		t.Helper()
		var failure *JournalError
		if !errors.Is(err, ErrJournal) || !errors.As(err, &failure) || failure.Operation != operation {
			t.Fatalf("wrong diagnostic: %v", err)
		}
		if strings.Contains(err.Error(), "private-marker") {
			t.Fatal("journal diagnostic leaked a private path")
		}
	}
	// A real filesystem failure retains its underlying error for operator code.
	root := t.TempDir()
	file := filepath.Join(root, "private-marker")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	err := Receive(t.Context(), fake(), filepath.Join(file, "journal"), nil)
	assert(err, "create-directory")
	var pathError *os.PathError
	if !errors.As(err, &pathError) {
		t.Fatal("storage cause was lost")
	}
	// A concurrently held lock is a different failure, with an inspectable errno.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	err = Receive(t.Context(), fake(), dir, nil)
	assert(err, "lock")
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatal("lock cause was lost")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	source := fake()
	if err := os.WriteFile(filepath.Join(dir, source.offer.Delivery+".json"), []byte("private-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	assert(Receive(t.Context(), source, dir, nil), "decode-receipt")
	if err := save(filepath.Join(dir, source.offer.Delivery+".json"), pending{1, "downloaded", "", source.offer.SHA256}); err != nil {
		t.Fatal(err)
	}
	source.offer.Sequence = 1
	source.offer.State = "failed"
	assert(Receive(t.Context(), source, dir, nil), "reconcile-receipt")
}

type fileSource struct{ *fakeSource }

func (s *fileSource) DownloadFile(_ context.Context, offer Offer, dir string) (string, error) {
	if s.downloadErr != nil {
		return "", s.downloadErr
	}
	if int64(len(s.payload)) != offer.Bytes || fmt.Sprintf("%x", sha256.Sum256(s.payload)) != offer.SHA256 {
		return "", ErrChecksum
	}
	f, err := os.CreateTemp(dir, ".archive-")
	if err != nil {
		return "", err
	}
	_, err = f.Write(s.payload)
	f.Close()
	return f.Name(), err
}
func TestRecordedReceiveVerificationRecoveryAndCleanup(t *testing.T) {
	for _, step := range []string{"", "downloading", "downloaded", "installing", "installed"} {
		t.Run(step, func(t *testing.T) {
			payload := []byte("compressed recording")
			source := &fileSource{fake()}
			source.payload = payload
			source.offer.Bytes = int64(len(payload))
			source.offer.SHA256 = fmt.Sprintf("%x", sha256.Sum256(payload))
			source.failState = step
			dir := filepath.Join(t.TempDir(), "journal")
			installs := 0
			install := func(_ context.Context, file, hash string) error {
				installs++
				data, err := os.ReadFile(file)
				if err != nil || string(data) != string(payload) {
					t.Fatal("bad scratch", err)
				}
				return nil
			}
			err := ReceiveFiles(t.Context(), source, dir, install)
			if step != "" {
				if err == nil {
					t.Fatal("expected receipt interruption")
				}
				if err = ReceiveFiles(t.Context(), source, dir, install); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err = ReceiveFiles(t.Context(), source, dir, install); err != nil {
				t.Fatal(err)
			}
			if installs < 1 {
				t.Fatal("not installed")
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".archive-") {
					t.Fatal("scratch retained")
				}
			}
			os.WriteFile(filepath.Join(dir, ".archive-orphan"), payload, 0600)
			if err = ReceiveFiles(t.Context(), source, dir, install); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(filepath.Join(dir, ".archive-orphan")); !os.IsNotExist(err) {
				t.Fatal("orphan not removed")
			}
		})
	}
	source := &fileSource{fake()}
	source.payload = []byte("bad")
	dir := filepath.Join(t.TempDir(), "journal")
	if err := ReceiveFiles(t.Context(), source, dir, func(context.Context, string, string) error { t.Fatal("installed wrong hash"); return nil }); !errors.Is(err, ErrChecksum) {
		t.Fatal(err)
	}
}

type brokenFileSource struct {
	*fakeSource
	path func(string) string
}

func (s *brokenFileSource) DownloadFile(_ context.Context, _ Offer, dir string) (string, error) {
	return s.path(dir), nil
}
func TestRecordedEarlyFailuresReportFailure(t *testing.T) {
	for _, kind := range []string{"unsupported", "outside", "missing", "empty", "directory"} {
		t.Run(kind, func(t *testing.T) {
			base := fake()
			var source Source = base
			if kind != "unsupported" {
				source = &brokenFileSource{base, func(dir string) string {
					switch kind {
					case "outside":
						return filepath.Join(t.TempDir(), "outside")
					case "empty":
						return ""
					case "directory":
						p := filepath.Join(dir, ".archive-directory")
						os.Mkdir(p, 0700)
						return p
					default:
						return filepath.Join(dir, ".archive-missing")
					}
				}}
			}
			err := ReceiveFiles(t.Context(), source, filepath.Join(t.TempDir(), "journal"), func(context.Context, string, string) error { t.Fatal("installed invalid scratch"); return nil })
			if err == nil || !reflect.DeepEqual(base.events, []string{"downloading", "failed"}) {
				t.Fatal("left delivery downloading", err, base.events)
			}
		})
	}
}
