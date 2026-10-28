package account

import (
	"bytes"
	"callmemaybe/internal/mediadelivery"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAudiobookTransportRequiresExplicitSKUAndStreamsApprovedBytes(t *testing.T) {
	payload := bytes.Repeat([]byte("opus"), 300000)
	hash := fmt.Sprintf("%x", sha256.Sum256(payload))
	id := strings.Repeat("b", 43)
	var origin string
	corrupt := false
	redirect := false
	broken := false
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Doorman-Media-Format") != "audiobook-v1" {
			t.Error("missing full-book opt-in")
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer dd_sync_") {
			t.Error("missing sync credential")
		}
		switch r.URL.Path {
		case mediaPath:
			if r.URL.Query().Get("sku") != "sandbox-full-book" {
				t.Error("unselected SKU")
			}
			json.NewEncoder(w).Encode(map[string]any{"schema": "sandbox-pack-delivery-v2", "packs": []any{map[string]any{"delivery": id, "sku": "sandbox-full-book", "version": "1", "sha256": hash, "bytes": len(payload), "state": "offered", "sequence": 0, "download": map[string]any{"url": origin + mediaPath + "/download?signature=PRIVATE", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "authorization": "sync-bearer"}, "receipts": mediaPath + "/receipts"}}})
		case mediaPath + "/download":
			if redirect {
				http.Redirect(w, r, "https://outside.invalid/private", 302)
				return
			}
			if broken {
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
				w.Write([]byte("short"))
				return
			}
			if corrupt {
				w.Write([]byte("bad"))
				return
			}
			w.Write(payload)
		case mediaPath + "/receipts":
			w.Write([]byte(`{"received":true}`))
		default:
			t.Error("escaped media path")
		}
	})
	origin = c.origin
	seed(t, c)
	if _, err := c.SandboxAudiobook(origin, "../escape"); err != ErrAudiobookSKU {
		t.Fatal("accepted invalid SKU")
	}
	s, err := c.SandboxAudiobook(origin, "sandbox-full-book")
	if err != nil {
		t.Fatal(err)
	}
	offers, err := s.List(t.Context())
	if err != nil || len(offers) != 1 {
		t.Fatal(err)
	}
	var dst bytes.Buffer
	if err := s.DownloadTo(t.Context(), offers[0], &dst); err != nil || !bytes.Equal(dst.Bytes(), payload) {
		t.Fatal("download", err)
	}
	for _, cause := range []error{syscall.ENOSPC, io.ErrShortWrite, mediadelivery.ErrChecksum} {
		if err := s.DownloadTo(t.Context(), offers[0], failureMediaWriter{cause}); !errors.Is(err, cause) {
			t.Fatal("hid disk/writer error", err)
		}
	}
	if err := s.DownloadTo(t.Context(), offers[0], failureMediaWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("lost silent short write", err)
	}
	broken = true
	if err := s.DownloadTo(t.Context(), offers[0], &bytes.Buffer{}); !errors.Is(err, ErrNetwork) {
		t.Fatal("reader failure misclassified", err)
	}
	broken = false
	if _, err := s.Download(t.Context(), offers[0]); err == nil {
		t.Fatal("large book reached buffered preview API")
	}
	if err := s.Receipt(t.Context(), offers[0], 1, "downloaded", ""); err != nil {
		t.Fatal(err)
	}
	corrupt = true
	if err := s.DownloadTo(t.Context(), offers[0], &bytes.Buffer{}); err == nil {
		t.Fatal("accepted corruption")
	}
	corrupt = false
	redirect = true
	if err := s.DownloadTo(t.Context(), offers[0], &bytes.Buffer{}); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("followed or leaked redirect")
	}
}

type failureMediaWriter struct{ err error }

func (w failureMediaWriter) Write([]byte) (int, error) { return 0, w.err }
