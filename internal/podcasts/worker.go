// Package podcasts prepares the small, explicit pilot outside the calling process.
package podcasts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
	if json.Unmarshal(data, &s) != nil || s.ReadyEpisodes < 0 || s.ReadyEpisodes > 2 || s.Bytes < 0 || s.Bytes > MaxCacheBytes {
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
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return errors.New("podcast requires public HTTPS")
	}
	return nil
}
func publicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}
func Client() *http.Client {
	transport := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DisableCompression: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" {
			return nil, errors.New("invalid podcast address")
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("podcast DNS unavailable")
		}
		for _, ip := range ips {
			if !publicIP(ip.IP) {
				return nil, errors.New("podcast address is not public")
			}
		}
		d := net.Dialer{Timeout: 15 * time.Second}
		for _, ip := range ips {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, errors.New("podcast server unavailable")
	}}
	return &http.Client{Transport: transport, Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 12 {
			return errors.New("too many podcast redirects")
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
func episodeID(guid string) string {
	sum := sha256.Sum256([]byte(podcastconfig.Feed + "\x00" + guid))
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
	if n, _ := cached(root); n == len(pilotGUIDs) {
		return status("ready")
	}
	if err = status("preparing"); err != nil {
		return err
	}
	// The lock proves no other worker owns these interrupted staging directories.
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
	used, err := diskBytes(root)
	if err != nil || used > MaxCacheBytes-(272<<20) {
		return errors.New("podcast pool needs space")
	}
	stage, err := os.MkdirTemp(root, ".refresh-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	feedFile, err := os.Create(filepath.Join(stage, "feed.xml"))
	if err != nil {
		return err
	}
	job, cancel := context.WithTimeout(ctx, time.Minute)
	err = fetch(job, client, podcastconfig.FeedURL, feedFile, 8<<20)
	cancel()
	_ = feedFile.Close()
	if err != nil {
		return err
	}
	feedFile, err = os.Open(filepath.Join(stage, "feed.xml"))
	if err != nil {
		return err
	}
	feed, err := gofeed.NewParser().Parse(feedFile)
	_ = feedFile.Close()
	if err != nil {
		return errors.New("invalid podcast feed")
	}
	catalog := books.Catalog{Schema: 1, Books: []books.Book{}}
	for _, guid := range pilotGUIDs {
		var item *gofeed.Item
		for _, candidate := range feed.Items {
			if candidate.GUID == guid {
				item = candidate
				break
			}
		}
		if item == nil || len(item.Enclosures) == 0 {
			return errors.New("pilot episode missing from feed")
		}
		title := strings.Join(strings.Fields(item.Title), " ")
		if len(title) == 0 || len(title) > 240 {
			return errors.New("invalid podcast title")
		}
		source := filepath.Join(stage, episodeID(guid)+".source")
		f, err := os.Create(source)
		if err != nil {
			return err
		}
		err = fetch(ctx, client, item.Enclosures[0].URL, f, 64<<20)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		catalog.Books = append(catalog.Books, books.Book{ID: episodeID(guid), Title: title, Language: "en", Origin: "podcast", Chapters: []books.Chapter{{Title: title, Audio: source}}})
	}
	source := filepath.Join(stage, "catalog.json")
	if err = publish(source, catalog); err != nil {
		return err
	}
	if err = books.Prepare(ctx, source, filepath.Join(root, "library")); err != nil {
		return fmt.Errorf("podcast preparation: %w", err)
	}
	if n, _ := cached(root); n != len(pilotGUIDs) {
		return errors.New("podcast artifacts or prompts unavailable")
	}
	return status("ready")
}
