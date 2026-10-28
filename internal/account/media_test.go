package account

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/mediadelivery"
)

func TestMediaTransportScopeAndIntegrity(t *testing.T) {
	payload := []byte("preview bytes")
	hash := fmt.Sprintf("%x", sha256.Sum256(payload))
	id := strings.Repeat("a", 43)
	var origin string
	var receipt map[string]any
	redirect := false
	corrupt := false
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer dd_sync_") {
			t.Error("missing sync credential")
		}
		switch r.URL.Path {
		case mediaPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"schema": "sandbox-pack-delivery-v1", "packs": []any{map[string]any{"delivery": id, "sku": "preview-v1", "version": "1", "sha256": hash, "bytes": len(payload), "state": "offered", "sequence": 0, "download": map[string]any{"url": origin + mediaPath + "/download?signature=PRIVATE", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "authorization": "sync-bearer"}, "receipts": mediaPath + "/receipts"}}})
		case mediaPath + "/download":
			if redirect {
				http.Redirect(w, r, "https://outside.invalid/secret", 302)
				return
			}
			if corrupt {
				_, _ = w.Write([]byte("invalid bytes"))
				return
			}
			_, _ = w.Write(payload)
		case mediaPath + "/receipts":
			_ = json.NewDecoder(r.Body).Decode(&receipt)
			_, _ = w.Write([]byte(`{"received":true}`))
		default:
			t.Error("escaped media path")
			w.WriteHeader(404)
		}
	})
	origin = c.origin
	seed(t, c)
	source, err := c.SandboxMedia(origin)
	if err != nil {
		t.Fatal(err)
	}
	offers, err := source.List(t.Context())
	if err != nil || len(offers) != 1 {
		t.Fatal(err)
	}
	p := offers[0]
	encoded, _ := json.Marshal(p)
	if strings.Contains(string(encoded), "PRIVATE") || strings.Contains(string(encoded), "https:") {
		t.Fatal("leaked signed address")
	}
	got, err := source.Download(t.Context(), p)
	if err != nil || string(got) != string(payload) {
		t.Fatal(err)
	}
	corrupt = true
	if _, err := source.Download(t.Context(), p); err != mediadelivery.ErrChecksum {
		t.Fatal("accepted wrong hash")
	}
	corrupt = false
	redirect = true
	if _, err := source.Download(t.Context(), p); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("followed or leaked redirect")
	}
	if err := source.Receipt(t.Context(), p, 1, "downloaded", ""); err != nil {
		t.Fatal(err)
	}
	if receipt["state"] != "downloaded" || receipt["sequence"] != float64(1) {
		t.Fatal(receipt)
	}
	for _, v := range []struct {
		seq         int64
		state, code string
	}{{0, "installed", ""}, {1, "offered", ""}, {1, "failed", "raw secret"}, {1, "installed", "network"}} {
		if source.Receipt(t.Context(), p, v.seq, v.state, v.code) != ErrProtocol {
			t.Fatal("accepted invalid receipt")
		}
	}
	for _, bad := range []string{"", "http://example.invalid", "https://user:pass@example.invalid", "https://example.invalid/path", "https://example.invalid?query", "https://example.invalid#fragment"} {
		if _, err := c.SandboxMedia(bad); err != ErrProtocol {
			t.Fatal("accepted untrusted shape", bad)
		}
	}
}

func TestMediaRejectsUntrustedOffers(t *testing.T) {
	for _, kind := range []string{"origin", "path", "authorization", "expired", "receipts", "id", "hash", "size", "sequence", "state", "schema", "duplicate", "json"} {
		t.Run(kind, func(t *testing.T) {
			var origin string
			c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
				if kind == "json" {
					_, _ = w.Write([]byte("invalid"))
					return
				}
				download := map[string]any{"url": origin + mediaPath + "/download?signature=hidden", "authorization": "sync-bearer", "expiresAt": time.Now().Add(time.Hour).UnixMilli()}
				p := map[string]any{"delivery": strings.Repeat("a", 43), "sku": "preview", "version": "1", "sha256": strings.Repeat("b", 64), "bytes": 12, "state": "offered", "sequence": 0, "download": download, "receipts": mediaPath + "/receipts"}
				schema := "sandbox-pack-delivery-v1"
				switch kind {
				case "origin":
					download["url"] = "https://outside.invalid" + mediaPath + "/download"
				case "path":
					download["url"] = origin + "/login"
				case "authorization":
					download["authorization"] = "cookie"
				case "expired":
					download["expiresAt"] = 0
				case "receipts":
					p["receipts"] = "/elsewhere"
				case "id":
					p["delivery"] = "../oops"
				case "hash":
					p["sha256"] = "wrong"
				case "size":
					p["bytes"] = mediadelivery.MaxRecordedBytes + 1
				case "sequence":
					p["sequence"] = -1
				case "state":
					p["state"] = "paid"
				case "schema":
					schema = "unknown"
				}
				packs := []any{p}
				if kind == "duplicate" {
					packs = append(packs, p)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"schema": schema, "packs": packs})
			})
			origin = c.origin
			seed(t, c)
			source, _ := c.SandboxMedia(origin)
			if _, err := source.List(context.Background()); err != ErrProtocol {
				t.Fatal("accepted invalid offer", err)
			}
		})
	}
}

