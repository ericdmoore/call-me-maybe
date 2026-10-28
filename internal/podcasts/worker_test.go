package podcasts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/books"
	"callmemaybe/internal/podcastconfig"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func audio(t *testing.T) []byte {
	t.Helper()
	for _, name := range []string{"ffmpeg", "ffprobe", "espeak-ng"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("audio tools unavailable")
		}
	}
	path := filepath.Join(t.TempDir(), "source.wav")
	if err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-y", path).Run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func TestSharedPilotCacheAndRetry(t *testing.T) {
	wav := audio(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "subscriptions.json")
	root := filepath.Join(dir, "pool")
	data, _ := podcastconfig.Select(nil, "kitchen", podcastconfig.Feed, true)
	data, _ = podcastconfig.Select(data, "bedroom", podcastconfig.Feed, true)
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fail := true
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		var body []byte
		if r.URL.String() == podcastconfig.FeedURL {
			body = []byte(fmt.Sprintf(`<rss version="2.0"><channel><title>Brains On</title><item><title>Dogs</title><guid>%s</guid><enclosure url="https://media.example/dogs.wav" type="audio/wav"/></item><item><title>Hair</title><guid>%s</guid><enclosure url="https://media.example/hair.wav" type="audio/wav"/></item></channel></rss>`, pilotGUIDs[0], pilotGUIDs[1]))
		} else {
			if fail {
				return nil, fmt.Errorf("unavailable")
			}
			body = wav
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
	if err := refresh(context.Background(), config, root, client); err == nil {
		t.Fatal("failed download accepted")
	}
	if s := ReadStatus(root); s == nil || s.State != "error" || s.ReadyEpisodes != 0 {
		t.Fatalf("failed download status %+v", s)
	}
	if _, err := books.Load(filepath.Join(root, "library")); !os.IsNotExist(err) {
		t.Fatal("failed preparation published a catalog")
	}
	fail = false
	if err := refresh(context.Background(), config, root, client); err != nil {
		t.Fatal(err)
	}
	c, err := books.Load(filepath.Join(root, "library"))
	if err != nil || len(c.Books) != 2 {
		t.Fatal("missing prepared catalog", err)
	}
	for _, b := range c.Books {
		if !books.Ready(filepath.Join(root, "library"), b) || !strings.HasSuffix(b.Chapters[0].Audio, ".opus") || b.Chapters[0].Bytes == 0 {
			t.Fatal("episode is not validated Opus")
		}
	}
	before := calls
	if err := refresh(context.Background(), config, root, client); err != nil || before != calls {
		t.Fatal("shared cache downloaded twice", err)
	}
	data, _ = podcastconfig.Select(data, "kitchen", podcastconfig.Feed, false)
	_ = os.WriteFile(config, data, 0600)
	if err := refresh(context.Background(), config, root, client); err != nil || before != calls {
		t.Fatal("unsubscribe discarded another handset's cache")
	}
	after, _ := books.Load(filepath.Join(root, "library"))
	a, _ := json.Marshal(c)
	b, _ := json.Marshal(after)
	if !bytes.Equal(a, b) {
		t.Fatal("unsubscribe changed shared artifacts")
	}
}
func TestPublicFetchBoundaries(t *testing.T) {
	for _, raw := range []string{"http://example.com/a", "file:///tmp/source", "https://user:secret@example.com/a", "https://example.com:444/a"} {
		if publicURL(raw) == nil {
			t.Fatal("accepted unsafe URL")
		}
	}
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "fc00::1"} {
		if publicIP(net.ParseIP(raw)) {
			t.Fatal("accepted private address")
		}
	}
	if !publicIP(net.ParseIP("1.1.1.1")) {
		t.Fatal("rejected public IP")
	}
	client := &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("12345")), Header: make(http.Header)}, nil
	})}
	if fetch(context.Background(), client, "https://example.com/a", io.Discard, 4) == nil {
		t.Fatal("oversized stream accepted")
	}
}
