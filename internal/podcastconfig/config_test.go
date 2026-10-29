package podcastconfig

import (
	"bytes"
	"testing"
)

func TestSubscriptionIsolation(t *testing.T) {
	first, err := Select(nil, "kitchen", Feed, true)
	if err != nil {
		t.Fatal(err)
	}
	both, err := Select(first, "bedroom", Feed, true)
	if err != nil {
		t.Fatal(err)
	}
	replay, _ := Select(both, "kitchen", Feed, true)
	if !bytes.Equal(both, replay) {
		t.Fatal("retry changed configuration")
	}
	next, _ := Select(both, "kitchen", Feed, false)
	c, err := Parse(next)
	if err != nil || c.Allows("kitchen") || !c.Allows("bedroom") {
		t.Fatal("unsubscribe affected other handset")
	}
	for _, data := range []string{`{}`, `{"schema":1,"subscriptions":null}`, `{"schema":1}`, `{"schema":2,"subscriptions":[]}`, `{"schema":1,"subscriptions":[{"handset":"kitchen","feed":"other"}]}`, `{"schema":1,"subscriptions":[],"token":"secret"}`, `{} {}`} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatalf("accepted invalid config: %s", data)
		}
	}
}

func TestGenericFeedsAndPilotCoexist(t *testing.T) {
	url := "https://podcasts.example.org/show.xml"
	id, err := IDForURL(url)
	if err != nil {
		t.Fatal(err)
	}
	source := Source{ID: id, URL: url, Title: "Nature"}
	data, err := SelectFeed(nil, "kitchen", source, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err = SelectFeed(data, "bedroom", source, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err = Select(data, "kitchen", Feed, true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data)
	if err != nil || c.Schema != 2 || len(c.Feeds) != 1 || len(c.Subscriptions) != 3 {
		t.Fatalf("config %+v %v", c, err)
	}
	again, err := SelectFeed(data, "kitchen", source, true)
	if err != nil || !bytes.Equal(data, again) {
		t.Fatal("not idempotent", err)
	}
	data, err = SelectFeed(data, "kitchen", source, false)
	if err != nil {
		t.Fatal(err)
	}
	c, _ = Parse(data)
	if c.AllowsFeed("kitchen", id) || !c.AllowsFeed("bedroom", id) || !c.AllowsFeed("kitchen", Feed) {
		t.Fatal("unsubscribe isolation")
	}
	data, err = SelectFeed(data, "bedroom", source, false)
	if err != nil {
		t.Fatal(err)
	}
	c, _ = Parse(data)
	if c.Schema != 1 || len(c.Feeds) != 0 || !c.AllowsFeed("kitchen", "") {
		t.Fatal("pilot compatibility")
	}
}
func TestGenericURLIdentityAndBoundaries(t *testing.T) {
	canonical, err := CanonicalURL("https://PODCASTS.example.org:443/feed")
	if err != nil || canonical != "https://podcasts.example.org/feed" {
		t.Fatal(canonical, err)
	}
	a, _ := IDForURL(canonical)
	b, _ := IDForURL("https://PODCASTS.example.org:443/feed")
	if a != b {
		t.Fatal("unstable identity")
	}
	for _, raw := range []string{"http://example.org/rss", "https://user:pass@example.org/rss", "https://example.org/rss?token=x", "https://example.org/rss#x", "https://127.0.0.1/rss", "https://100.64.0.1/rss", "https://[::ffff:127.0.0.1]/rss", "https://[64:ff9b::a00:1]/rss", "https://example.local/rss", "https://example.org:444/rss", "https://localhost/rss", "https://example.org/rss?"} {
		if _, err := CanonicalURL(raw); err == nil {
			t.Fatal("unsafe feed accepted", raw)
		}
	}
	id, _ := IDForURL("https://example.org/feed")
	for _, s := range []Source{{ID: "wrong", URL: "https://example.org/feed", Title: "Show"}, {ID: id, URL: "https://example.org/feed", Title: ""}, {ID: id, URL: "https://example.org/feed", Title: "bad\nname"}} {
		if s.Valid() {
			t.Fatal("invalid source accepted")
		}
	}
}

func TestDirectoryIdentityVector(t *testing.T) {
	id, err := IDForURL("https://podcasts.example.org/nature.xml")
	if err != nil || id != "feed-4c1ee30ad0f2e1c75a4f791962f28521" {
		t.Fatal("cross-language identity drift", id, err)
	}
}
