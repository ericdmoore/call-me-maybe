package mediadelivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeSource struct {
	offer       Offer
	events      []string
	failState   string
	reject      bool
	downloadErr error
}

func (s *fakeSource) List(context.Context) ([]Offer, error) {
	return []Offer{s.offer}, nil
}
func (s *fakeSource) Download(context.Context, Offer) ([]byte, error) {
	return []byte("archive"), s.downloadErr
}
func (s *fakeSource) Receipt(_ context.Context, p Offer, seq int64, state, code string) error {
	s.events = append(s.events, state)
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
	return &fakeSource{offer: Offer{Delivery: strings.Repeat("a", 43), SHA256: strings.Repeat("b", 64), State: "offered"}}
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
	file := filepath.Join(dir, source.offer.Delivery+".json")
	if err := save(file, pending{1, "downloaded", "", source.offer.SHA256}); err != nil {
		t.Fatal(err)
	}
	source.offer.Sequence = 1
	source.offer.State = "failed"
	if Receive(t.Context(), source, dir, nil) != ErrJournal {
		t.Fatal("overwrote a conflicting transition")
	}
	if err := os.WriteFile(file, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if Receive(t.Context(), source, dir, nil) != ErrJournal {
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
