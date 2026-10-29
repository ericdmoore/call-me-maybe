// Package podcasts prepares the small, explicit pilot outside the calling process.
package podcasts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"callmemaybe/internal/books"
	"callmemaybe/internal/podcastconfig"
	"github.com/mmcdole/gofeed"
)

const DefaultRoot = "/var/spool/asterisk/cmm-media-cache/podcasts"
const DefaultConfig = "/var/spool/call-me-maybe/podcasts/subscriptions.json"
const MaxCacheBytes int64 = 512 << 20

// Fixed normal episodes keep the first pilot predictable; never auto-play a
// cross-promotion simply because it is the newest feed item.
var pilotGUIDs = []string{"1b208194-378e-11f1-9278-2793d99a1c1d", "1b0cb876-378e-11f1-9278-5f8c1f38de0e"}

type Status struct {
	State         string `json:"state"`
	ReadyEpisodes int    `json:"readyEpisodes"`
	Bytes         int64  `json:"bytes"`
	UpdatedAt     int64  `json:"updatedAt"`
}

func ReadStatus(root string) *Status {
	data, err := os.ReadFile(filepath.Join(root, "status.json"))
	if err != nil || len(data) > 4096 {
		return nil
	}
	var s Status
	if json.Unmarshal(data, &s) != nil || s.ReadyEpisodes < 0 || s.ReadyEpisodes > 18 || s.Bytes < 0 || s.Bytes > MaxCacheBytes {
		return nil
	}
	switch s.State {
	case "idle", "preparing", "ready", "error":
	default:
		return nil
	}
	return &s
}
func publish(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".publish-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0640); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// Public HTTPS only, including every redirect and DNS answer. The same
// resolved IP is dialled, so a second DNS resolution cannot reach the LAN.
func publicURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || u.Fragment != "" {
		return errors.New("podcast requires public HTTPS")
	}
	// Public enclosure URLs may contain publisher signatures; never log them.
	candidate := *u
	candidate.RawQuery = ""
	candidate.ForceQuery = false
	if _, err = podcastconfig.CanonicalURL(candidate.String()); err != nil {
		return errors.New("podcast requires public HTTPS")
	}
	return nil
}
func publicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	return ok && podcastconfig.PublicIP(addr)
}

type feedRequestKey struct{}
type lookupFunc func(context.Context, string) ([]net.IPAddr, error)
type dialFunc func(context.Context, string, string) (net.Conn, error)

func dialPublic(ctx context.Context, network, address string, lookup lookupFunc, dial dialFunc) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, errors.New("invalid podcast address")
	}
	ips, err := lookup(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("podcast DNS unavailable")
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			return nil, errors.New("podcast address is not public")
		}
	}
	for _, ip := range ips {
		conn, err := dial(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("podcast server unavailable")
}
func Client() *http.Client {
	transport := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DisableCompression: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dialer := net.Dialer{Timeout: 15 * time.Second}
		return dialPublic(ctx, network, address, net.DefaultResolver.LookupIPAddr, dialer.DialContext)
	}}
	return &http.Client{Transport: transport, Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 12 {
			return errors.New("too many podcast redirects")
		}
		if req.Context().Value(feedRequestKey{}) == true {
			if _, err := podcastconfig.CanonicalURL(req.URL.String()); err != nil {
				return errors.New("invalid public feed redirect")
			}
		}
		return publicURL(req.URL.String())
	}}
}
func fetch(ctx context.Context, client *http.Client, raw string, out io.Writer, limit int64) error {
	if publicURL(raw) != nil {
		return errors.New("invalid podcast URL")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return errors.New("invalid podcast request")
	}
	req.Header.Set("User-Agent", "CallMeMaybe-PodcastPilot/1")
	res, err := client.Do(req)
	if err != nil {
		return errors.New("podcast fetch failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.ContentLength > limit {
		return errors.New("podcast response refused")
	}
	n, err := io.Copy(out, io.LimitReader(res.Body, limit+1))
	if err != nil || n == 0 || n > limit {
		return errors.New("podcast response exceeds limit or is incomplete")
	}
	return nil
}
func episodeID(guid string) string { return episodeIDFor(podcastconfig.Feed, guid) }
func episodeIDFor(feed, guid string) string {
	sum := sha256.Sum256([]byte(feed + "\x00" + guid))
	return "pod-" + hex.EncodeToString(sum[:])[:32]
}
func diskBytes(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			st, err := d.Info()
			if err != nil {
				return err
			}
			if !st.Mode().IsRegular() {
				return errors.New("unexpected cache file")
			}
			size += st.Size()
		}
		return nil
	})
	return size, err
}
func cached(root string) (int, int64) {
	c, err := books.Load(filepath.Join(root, "library"))
	if err != nil {
		return 0, 0
	}
	n := 0
	var bytes int64
	for _, b := range c.Books {
		if b.Origin != "podcast" || len(b.Chapters) != 1 || !books.Ready(filepath.Join(root, "library"), b) {
			continue
		}
		ch := b.Chapters[0]
		st, err := os.Stat(filepath.Join(root, "library", b.ID+"-"+b.Revision, ch.Audio))
		if err == nil && st.Mode().IsRegular() && st.Size() > 0 {
			n++
			bytes += st.Size()
		}
	}
	return n, bytes
}

