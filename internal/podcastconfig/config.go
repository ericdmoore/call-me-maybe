// Package podcastconfig owns opt-in subscriptions, independently of downloads
// and the calling daemon. Schema 1 and the pinned pilot remain compatible.
package podcastconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const Capability = "podcast.subscribe"
const FeedCapability = "podcast.subscribeFeed"
const Feed = "brains-on"
const FeedURL = "https://feeds.megaphone.fm/LEME2532531912"
const MaxFeeds = 8

var handsetID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type Subscription struct {
	Handset string `json:"handset"`
	Feed    string `json:"feed"`
}
type Source struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
}
type Config struct {
	Schema        int            `json:"schema"`
	Subscriptions []Subscription `json:"subscriptions"`
	Feeds         []Source       `json:"feeds,omitempty"`
}

var invalid = errors.New("invalid podcast subscriptions")

// CanonicalURL is intentionally narrower than enclosure URLs: private/tokenized
// feeds are not supported. DNS is checked again at the network boundary.
func CanonicalURL(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > 2048 || strings.ContainsFunc(raw, unicode.IsControl) {
		return "", invalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return "", invalid
	}
	host := strings.ToLower(u.Hostname())
	if strings.IndexFunc(host, func(r rune) bool { return r > 127 }) >= 0 {
		return "", invalid
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return "", invalid
		}
	}
	if strings.HasSuffix(host, ".") || !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		return "", invalid
	}
	for _, suffix := range []string{"localhost", "local", "internal", "invalid", "test"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return "", invalid
		}
	}
	if ip, err := netip.ParseAddr(host); err == nil && !PublicIP(ip) {
		return "", invalid
	}
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}
func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return false
		}
	}
	return true
}
func IDForURL(raw string) (string, error) {
	canonical, err := CanonicalURL(raw)
	if err != nil {
		return "", err
	}
	if canonical == FeedURL {
		return Feed, nil
	}
	sum := sha256.Sum256([]byte(canonical))
	return "feed-" + hex.EncodeToString(sum[:])[:32], nil
}
func (s Source) Valid() bool {
	id, err := IDForURL(s.URL)
	canonical, e := CanonicalURL(s.URL)
	return err == nil && e == nil && canonical == s.URL && s.ID == id && utf8.ValidString(s.Title) && strings.TrimSpace(s.Title) != "" && len(s.Title) <= 240 && !strings.ContainsFunc(s.Title, unicode.IsControl)
}
func Parse(data []byte) (Config, error) {
	if len(data) == 0 {
		return Config{Schema: 1, Subscriptions: []Subscription{}}, nil
	}
	var c Config
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if len(data) > 65536 || d.Decode(&c) != nil {
		return c, invalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF || c.Subscriptions == nil || len(c.Subscriptions) > 100 || (c.Schema != 1 && c.Schema != 2) || len(c.Feeds) > MaxFeeds || c.Schema == 1 && len(c.Feeds) > 0 || c.Schema == 2 && len(c.Feeds) == 0 {
		return c, invalid
	}
	sources := map[string]bool{Feed: true}
	for _, source := range c.Feeds {
		if !source.Valid() || sources[source.ID] {
			return c, invalid
		}
		sources[source.ID] = true
	}
	seen := map[string]bool{}
	for _, sub := range c.Subscriptions {
		key := sub.Handset + "\x00" + sub.Feed
		if !handsetID.MatchString(sub.Handset) || !sources[sub.Feed] || seen[key] {
			return c, invalid
		}
		seen[key] = true
	}
	return c, nil
}
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return Parse(nil)
	}
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 65536 {
		return Config{}, invalid
	}
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return Config{}, err
	}
	return Parse(data)
}
func (c Config) Allows(handset string) bool {
	for _, sub := range c.Subscriptions {
		if sub.Handset == handset {
			return true
		}
	}
	return false
}
func (c Config) AllowsFeed(handset, feed string) bool {
	if feed == "" {
		feed = Feed
	}
	for _, sub := range c.Subscriptions {
		if sub.Handset == handset && sub.Feed == feed {
			return true
		}
	}
	return false
}
func encode(c Config) ([]byte, error) {
	sort.Slice(c.Subscriptions, func(i, j int) bool {
		a, b := c.Subscriptions[i], c.Subscriptions[j]
		if a.Handset == b.Handset {
			return a.Feed < b.Feed
		}
		return a.Handset < b.Handset
	})
	sort.Slice(c.Feeds, func(i, j int) bool { return c.Feeds[i].ID < c.Feeds[j].ID })
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	if _, err = Parse(data); err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
func selectPair(c Config, handset, feed string, enabled bool) Config {
	next := []Subscription{}
	for _, sub := range c.Subscriptions {
		if sub.Handset != handset || sub.Feed != feed {
			next = append(next, sub)
		}
	}
	if enabled {
		next = append(next, Subscription{handset, feed})
	}
	c.Subscriptions = next
	return c
}
func Select(data []byte, handset, feed string, enabled bool) ([]byte, error) {
	c, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if !handsetID.MatchString(handset) || feed != Feed {
		return nil, invalid
	}
	return encode(selectPair(c, handset, feed, enabled))
}
func SelectFeed(data []byte, handset string, source Source, enabled bool) ([]byte, error) {
	c, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if !handsetID.MatchString(handset) || !source.Valid() {
		return nil, invalid
	}
	if source.ID == Feed {
		return Select(data, handset, Feed, enabled)
	}
	c = selectPair(c, handset, source.ID, enabled)
	byID := map[string]Source{}
	for _, f := range c.Feeds {
		byID[f.ID] = f
	}
	if enabled {
		byID[source.ID] = source
	}
	c.Feeds = nil
	for id, f := range byID {
		for _, sub := range c.Subscriptions {
			if sub.Feed == id {
				c.Feeds = append(c.Feeds, f)
				break
			}
		}
	}
	c.Schema = 1
	if len(c.Feeds) > 0 {
		c.Schema = 2
	}
	return encode(c)
}
func Save(path string, data []byte) error {
	if _, err := Parse(data); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".subscriptions-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0644); err == nil {
		_, err = f.Write(data)
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
