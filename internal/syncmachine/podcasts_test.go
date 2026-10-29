package syncmachine

import (
	"callmemaybe/internal/podcastconfig"
	"callmemaybe/internal/syncwire"
	"os"
	"path/filepath"
	"testing"
)

func TestPodcastSubscriptionSync(t *testing.T) {
	m, p, _ := fixture(t)
	p.Podcasts = filepath.Join(filepath.Dir(p.Policy), "subscriptions.json")
	m.paths = p
	s, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if s.Configuration.Podcasts == nil || len(*s.Configuration.Podcasts) != 0 {
		t.Fatal("missing supported empty projection")
	}
	verb := map[string]any{"name": "podcast.subscribe", "handset": "kitchen", "feed": "brains-on", "enabled": true}
	q := proposal(t, s, verb)
	r, err := m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusApplied {
		t.Fatalf("receipt %+v %v", r, err)
	}
	c, err := podcastconfig.Load(p.Podcasts)
	if err != nil || !c.Allows("kitchen") {
		t.Fatal("subscription not persisted")
	}
	r, err = m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusApplied {
		t.Fatalf("retry %+v %v", r, err)
	}
	s, err = m.Observe()
	if err != nil || s.Revision == q.Base.Revision {
		t.Fatal("subscription did not advance revision")
	}
	verb["handset"] = "unknown"
	r, err = m.Apply(proposal(t, s, verb))
	if err != nil || r.Status != syncwire.ReceiptStatusRejected {
		t.Fatalf("unknown handset %+v %v", r, err)
	}
	stale := proposal(t, s, map[string]any{"name": "podcast.subscribe", "handset": "kitchen", "feed": "brains-on", "enabled": false})
	changed, _ := podcastconfig.Select(nil, "kitchen", "brains-on", false)
	if err = os.WriteFile(p.Podcasts, changed, 0600); err != nil {
		t.Fatal(err)
	}
	r, err = m.Apply(stale)
	if err != nil || r.Status != syncwire.ReceiptStatusConflict {
		t.Fatalf("stale %+v %v", r, err)
	}
}

func TestGenericPodcastSyncAndConflict(t *testing.T) {
	m, p, _ := fixture(t)
	p.Podcasts = filepath.Join(filepath.Dir(p.Policy), "subscriptions.json")
	m.paths = p
	s, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	url := "https://podcasts.example.org/nature.xml"
	id, _ := podcastconfig.IDForURL(url)
	source := map[string]any{"id": id, "url": url, "title": "Nature"}
	verb := map[string]any{"name": "podcast.subscribeFeed", "handset": "kitchen", "feed": source, "enabled": true}
	q := proposal(t, s, verb)
	r, err := m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusApplied {
		t.Fatalf("generic %+v %v", r, err)
	}
	c, err := podcastconfig.Load(p.Podcasts)
	if err != nil || !c.AllowsFeed("kitchen", id) || c.Schema != 2 {
		t.Fatal("generic not saved", err)
	}
	r, err = m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusApplied {
		t.Fatal("retry", err)
	}
	s, err = m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if s.Configuration.PodcastFeeds == nil || len(*s.Configuration.PodcastFeeds) != 1 {
		t.Fatal("missing source projection")
	}
	source["url"] = "https://127.0.0.1/private"
	r, err = m.Apply(proposal(t, s, verb))
	if err != nil || r.Status != syncwire.ReceiptStatusRejected {
		t.Fatal("invalid URL applied", err)
	}
}

func TestOptionalPodcastsDoNotBlockOtherSync(t *testing.T) {
	for _, data := range []string{`{"schema":1,"subscriptions":[{"handset":"retired","feed":"brains-on"}]}`, `{broken`} {
		t.Run(data, func(t *testing.T) {
			m, p, _ := fixture(t)
			p.Podcasts = filepath.Join(filepath.Dir(p.Policy), "subscriptions.json")
			m.paths = p
			if err := os.WriteFile(p.Podcasts, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := m.Observe()
			if err != nil {
				t.Fatal("optional file blocked sync", err)
			}
			if data == `{broken` && s.Configuration.Podcasts != nil {
				t.Fatal("corrupt file advertised")
			}
			if data != `{broken` && (s.Configuration.Podcasts == nil || len(*s.Configuration.Podcasts) != 0) {
				t.Fatal("stale handset projected")
			}
			q := proposal(t, s, map[string]any{"name": "policy.setSchedule", "policy": "default", "schedule": map[string]any{"id": "bedtime", "start": "22:00", "end": "07:00", "days": []string{"MO", "TU"}, "enabled": true}})
			r, err := m.Apply(q)
			if err != nil || r.Status != syncwire.ReceiptStatusSaved {
				t.Fatal("unrelated change blocked", r, err)
			}
			after, _ := os.ReadFile(p.Podcasts)
			if string(after) != data {
				t.Fatal("unrelated apply rewrote podcasts")
			}
		})
	}
}
func TestPodcastSyncReadAccessForAGI(t *testing.T) {
	m, p, _ := fixture(t)
	p.Podcasts = filepath.Join(filepath.Dir(p.Policy), "subscriptions.json")
	m.paths = p
	s, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	v := map[string]any{"name": "podcast.subscribe", "handset": "kitchen", "feed": "brains-on", "enabled": true}
	r, err := m.Apply(proposal(t, s, v))
	if err != nil || r.Status != syncwire.ReceiptStatusApplied {
		t.Fatal(r, err)
	}
	st, err := os.Stat(p.Podcasts)
	if err != nil || st.Mode().Perm() != 0644 {
		t.Fatal("AGI cannot read sync-created metadata", st, err)
	}
	_ = os.Chmod(p.Podcasts, 0640)
	s, _ = m.Observe()
	v["enabled"] = false
	r, err = m.Apply(proposal(t, s, v))
	if err != nil || r.Status != syncwire.ReceiptStatusApplied {
		t.Fatal(r, err)
	}
	st, _ = os.Stat(p.Podcasts)
	if st.Mode().Perm() != 0644 {
		t.Fatal("AGI cannot read replaced metadata")
	}
}