// Refresh is restartable: temporary sources are disposable; only a complete,
// verified catalog is published. One box lock coalesces all handset demand.
func Refresh(ctx context.Context, configPath, root string) error {
	return refresh(ctx, configPath, root, Client())
}
func RefreshScheduled(ctx context.Context, configPath, root string) error {
	return refreshRun(ctx, configPath, root, Client(), time.Now(), true)
}
func refresh(ctx context.Context, configPath, root string, client *http.Client) error {
	return refreshRun(ctx, configPath, root, client, time.Now(), false)
}
func refreshRun(ctx context.Context, configPath, root string, client *http.Client, now time.Time, scheduled bool) (result error) {
	if err := os.MkdirAll(root, 0750); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(root, ".worker.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return errors.New("podcast worker busy")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	status := func(state string) error {
		n, size := cached(root)
		if previous := ReadStatus(root); previous != nil && previous.State == state && previous.ReadyEpisodes == n && previous.Bytes == size {
			return nil
		}
		return publish(filepath.Join(root, "status.json"), Status{state, n, size, now.UnixMilli()})
	}
	defer func() {
		if result != nil {
			_ = status("error")
		}
	}()
	c, err := podcastconfig.Load(configPath)
	if err != nil {
		return errors.New("podcast subscriptions unavailable")
	}
	library := filepath.Join(root, "library")
	if err = os.MkdirAll(library, 0755); err != nil {
		return err
	}
	oldCatalog, loadErr := books.Load(library)
	if loadErr != nil && !os.IsNotExist(loadErr) {
		return errors.New("podcast catalog unavailable")
	}
	if os.IsNotExist(loadErr) {
		oldCatalog = books.Catalog{Schema: 1, Books: []books.Book{}}
	}
	// Only artifacts still referenced by the old catalog survive interrupted work.
	if err = books.ReapPodcasts(library); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".refresh-") {
			if err = os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return err
			}
		}
	}
	sources := map[string]podcastconfig.Source{}
	for _, sub := range c.Subscriptions {
		if sub.Feed == podcastconfig.Feed {
			sources[sub.Feed] = podcastconfig.Source{ID: podcastconfig.Feed, URL: podcastconfig.FeedURL, Title: "Brains On!"}
		}
	}
	for _, source := range c.Feeds {
		for _, sub := range c.Subscriptions {
			if sub.Feed == source.ID {
				sources[source.ID] = source
				break
			}
		}
	}
	ids := []string{}
	for id := range sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// Feed HTTP caches are disposable and belong to the same bounded pool.
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".feed-") {
			id := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), ".feed-"), ".json")
			if _, ok := sources[id]; !ok {
				if err = os.Remove(filepath.Join(root, entry.Name())); err != nil {
					return err
				}
			}
		}
	}
	next := []books.Book{}
	failed := false
	for _, id := range ids {
		source := sources[id]
		previous := []books.Book{}
		for _, book := range oldCatalog.Books {
			feed := book.Feed
			if feed == "" {
				feed = podcastconfig.Feed
			}
			if feed == id && books.Ready(library, book) {
				previous = append(previous, book)
			}
		}
		selected, e := refreshSource(ctx, client, root, library, source, previous, now, scheduled)
		if e != nil {
			failed = true
		}
		next = append(next, selected...)
	}
	// New/retained episodes retain the same feed + publisher order, independent
	// of which ones required conversion during this pass.
	encoded, _ := json.Marshal(next)
	oldEncoded, _ := json.Marshal(oldCatalog.Books)
	if !bytes.Equal(encoded, oldEncoded) || loadErr != nil {
		if len(next) == 0 {
			if err = publish(filepath.Join(library, "catalog.json"), books.Catalog{Schema: 1, Books: next}); err != nil {
				return err
			}
			if err = os.Chmod(filepath.Join(library, "catalog.json"), 0644); err != nil {
				return err
			}
		} else {
			stage, e := os.MkdirTemp(root, ".refresh-")
			if e != nil {
				return e
			}
			defer os.RemoveAll(stage)
			path := filepath.Join(stage, "catalog.json")
			if err = publish(path, books.Catalog{Schema: 1, Books: []books.Book{}}); err != nil {
				return err
			}
			if err = books.PreparePodcasts(ctx, path, library, next); err != nil {
				return errors.New("podcast catalog publication failed")
			}
		}
	}
	if err = books.ReapPodcasts(library); err != nil {
		return err
	}
	if failed {
		return errors.New("one or more podcast feeds unavailable; other feeds refreshed")
	}
	if len(c.Subscriptions) == 0 {
		return status("idle")
	}
	return status("ready")
}

