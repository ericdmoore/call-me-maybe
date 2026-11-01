package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/mediadelivery"
)

type ownedFixture struct {
	offer         mediadelivery.Offer
	payload       []byte
	failList      bool
	loseInstalled bool
}

func (s *ownedFixture) List(context.Context) ([]mediadelivery.Offer, error) {
	if s.failList {
		return nil, errors.New("media unavailable")
	}
	return []mediadelivery.Offer{s.offer}, nil
}
func (s *ownedFixture) DownloadTo(_ context.Context, _ mediadelivery.Offer, w io.Writer) error {
	_, err := w.Write(s.payload)
	return err
}
func (s *ownedFixture) Receipt(_ context.Context, _ mediadelivery.Offer, seq int64, state, code string) error {
	if state == "installed" && s.loseInstalled {
		s.loseInstalled = false
		return errors.New("receipt unavailable")
	}
	s.offer.Sequence = seq
	s.offer.State = state
	return nil
}
func ownedBook(letter string) *ownedFixture {
	payload := []byte("synthetic full book " + letter)
	return &ownedFixture{payload: payload, offer: mediadelivery.Offer{Delivery: strings.Repeat(letter, 43), SKU: "book-" + letter, Version: "v1", SHA256: fmt.Sprintf("%x", sha256.Sum256(payload)), Bytes: int64(len(payload)), State: "offered"}}
}
func TestOwnedMediaContinuesOtherBooksAndReplaysReceipt(t *testing.T) {
	a, b := ownedBook("a"), ownedBook("b")
	a.failList = true
	sources := []mediadelivery.FileSource{a, b}
	installs := map[string]int{}
	install := func(_ context.Context, file, hash string) error {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != hash {
			t.Fatal("unchecked import")
		}
		installs[hash]++
		return nil
	}
	journal := filepath.Join(t.TempDir(), "receipts")
	if runOwnedMediaSources(t.Context(), sources, journal, "once", install) != 1 {
		t.Fatal("failure hidden")
	}
	if b.offer.State != "installed" || len(installs) != 1 {
		t.Fatal("one failed title blocked another")
	}
	a.failList = false
	a.loseInstalled = true
	if runOwnedMediaSources(t.Context(), sources, journal, "once", install) != 1 {
		t.Fatal("lost receipt hidden")
	}
	if installs[a.offer.SHA256] != 1 || installs[b.offer.SHA256] != 1 {
		t.Fatal(installs)
	}
	if runOwnedMediaSources(t.Context(), sources, journal, "once", install) != 0 {
		t.Fatal("replay failed")
	}
	if a.offer.State != "installed" || installs[a.offer.SHA256] != 1 || installs[b.offer.SHA256] != 1 {
		t.Fatal("replay reinstalled books", installs)
	}
	// Empty discovery never touches the installed library or pending outboxes.
	if runOwnedMediaSources(t.Context(), nil, journal, "once", install) != 0 {
		t.Fatal("empty pass")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if runOwnedMediaSources(ctx, sources, journal, "once", install) != 1 {
		t.Fatal("ignored cancellation")
	}
	a.failList = true
	if runOwnedMediaSources(t.Context(), sources, journal, "list", install) != 1 {
		t.Fatal("partial listing reported success")
	}
	a.failList = false
	if runOwnedMediaSources(t.Context(), sources, journal, "list", install) != 0 {
		t.Fatal("list failed")
	}
}
