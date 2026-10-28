// Package podcastconfig owns the opt-in podcast subscriptions, independently
// of media downloads and the calling daemon. The pilot offers one public feed.
package podcastconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
)

const Capability = "podcast.subscribe"
const Feed = "brains-on"
const FeedURL = "https://feeds.megaphone.fm/LEME2532531912"

var handsetID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type Subscription struct {
	Handset string `json:"handset"`
	Feed    string `json:"feed"`
}
type Config struct {
	Schema        int            `json:"schema"`
	Subscriptions []Subscription `json:"subscriptions"`
}

func Parse(data []byte) (Config, error) {
	c := Config{Schema: 1, Subscriptions: []Subscription{}}
	if len(data) == 0 {
		return c, nil
	}
	c = Config{}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if len(data) > 65536 || d.Decode(&c) != nil {
		return c, errors.New("invalid podcast subscriptions")
	}
	var extra any
	if d.Decode(&extra) != io.EOF || c.Schema != 1 || c.Subscriptions == nil || len(c.Subscriptions) > 100 {
		return c, errors.New("invalid podcast subscriptions")
	}
	seen := map[string]bool{}
	for _, s := range c.Subscriptions {
		if !handsetID.MatchString(s.Handset) || s.Feed != Feed || seen[s.Handset] {
			return c, errors.New("invalid podcast subscription")
		}
		seen[s.Handset] = true
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
		return Config{}, errors.New("invalid podcast subscriptions")
	}
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return Config{}, err
	}
	return Parse(data)
}
func (c Config) Allows(handset string) bool {
	for _, s := range c.Subscriptions {
		if s.Handset == handset && s.Feed == Feed {
			return true
		}
	}
	return false
}
func Select(data []byte, handset, feed string, enabled bool) ([]byte, error) {
	c, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if !handsetID.MatchString(handset) || feed != Feed {
		return nil, errors.New("invalid podcast subscription")
	}
	next := []Subscription{}
	for _, s := range c.Subscriptions {
		if s.Handset != handset {
			next = append(next, s)
		}
	}
	if enabled {
		next = append(next, Subscription{handset, feed})
	}
	c.Subscriptions = next
	sort.Slice(c.Subscriptions, func(i, j int) bool { return c.Subscriptions[i].Handset < c.Subscriptions[j].Handset })
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	if _, err = Parse(out); err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
