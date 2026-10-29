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
	"time"

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
	if c, err := books.Load(filepath.Join(root, "library")); err != nil || len(c.Books) != 0 {
		t.Fatal("failed preparation published episodes", err)
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

func TestGenericSharedFeedRefreshAndLastGood(t *testing.T) {
	wav := audio(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "subscriptions.json")
	root := filepath.Join(dir, "pool")
	feedURL := "https://podcasts.example.org/nature.xml"
	id, _ := podcastconfig.IDForURL(feedURL)
	source := podcastconfig.Source{ID: id, URL: feedURL, Title: "Nature"}
	data, err := podcastconfig.SelectFeed(nil, "kitchen", source, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err = podcastconfig.SelectFeed(data, "bedroom", source, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = podcastconfig.Save(config, data); err != nil {
		t.Fatal(err)
	}
	feeds, downloads := 0, 0
	broken := false
	extra := false
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		var body []byte
		if r.URL.String() == feedURL {
			feeds++
			if broken {
				return nil, fmt.Errorf("failed public feed")
			}
			items := `<item><title>Birds</title><guid>one</guid><enclosure url="https://media.example.org/one.wav" type="audio/wav"/></item>`
			if extra {
				items = `<item><title>Trees</title><guid>two</guid><enclosure url="https://media.example.org/two.wav" type="audio/wav"/></item>` + items
			}
			body = []byte(`<rss version="2.0"><channel><title>Nature</title>` + items + `</channel></rss>`)
		} else {
			downloads++
			body = wav
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
	if err = refresh(context.Background(), config, root, client); err != nil {
		t.Fatal(err)
	}
	c, err := books.Load(filepath.Join(root, "library"))
	if err != nil || len(c.Books) != 1 || c.Books[0].Feed != id || downloads != 1 || feeds != 1 {
		t.Fatalf("not shared: %+v %d %d %v", c, feeds, downloads, err)
	}
	revision := c.Books[0].Revision
	if err = refresh(context.Background(), config, root, client); err != nil || downloads != 1 || feeds != 2 {
		t.Fatal("cached episode downloaded twice", err)
	}
	extra = true
	if err = refresh(context.Background(), config, root, client); err != nil {
		t.Fatal(err)
	}
	c, err = books.Load(filepath.Join(root, "library"))
	if err != nil || len(c.Books) != 2 || downloads != 2 {
		t.Fatal("incremental addition", err, downloads)
	}
	found := false
	for _, b := range c.Books {
		found = found || b.Revision == revision
	}
	if !found {
		t.Fatal("retained revision changed")
	}
	before, _ := os.ReadFile(filepath.Join(root, "library/catalog.json"))
	broken = true
	if err = refresh(context.Background(), config, root, client); err == nil {
		t.Fatal("failed feed accepted")
	}
	after, _ := os.ReadFile(filepath.Join(root, "library/catalog.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("failure replaced last good catalog")
	}
	data, err = podcastconfig.SelectFeed(data, "kitchen", source, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = podcastconfig.Save(config, data)
	broken = false
	if err = refresh(context.Background(), config, root, client); err != nil || downloads != 2 {
		t.Fatal("unsubscribe changed remaining handset demand", err)
	}
	// Two unrelated cached episodes must not satisfy a later pilot-only config.
	pilot, _ := podcastconfig.Select(nil, "kitchen", podcastconfig.Feed, true)
	_ = podcastconfig.Save(config, pilot)
	called := false
	failed := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		called = r.URL.String() == podcastconfig.FeedURL
		return nil, fmt.Errorf("offline")
	})}
	if err = refresh(context.Background(), config, root, failed); err == nil || !called {
		t.Fatal("generic catalog mistaken for ready pilot", err)
	}
}
func TestDNSAndRedirectBoundaries(t *testing.T) {
	dialled := []string{}
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		dialled = append(dialled, address)
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
	conn, err := dialPublic(context.Background(), "tcp", "public.example.org:443", lookup, dial)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if len(dialled) != 1 || dialled[0] != "1.1.1.1:443" {
		t.Fatal("DNS pin not preserved", dialled)
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "100.64.0.1", "198.18.0.1", "169.254.169.254", "::1", "64:ff9b::a00:1"} {
		mixed := func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}, {IP: net.ParseIP(ip)}}, nil
		}
		if _, err = dialPublic(context.Background(), "tcp", "example.org:443", mixed, dial); err == nil {
			t.Fatal("mixed private DNS accepted", ip)
		}
	}
	if len(dialled) != 1 {
		t.Fatal("dialled before checking every DNS answer")
	}
	client := Client()
	for _, raw := range []string{"http://example.org/feed", "https://127.0.0.1/feed", "https://user:pass@example.org/feed"} {
		req, _ := http.NewRequest("GET", raw, nil)
		if client.CheckRedirect(req, nil) == nil {
			t.Fatal("unsafe redirect", raw)
		}
	}
	req, _ := http.NewRequestWithContext(context.WithValue(context.Background(), feedRequestKey{}, true), "GET", "https://example.org/private?token=x", nil)
	if client.CheckRedirect(req, nil) == nil {
		t.Fatal("private feed redirect accepted")
	}
}

