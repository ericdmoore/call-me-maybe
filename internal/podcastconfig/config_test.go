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
