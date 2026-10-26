package webhookconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestURLRules(t *testing.T) {
	for _, c := range []Config{{"https://HOME.example:443/", "//hooks/test%20value//"}, {"http://[::1]:8080", ""}, {"", ""}} {
		got, err := Normalise(c)
		if err != nil {
			t.Fatal(err)
		}
		if got.Domain != "" && !strings.HasSuffix(got.URL(), Suffix) {
			t.Fatal(got.URL())
		}
		if c.Domain == "" && got.URL() != "" {
			t.Fatal("disable")
		}
		round, err := Parse(Encode(got))
		if err != nil || round != got {
			t.Fatal("roundtrip", err)
		}
	}
	for _, c := range []Config{{"", "/nonempty"}, {"https://a.example/path", ""}, {"https://user:pass@a.example", ""}, {"https://a.example?q=x", ""}, {"ftp://a.example", ""}, {"http://", ""}, {"https://a.example", "/a/../b"}, {"https://a.example", "%2f"}, {"https://a.example", "%5c"}, {"https://a.example", "%FF"}, {"https://a.example", "%00"}, {"https://a.example", "bad%"}, {"https://a.example", "http://x"}, {"https://a.example", "?q=x"}, {strings.Repeat("a", 1025), ""}} {
		if _, err := Normalise(c); err == nil {
			t.Fatalf("accepted invalid input %#v", c)
		}
	}
	for _, data := range []string{"domain = [", "unknown = 2", strings.Repeat("a", 8193)} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatal("invalid file")
		}
	}
	if NormaliseJSON([]byte(`{"domain":"https://a.example","pathPrefix":"x","secret":"no"}`)) != nil {
		t.Fatal("unknown JSON key")
	}
	if string(NormaliseJSON([]byte(`{"domain":"https://a.example","pathPrefix":"x"}`))) != `{"domain":"https://a.example","pathPrefix":"/x"}` {
		t.Fatal("JSON normalization")
	}
}
func TestStoreReloadRestartAndDisable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "webhook.toml")
	s := OpenStore(path)
	if s.Current().Managed {
		t.Fatal("missing must preserve environment")
	}
	write := func(data []byte) {
		t.Helper()
		if os.WriteFile(path, data, 0600) != nil {
			t.Fatal("write")
		}
	}
	first := Encode(Config{"https://a.example", "/first"})
	write(first)
	if s.Reload() != nil || s.Current().Config.PathPrefix != "/first" {
		t.Fatal("load")
	}
	if s.Reload() != nil {
		t.Fatal("same")
	}
	info, _ := os.Stat(path + ".last-good")
	if info.Mode().Perm() != 0600 {
		t.Fatal("private fallback")
	}
	write([]byte("broken = ["))
	if s.Reload() == nil {
		t.Fatal("invalid accepted")
	}
	restarted := OpenStore(path)
	if restarted.Current() != s.Current() {
		t.Fatal("lost last good on restart")
	}
	write(Encode(Config{}))
	if s.Reload() != nil || !s.Current().Managed || s.Current().Config.URL() != "" {
		t.Fatal("disable")
	}
	os.Remove(path)
	if s.Reload() != nil || s.Current().Managed {
		t.Fatal("restore legacy")
	}
	if _, err := os.Stat(path + ".last-good"); !os.IsNotExist(err) {
		t.Fatal("stale fallback")
	}
	os.Mkdir(path, 0700)
	if s.Reload() == nil {
		t.Fatal("directory accepted")
	}
	os.Remove(path)
	write([]byte(strings.Repeat("x", 8193)))
	if s.Reload() == nil {
		t.Fatal("oversized")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Watch(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watch shutdown")
	}
}