func TestFourFeedsPrepareIndependentlyAndScheduledValidation(t *testing.T) {
	wav := audio(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "subscriptions.json")
	root := filepath.Join(dir, "pool")
	var data []byte
	sources := []podcastconfig.Source{}
	for i := 0; i < 4; i++ {
		url := fmt.Sprintf("https://publisher%d.example.org/rss", i)
		id, _ := podcastconfig.IDForURL(url)
		source := podcastconfig.Source{ID: id, URL: url, Title: fmt.Sprintf("Show %d", i)}
		sources = append(sources, source)
		var err error
		data, err = podcastconfig.SelectFeed(data, "kitchen", source, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := podcastconfig.Save(config, data); err != nil {
		t.Fatal(err)
	}
	feeds, downloads := 0, 0
	broken := ""
	add := false
	token := "one"
	conditional := 0
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		headers := make(http.Header)
		var body []byte
		if strings.HasSuffix(r.URL.Path, "rss") {
			feeds++
			if r.URL.Host == broken {
				return nil, fmt.Errorf("offline")
			}
			if r.Header.Get("If-None-Match") == "v1" && !add {
				conditional++
				return &http.Response{StatusCode: 304, Body: io.NopCloser(strings.NewReader("")), Header: headers}, nil
			}
			headers.Set("ETag", "v1")
			newer := ""
			if add {
				newer = `<item><title>New</title><guid>new</guid><enclosure url="https://media.example.org/new.wav"/></item>`
			}
			body = []byte(`<rss version="2.0"><channel><title>Show</title>` + newer + `<item><title>Bad HTTP</title><guid>bad</guid><enclosure url="http://media.example.org/no.wav"/></item><item><title>First</title><guid>one</guid><enclosure url="https://media.example.org/one.wav?signature=` + token + `"/></item><item><title>Second</title><guid>two</guid><enclosure url="https://media.example.org/two.wav"/></item></channel></rss>`)
		} else {
			downloads++
			body = wav
		}
		return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	now := time.Unix(2000000000, 0)
	run := func(scheduled bool) error {
		return refreshRun(context.Background(), config, root, client, now, scheduled)
	}
	if err := run(true); err != nil {
		t.Fatal(err)
	}
	c, err := books.Load(filepath.Join(root, "library"))
	if err != nil || len(c.Books) != 8 || downloads != 8 || feeds != 4 {
		t.Fatal("four-feed working set", len(c.Books), feeds, downloads, err)
	}
	catalog, _ := os.ReadFile(filepath.Join(root, "library/catalog.json"))
	st, _ := os.Stat(filepath.Join(root, "library/catalog.json"))
	status, _ := os.Stat(filepath.Join(root, "status.json"))
	for i := 0; i < 59; i++ {
		now = now.Add(time.Minute)
		if err = run(true); err != nil {
			t.Fatal(err)
		}
	}
	if feeds != 4 || downloads != 8 {
		t.Fatal("scheduled polling repeated", feeds, downloads)
	}
	again, _ := os.Stat(filepath.Join(root, "library/catalog.json"))
	againStatus, _ := os.Stat(filepath.Join(root, "status.json"))
	if !again.ModTime().Equal(st.ModTime()) || !againStatus.ModTime().Equal(status.ModTime()) {
		t.Fatal("unchanged catalog/status rewritten")
	}
	now = now.Add(time.Minute)
	if err = run(true); err != nil || conditional != 4 || downloads != 8 {
		t.Fatal("hourly conditional polling", conditional, downloads, err)
	}
	// Signed URL churn does not change source identity or transcode unchanged audio.
	token = "two"
	add = true
	broken = strings.TrimPrefix(strings.TrimSuffix(sources[0].URL, "/rss"), "https://")
	if err = run(false); err == nil {
		t.Fatal("missing per-feed failure")
	}
	c, _ = books.Load(filepath.Join(root, "library"))
	if len(c.Books) != 8 || downloads != 11 {
		t.Fatal("healthy feeds stalled", len(c.Books), downloads)
	}
	for _, source := range sources {
		if source.ID == sources[0].ID {
			if !readFeedState(filepath.Join(root, ".feed-"+source.ID+".json")).Failed {
				t.Fatal("source failure not recorded")
			}
		}
	}
	// Stable feed ordering does not depend on whether each episode was reused.
	previousFeed := ""
	for _, b := range c.Books {
		if b.Feed < previousFeed {
			t.Fatal("menu feed order changed")
		}
		previousFeed = b.Feed
	}
	if bytes.Equal(catalog, mustRead(t, filepath.Join(root, "library/catalog.json"))) {
		t.Fatal("healthy additions not published")
	}
	broken = ""
	add = false
	sources[1].Title = "Renamed show"
	data, err = podcastconfig.SelectFeed(data, "kitchen", sources[1], true)
	if err != nil {
		t.Fatal(err)
	}
	_ = podcastconfig.Save(config, data)
	if err = run(false); err != nil {
		t.Fatal(err)
	}
	renamed, _ := books.Load(filepath.Join(root, "library"))
	foundRename := false
	for _, b := range renamed.Books {
		if b.Feed == sources[1].ID {
			foundRename = true
			if !strings.HasPrefix(b.Title, "Renamed show: ") {
				t.Fatal("stale spoken show title", b.Title)
			}
		}
	}
	if !foundRename || downloads != 13 {
		t.Fatal("title refresh or signed URL reuse failed", downloads)
	}
	// Removing all subscriptions clears both audio and RSS caches, preserving bookmarks.
	if err = podcastconfig.Save(config, []byte(`{"schema":1,"subscriptions":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err = run(true); err != nil {
		t.Fatal(err)
	}
	c, _ = books.Load(filepath.Join(root, "library"))
	s := ReadStatus(root)
	if len(c.Books) != 0 || s == nil || s.State != "idle" || s.ReadyEpisodes != 0 || s.Bytes != 0 {
		t.Fatal("idle retains audio", s)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "library"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "pod-") {
			t.Fatal("audio retained", e.Name())
		}
	}
}
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestBadAudioDoesNotBlockOtherEpisodes(t *testing.T) {
	wav := audio(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "subscriptions.json")
	root := filepath.Join(dir, "pool")
	url := "https://publisher.example.org/rss"
	id, _ := podcastconfig.IDForURL(url)
	data, _ := podcastconfig.SelectFeed(nil, "kitchen", podcastconfig.Source{ID: id, URL: url, Title: "Show"}, true)
	_ = podcastconfig.Save(config, data)
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		body := wav
		if r.URL.String() == url {
			body = []byte(`<rss version="2.0"><channel><title>Show</title><item><title>Corrupt</title><guid>bad</guid><enclosure url="https://media.example.org/bad"/></item><item><title>Good</title><guid>good</guid><enclosure url="https://media.example.org/good"/></item></channel></rss>`)
		} else if r.URL.Path == "/bad" {
			body = []byte("corrupt audio")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	if err := refresh(context.Background(), config, root, client); err == nil {
		t.Fatal("bad item not reported")
	}
	c, err := books.Load(filepath.Join(root, "library"))
	if err != nil || len(c.Books) != 1 || c.Books[0].ID != episodeIDFor(id, "good") {
		t.Fatal("bad item blocked valid audio", c, err)
	}
}
