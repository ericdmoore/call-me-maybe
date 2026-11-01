package account

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOwnedAudiobooksPinEditionsAndProductionPaths(t *testing.T) {
	payload := []byte("full recording fixture")
	hash := fmt.Sprintf("%x", sha256.Sum256(payload))
	origin := ""
	version := "v1"
	offerHash := hash
	offerSchema := "pack-delivery-v1"
	receipts := productionMediaPath + "/receipts"
	downloadOrigin := ""
	downloadPath := productionMediaPath + "/download?signature=PRIVATE"
	expires := time.Now().Add(time.Hour).UnixMilli()
	redirect := false
	refused := false
	var paths []string
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("X-Doorman-Media-Format") != "audiobook-v1" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer dd_sync_") {
			t.Error("missing authentication/format")
		}
		switch r.URL.Path {
		case productionMediaPath + "/owned":
			json.NewEncoder(w).Encode(map[string]any{"schema": "pack-owned-v1", "packs": []any{map[string]string{"sku": "first-book", "version": "v1", "sha256": hash}, map[string]string{"sku": "second-book", "version": "v1", "sha256": hash}}})
		case productionMediaPath:
			if refused {
				w.WriteHeader(404)
				return
			}
			id := strings.Repeat("a", 43)
			if r.URL.Query().Get("sku") == "second-book" {
				id = strings.Repeat("b", 43)
			}
			target := origin
			if downloadOrigin != "" {
				target = downloadOrigin
			}
			json.NewEncoder(w).Encode(map[string]any{"schema": offerSchema, "packs": []any{map[string]any{"delivery": id, "sku": r.URL.Query().Get("sku"), "version": version, "sha256": offerHash, "bytes": len(payload), "state": "offered", "sequence": 0, "download": map[string]any{"url": target + downloadPath, "expiresAt": expires, "authorization": "sync-bearer"}, "receipts": receipts}}})
		case productionMediaPath + "/download":
			if redirect {
				http.Redirect(w, r, "https://outside.invalid/PRIVATE", 302)
				return
			}
			w.Write(payload)
		case productionMediaPath + "/receipts":
			w.Write([]byte(`{"received":true}`))
		default:
			t.Error("unexpected path", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	origin = c.origin
	seed(t, c)
	sources, err := c.OwnedAudiobooks(t.Context(), origin)
	if err != nil || len(sources) != 2 {
		t.Fatal(err, sources)
	}
	for _, source := range sources {
		offers, err := source.List(t.Context())
		if err != nil || len(offers) != 1 {
			t.Fatal(err)
		}
		var dst bytes.Buffer
		if err := source.DownloadTo(t.Context(), offers[0], &dst); err != nil || !bytes.Equal(dst.Bytes(), payload) {
			t.Fatal(err)
		}
		if err := source.Receipt(t.Context(), offers[0], 1, "installed", ""); err != nil {
			t.Fatal(err)
		}
	}
	source := sources[0]
	offers, _ := source.List(t.Context())
	redirect = true
	if err := source.DownloadTo(t.Context(), offers[0], &bytes.Buffer{}); err == nil || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal(err)
	}
	redirect = false
	for _, bad := range []string{"version", "schema", "receipt", "download", "origin", "hash"} {
		switch bad {
		case "hash":
			offerHash = strings.Repeat("b", 64)
		case "origin":
			downloadOrigin = "https://outside.invalid"
		case "version":
			version = "v2"
		case "schema":
			offerSchema = "sandbox-pack-delivery-v2"
		case "receipt":
			receipts = mediaPath + "/receipts"
		case "download":
			downloadPath = mediaPath + "/download?signature=PRIVATE"
		}
		if _, err := source.List(t.Context()); err != ErrProtocol {
			t.Fatal(bad, err)
		}
		if err := source.DownloadTo(t.Context(), offers[0], &bytes.Buffer{}); err != ErrProtocol {
			t.Fatal("stale cached download", bad, err)
		}
		downloadOrigin = ""
		offerHash = hash
		version = "v1"
		offerSchema = "pack-delivery-v1"
		receipts = productionMediaPath + "/receipts"
		downloadPath = productionMediaPath + "/download?signature=PRIVATE"
	}
	// Refresh an expiring link against the originally discovered edition.
	expires = time.Now().Add(10 * time.Second).UnixMilli()
	offers, err = source.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	expires = time.Now().Add(time.Hour).UnixMilli()
	if err := source.DownloadTo(t.Context(), offers[0], &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	refused = true
	if _, err := source.List(t.Context()); err == nil {
		t.Fatal("revoked title accepted")
	}
	for _, path := range paths {
		if strings.Contains(path, "sandbox") {
			t.Fatal("fallback", path)
		}
	}
}

func TestOwnedDiscoveryRejectsMalformedInventoryAndFailure(t *testing.T) {
	hash := strings.Repeat("a", 64)
	valid := map[string]string{"sku": "book", "version": "v1", "sha256": hash}
	cases := []any{
		map[string]any{"schema": "other", "packs": []any{}},
		map[string]any{"schema": "pack-owned-v1"},
		map[string]any{"schema": "pack-owned-v1", "packs": []any{valid, valid}},
		map[string]any{"schema": "pack-owned-v1", "packs": []any{map[string]string{"sku": "../escape", "version": "v1", "sha256": hash}}},
		map[string]any{"schema": "pack-owned-v1", "packs": []any{map[string]string{"sku": "book", "version": "../escape", "sha256": hash}}},
		map[string]any{"schema": "pack-owned-v1", "packs": []any{map[string]string{"sku": "book", "version": "v1", "sha256": "bad"}}},
		map[string]any{"schema": "pack-owned-v1", "packs": make([]any, 1025)},
	}
	var response any
	status := 200
	c := clientServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(response)
	})
	seed(t, c)
	for _, responseCase := range cases {
		response = responseCase
		if _, err := c.OwnedAudiobooks(t.Context(), c.origin); err != ErrProtocol {
			t.Fatal(responseCase, err)
		}
	}
	response = map[string]any{"schema": "pack-owned-v1", "packs": []any{}}
	if sources, err := c.OwnedAudiobooks(t.Context(), c.origin); err != nil || len(sources) != 0 {
		t.Fatal(err)
	}
	status = 503
	if _, err := c.OwnedAudiobooks(t.Context(), c.origin); err == nil {
		t.Fatal("failure became empty inventory")
	}
	if _, err := c.OwnedAudiobooks(t.Context(), "http://outside.invalid"); err != ErrProtocol {
		t.Fatal(err)
	}
}
