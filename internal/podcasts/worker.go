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
func refresh(ctx context.Context, configPath, root string, client *http.Client) (result error) {
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
		n, bytes := cached(root)
		return publish(filepath.Join(root, "status.json"), Status{state, n, bytes, time.Now().UnixMilli()})
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
	if len(c.Subscriptions) == 0 {
		return status("idle")
	}

	// Pinned pilot episodes do not need another feed download once both are ready.
	if len(c.Feeds) == 0 {
		live, loadErr := books.Load(filepath.Join(root, "library"))
		pilotOnly := loadErr == nil && len(live.Books) == len(pilotGUIDs)
		for _, b := range live.Books {
			pilotOnly = pilotOnly && (b.ID == episodeID(pilotGUIDs[0]) || b.ID == episodeID(pilotGUIDs[1])) && (b.Feed == "" || b.Feed == podcastconfig.Feed)
		}
		if n, _ := cached(root); pilotOnly && n == len(pilotGUIDs) {
			return status("ready")
		}
	}
	if err = status("preparing"); err != nil {
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
	library := filepath.Join(root, "library")
	if err = books.ReapPodcasts(library); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(root, ".refresh-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	old := map[string]books.Book{}
	if live, e := books.Load(library); e == nil {
		for _, b := range live.Books {
			old[b.ID] = b
		}
	} else if !os.IsNotExist(e) {
		return errors.New("podcast catalog unavailable")
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
	next := books.Catalog{Schema: 1, Books: []books.Book{}}
	retained := []books.Book{}
	for _, id := range ids {
		source := sources[id]
		var body bytes.Buffer
		job, cancel := context.WithTimeout(ctx, time.Minute)
		err = fetch(context.WithValue(job, feedRequestKey{}, true), client, source.URL, &body, 8<<20)
		cancel()
		if err != nil {
			return err
		}
		parsed, e := gofeed.NewParser().Parse(bytes.NewReader(body.Bytes()))
		if e != nil || len(parsed.Items) > 5000 {
			return errors.New("invalid or oversized podcast feed")
		}
		selected, e := selectEpisodes(parsed, id)
		if e != nil {
			return e
		}
		for _, item := range selected {
			identity := item.GUID
			if identity == "" {
				identity = item.Link
			}
			if identity == "" || len(identity) > 2048 {
				return errors.New("podcast episode needs stable identity")
			}
			episode := episodeIDFor(id, identity)
			signature := episodeSource(item)
			if b, ok := old[episode]; ok && b.SourceHash == signature && books.Ready(library, b) {
				retained = append(retained, b)
				continue
			}
			title := strings.Join(strings.Fields(item.Title), " ")
			if len(title) == 0 || len(title) > 240 {
				return errors.New("invalid podcast title")
			}
			// Include the show in spoken episode titles; menus remain a bounded flat list.
			spoken := title
			if id != podcastconfig.Feed {
				spoken = source.Title + ": " + title
			}
			for len(spoken) > 240 {
				_, n := utf8.DecodeLastRuneInString(spoken)
				spoken = spoken[:len(spoken)-n]
			}
			used, e := diskBytes(root)
			if e != nil || used > MaxCacheBytes-(64<<20) {
				return errors.New("podcast pool needs space")
			}
			audio := filepath.Join(stage, episode+".source")
			f, e := os.Create(audio)
			if e != nil {
				return e
			}
			e = fetch(ctx, client, item.Enclosures[0].URL, f, 64<<20)
			closeErr := f.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
			next.Books = append(next.Books, books.Book{ID: episode, Feed: id, SourceHash: signature, Title: spoken, Language: "en", Origin: "podcast", Chapters: []books.Chapter{{Title: spoken, Audio: audio}}})
			used, e = diskBytes(root)
			if e != nil || used+int64(len(next.Books))*(68<<20) > MaxCacheBytes {
				return errors.New("podcast pool needs preparation space")
			}
		}
	}
	source := filepath.Join(stage, "catalog.json")
	if err = publish(source, next); err != nil {
		return err
	}
	if err = books.PreparePodcasts(ctx, source, library, retained); err != nil {
		return errors.New("podcast preparation failed")
	}
	if n, _ := cached(root); n != len(next.Books)+len(retained) {
		return errors.New("podcast artifacts or prompts unavailable")
	}
	// Publication is complete. Active menus hold a lease; stale editions can wait
	// until the next refresh rather than disrupting playback or deleting bookmarks.
	if err = books.ReapPodcasts(library); err != nil {
		return err
	}
	return status("ready")
}
func selectEpisodes(feed *gofeed.Feed, id string) ([]*gofeed.Item, error) {
	selected := []*gofeed.Item{}
	if id == podcastconfig.Feed {
		for _, guid := range pilotGUIDs {
			for _, item := range feed.Items {
				if item.GUID == guid {
					selected = append(selected, item)
					break
				}
			}
		}
		if len(selected) != len(pilotGUIDs) {
			return nil, errors.New("pilot episode missing from feed")
		}
	} else {
		// Feed order is publisher order. Trailers/bonus items are excluded when tagged;
		// absence of a tag is not a promise that an episode has no advertising.
		seen := map[string]bool{}
		for _, item := range feed.Items {
			identity := item.GUID
			if identity == "" {
				identity = item.Link
			}
			if identity == "" || seen[identity] || len(item.Enclosures) == 0 {
				continue
			}
			if item.ITunesExt != nil && item.ITunesExt.EpisodeType != "" && item.ITunesExt.EpisodeType != "full" {
				continue
			}
			seen[identity] = true
			selected = append(selected, item)
			if len(selected) == 2 {
				break
			}
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("no eligible podcast episodes")
	}
	for _, item := range selected {
		if len(item.Enclosures) == 0 || publicURL(item.Enclosures[0].URL) != nil {
			return nil, errors.New("invalid podcast enclosure")
		}
	}
	return selected, nil
}

func episodeSource(item *gofeed.Item) string {
	encoded, _ := json.Marshal([]string{item.GUID, item.Link, item.Published, item.Updated, item.Enclosures[0].URL, item.Enclosures[0].Length, item.Enclosures[0].Type})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