// Failures are recorded per source. One bad publisher/episode cannot hold other
// shows back; its verified last-good episodes fill remaining slots.
func refreshSource(ctx context.Context, client *http.Client, root, library string, source podcastconfig.Source, previous []books.Book, now time.Time, scheduled bool) (selected []books.Book, result error) {
	statePath := filepath.Join(root, ".feed-"+source.ID+".json")
	state := readFeedState(statePath)
	waiting := scheduled && now.Unix() < state.NextCheck
	if waiting && state.Failed {
		return previous, errors.New("podcast feed retry waiting")
	}
	if waiting && state.Title == source.Title && len(previous) > 0 {
		return previous, nil
	}

	defer func() {
		if result != nil && !waiting {
			state.NextCheck = now.Add(5 * time.Minute).Unix()
		}
		state.Failed = result != nil
		if err := saveFeedState(root, statePath, state); err != nil && result == nil {
			result = errors.New("podcast feed cache unavailable")
		}
	}()
	if source.ID == podcastconfig.Feed && len(previous) == 2 {
		return previous, nil
	}
	var parsed *gofeed.Feed
	body, err := loadFeed(ctx, client, source.URL, state, now, scheduled)
	if err == nil {
		parsed, err = gofeed.NewParser().Parse(strings.NewReader(body))
	}
	if err != nil || len(parsed.Items) > 5000 {
		result = errors.New("podcast feed unavailable")
		if !waiting {
			state.NextCheck = now.Add(5 * time.Minute).Unix()
		}
		return previous, result
	}
	state.Body = body
	if state.NoStore {
		state.Body = ""
	}
	state.Title = source.Title
	if !waiting {
		state.NextCheck = now.Add(time.Hour).Unix()
	}
	candidates, err := selectEpisodes(parsed, source.ID)
	if err != nil {
		result = err
		return previous, result
	}
	selected = []books.Book{}
	attempted := 0
	for _, item := range candidates {
		identity := item.GUID
		if identity == "" {
			identity = item.Link
		}
		episode := episodeIDFor(source.ID, identity)
		signature := episodeSourceFor(source, item)
		reused := false
		for _, b := range previous {
			if b.ID == episode && b.SourceHash == signature {
				selected = append(selected, b)
				reused = true
				break
			}
		}
		if reused {
			if len(selected) == 2 {
				break
			}
			continue
		}
		if attempted == 4 {
			break
		}
		attempted++
		b, e := prepareEpisode(ctx, client, root, library, source, item, episode, signature)
		if e != nil {
			result = errors.New("podcast episode unavailable")
			continue
		}
		selected = append(selected, b)
		if len(selected) == 2 {
			break
		}
	}
	for _, b := range previous {
		if len(selected) == 2 {
			break
		}
		found := false
		for _, v := range selected {
			found = found || v.ID == b.ID
		}
		if !found {
			selected = append(selected, b)
		}
	}
	if result != nil {
		if !waiting {
			state.NextCheck = now.Add(5 * time.Minute).Unix()
		}
	}
	return selected, result
}
func prepareEpisode(ctx context.Context, client *http.Client, root, library string, source podcastconfig.Source, item *gofeed.Item, id, signature string) (books.Book, error) {
	b := books.Book{}
	used, err := diskBytes(root)
	if err != nil || used > MaxCacheBytes-(64<<20) {
		return b, errors.New("podcast pool needs download space")
	}
	stage, err := os.MkdirTemp(root, ".refresh-")
	if err != nil {
		return b, err
	}
	defer os.RemoveAll(stage)
	audio := filepath.Join(stage, "episode.source")
	f, err := os.Create(audio)
	if err != nil {
		return b, err
	}
	err = fetch(ctx, client, item.Enclosures[0].URL, f, 64<<20)
	closeErr := f.Close()
	if err != nil {
		return b, err
	}
	if closeErr != nil {
		return b, closeErr
	}
	// The source is already charged in used. Reserve only one output + prompts,
	// not 68 MiB multiplied by every episode in the complete catalog.
	used, err = diskBytes(root)
	if err != nil || used > MaxCacheBytes-(68<<20) {
		return b, errors.New("podcast pool needs preparation space")
	}
	title := strings.Join(strings.Fields(item.Title), " ")
	spoken := title
	if source.ID != podcastconfig.Feed {
		spoken = source.Title + ": " + title
	}
	for len(spoken) > 240 {
		_, n := utf8.DecodeLastRuneInString(spoken)
		spoken = spoken[:len(spoken)-n]
	}
	b = books.Book{ID: id, Feed: source.ID, SourceHash: signature, Title: spoken, Language: "en", Origin: "podcast", Chapters: []books.Chapter{{Title: spoken, Audio: audio}}}
	return books.StagePodcast(ctx, library, b)
}
func selectEpisodes(feed *gofeed.Feed, id string) ([]*gofeed.Item, error) {
	selected := []*gofeed.Item{}
	seen := map[string]bool{}
	items := feed.Items
	if id == podcastconfig.Feed {
		items = nil
		for _, guid := range pilotGUIDs {
			for _, item := range feed.Items {
				if item.GUID == guid {
					items = append(items, item)
					break
				}
			}
		}
	}
	for _, item := range items {
		identity := item.GUID
		if identity == "" {
			identity = item.Link
		}
		title := strings.Join(strings.Fields(item.Title), " ")
		if identity == "" || len(identity) > 2048 || seen[identity] || len(title) == 0 || len(title) > 240 || len(item.Enclosures) == 0 || publicURL(item.Enclosures[0].URL) != nil {
			continue
		}
		if item.ITunesExt != nil && item.ITunesExt.EpisodeType != "" && item.ITunesExt.EpisodeType != "full" {
			continue
		}
		seen[identity] = true
		selected = append(selected, item)
	}
	if len(selected) == 0 {
		return nil, errors.New("no eligible podcast episodes")
	}
	return selected, nil
}
func episodeSource(item *gofeed.Item) string { return episodeSourceFor(podcastconfig.Source{}, item) }
func episodeSourceFor(source podcastconfig.Source, item *gofeed.Item) string {
	u, _ := url.Parse(item.Enclosures[0].URL)
	query := u.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if lower == "signature" || lower == "token" || lower == "expires" || lower == "policy" || lower == "key-pair-id" || strings.HasPrefix(lower, "x-amz-") {
			query.Del(key)
		}
	}
	u.RawQuery = query.Encode()
	u.ForceQuery = false
	encoded, _ := json.Marshal([]string{source.Title, item.Title, item.GUID, item.Link, item.Published, item.Updated, u.String(), item.Enclosures[0].Length, item.Enclosures[0].Type})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