func TestMediaDoesNotRevokeAccountOnRefusal(t *testing.T) {
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"box_removed"}`))
	})
	seed(t, c)
	source, _ := c.SandboxMedia(c.origin)
	if _, err := source.List(t.Context()); err == nil {
		t.Fatal("accepted refusal")
	}
	if loadTest(t, c).NeedsLogin || !loadTest(t, c).RemovedAt.IsZero() {
		t.Fatal("media source changed account authority")
	}
}

func TestRecordedDownloadStreamsToPrivateFile(t *testing.T) {
	payload := []byte(strings.Repeat("recorded audio", 100000))
	hash := fmt.Sprintf("%x", sha256.Sum256(payload))
	id := strings.Repeat("a", 43)
	var origin string
	mode := "ok"
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == mediaPath {
			json.NewEncoder(w).Encode(map[string]any{"schema": "sandbox-pack-delivery-v1", "packs": []any{map[string]any{"delivery": id, "sku": "book", "version": "1", "sha256": hash, "bytes": len(payload), "state": "offered", "sequence": 0, "download": map[string]any{"url": origin + mediaPath + "/download?signature=PRIVATE", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "authorization": "sync-bearer"}, "receipts": mediaPath + "/receipts"}}})
			return
		}
		switch mode {
		case "redirect":
			http.Redirect(w, r, "https://other.invalid/private", 302)
		case "corrupt":
			w.Write([]byte(strings.Repeat("x", len(payload))))
		case "short":
			w.Write(payload[:10])
		default:
			w.Write(payload)
		}
	})
	origin = c.origin
	seed(t, c)
	preview, _ := c.SandboxMedia(origin)
	if offers, err := preview.List(t.Context()); err != nil || len(offers) != 0 {
		t.Fatal("preview did not skip full recording", err)
	}
	source, err := c.RecordedMedia(origin)
	if err != nil {
		t.Fatal(err)
	}
	offers, err := source.List(t.Context())
	if err != nil || len(offers) != 1 {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file, err := source.DownloadFile(t.Context(), offers[0], dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != string(payload) {
		t.Fatal("wrong file", err)
	}
	st, _ := os.Stat(file)
	if st.Mode().Perm() != 0600 {
		t.Fatal("scratch file permissions")
	}
	os.Remove(file)
	for _, mode = range []string{"redirect", "corrupt", "short"} {
		file, err = source.DownloadFile(t.Context(), offers[0], dir)
		if err == nil || file != "" || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal(mode, err)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("left failed download scratch", mode)
		}
	}
}

func TestMediaCapabilitiesAndPreviewFiltering(t *testing.T) {
	var origin string
	full := false
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		want := ""
		if full {
			want = "recorded-v1"
		}
		if r.Header.Get("X-Doorman-Media") != want {
			t.Error("wrong capability")
		}
		packs := []any{}
		for i, size := range []int64{12, MaxPreviewBytes + 1} {
			packs = append(packs, map[string]any{"delivery": strings.Repeat(string(rune('a'+i)), 43), "sku": "book", "version": "1", "sha256": strings.Repeat("b", 64), "bytes": size, "state": "offered", "sequence": 0, "download": map[string]any{"url": origin + mediaPath + "/download?signature=PRIVATE", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "authorization": "sync-bearer"}, "receipts": mediaPath + "/receipts"})
		}
		json.NewEncoder(w).Encode(map[string]any{"schema": "sandbox-pack-delivery-v1", "packs": packs})
	})
	origin = c.origin
	seed(t, c)
	preview, _ := c.SandboxMedia(origin)
	if offers, err := preview.List(t.Context()); err != nil || len(offers) != 1 {
		t.Fatal("preview blocked", err)
	}
	full = true
	source, _ := c.RecordedMedia(origin)
	if offers, err := source.List(t.Context()); err != nil || len(offers) != 2 {
		t.Fatal("full capability failed", err)
	}
}

func TestRecordedDownloadRefreshAndIdleDeadline(t *testing.T) {
	for _, stall := range []bool{false, true} {
		t.Run(fmt.Sprint(stall), func(t *testing.T) {
			payload := []byte("abcdefgh")
			hash := fmt.Sprintf("%x", sha256.Sum256(payload))
			id := strings.Repeat("a", 43)
			var origin string
			lists := 0
			c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == mediaPath {
					lists++
					json.NewEncoder(w).Encode(map[string]any{"schema": "sandbox-pack-delivery-v1", "packs": []any{map[string]any{"delivery": id, "sku": "book", "version": "1", "sha256": hash, "bytes": len(payload), "state": "offered", "sequence": 0, "download": map[string]any{"url": origin + mediaPath + "/download?fresh=yes", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "authorization": "sync-bearer"}, "receipts": mediaPath + "/receipts"}}})
					return
				}
				if r.URL.Query().Get("fresh") != "yes" {
					t.Error("used expired address")
				}
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				if stall {
					<-r.Context().Done()
					return
				}
				for _, b := range payload {
					w.Write([]byte{b})
					w.(http.Flusher).Flush()
					time.Sleep(15 * time.Millisecond)
				}
			})
			origin = c.origin
			seed(t, c)
			source, _ := c.RecordedMedia(origin)
			source.idleTimeout = 80 * time.Millisecond
			source.downloads[id+"/"+hash] = mediaPath + "/download?expired=yes"
			source.expires = map[string]int64{id + "/" + hash: time.Now().Add(-time.Minute).UnixMilli()}
			dir := t.TempDir()
			file, err := source.DownloadFile(t.Context(), MediaOffer{Delivery: id, SHA256: hash, Bytes: int64(len(payload))}, dir)
			if lists != 1 {
				t.Fatal("did not refresh before download")
			}
			if stall {
				if err == nil || file != "" {
					t.Fatal("stalled transfer succeeded")
				}
				entries, _ := os.ReadDir(dir)
				if len(entries) != 0 {
					t.Fatal("stalled scratch retained")
				}
			} else {
				if err != nil {
					t.Fatal("progressing transfer cut off", err)
				}
				data, _ := os.ReadFile(file)
				if string(data) != string(payload) {
					t.Fatal("wrong data")
				}
			}
		})
	}
}
