package voicemail

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func serveFixture(t *testing.T) (*Server, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	tok, _ := NewToken(nil)
	os.WriteFile(filepath.Join(dir, tok+".wav"), []byte("RIFF....WAVEfmt pcm-bytes"), 0o640)
	var logs bytes.Buffer
	return &Server{Dir: dir, Log: slog.New(slog.NewTextHandler(&logs, nil))}, tok, &logs
}

func get(s http.Handler, path, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestAValidTokenServesTheRecordingPrivately(t *testing.T) {
	s, tok, logs := serveFixture(t)
	w := get(s, "/v/"+tok, "100.101.102.103:51234", nil)
	if w.Code != 200 || w.Body.String() != "RIFF....WAVEfmt pcm-bytes" {
		t.Fatalf("code %d body %q", w.Code, w.Body.String())
	}
	for k, v := range map[string]string{
		"Content-Type":           "audio/wav",
		"Cache-Control":          "no-store",
		"Referrer-Policy":        "no-referrer",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// Safari will not play audio from a server that ignores Range.
	rw := get(s, "/v/"+tok, "192.168.7.20:1", map[string]string{"Range": "bytes=0-3"})
	if rw.Code != http.StatusPartialContent || rw.Body.String() != "RIFF" {
		t.Errorf("range: %d %q", rw.Code, rw.Body.String())
	}
	// A browser opening the link from a mail gets a page that plays it.
	page := get(s, "/v/"+tok, "[fd7a:115c:a1e0::1]:443", map[string]string{"Accept": "text/html,application/xhtml+xml,*/*;q=0.8"})
	if page.Code != 200 || !strings.HasPrefix(page.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(page.Body.String(), `<audio controls preload="metadata" src="/v/`+tok+`?audio"`) ||
		!strings.Contains(page.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("page: %d %q %q", page.Code, page.Header(), page.Body.String())
	}
	if a := get(s, "/v/"+tok+"?audio", "127.0.0.1:1", map[string]string{"Accept": "text/html"}); a.Header().Get("Content-Type") != "audio/wav" {
		t.Errorf("?audio must be the recording whatever Accept says: %q", a.Header().Get("Content-Type"))
	}
	if strings.Contains(logs.String(), tok) || strings.Contains(logs.String(), "100.101") || strings.Contains(logs.String(), "192.168") {
		t.Errorf("a token or an address reached the log: %s", logs.String())
	}
}

func TestUnknownAndMalformedTokens(t *testing.T) {
	s, _, _ := serveFixture(t)
	opened := 0
	s.open = func(p string) (*os.File, error) { opened++; return os.Open(p) }
	other, _ := NewToken(nil)
	if w := get(s, "/v/"+other, "10.0.0.5:1", nil); w.Code != 404 {
		t.Errorf("unknown token = %d", w.Code)
	}
	if opened != 1 {
		t.Fatalf("an unknown, well-formed token should be looked up once, was %d", opened)
	}
	opened = 0
	for _, p := range []string{
		"/", "/v/", "/v", "/index.html", "/v/short", "/v/../../etc/passwd",
		"/v/%2e%2e%2f%2e%2e%2fetc%2fpasswd", "/v/" + other + "/x", "/v/" + other + ".wav",
		"/V/" + other, "/links/" + other + ".wav",
	} {
		if w := get(s, p, "10.0.0.5:1", nil); w.Code != 404 {
			t.Errorf("%s = %d, want 404", p, w.Code)
		}
	}
	if opened != 0 {
		t.Errorf("a malformed path touched the filesystem %d times", opened)
	}
	r := httptest.NewRequest(http.MethodPost, "/v/"+other, nil)
	r.RemoteAddr = "10.0.0.5:1"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed || opened != 0 {
		t.Errorf("POST = %d (opened %d)", w.Code, opened)
	}
	// A directory under a token's name is not a recording.
	dirTok, _ := NewToken(nil)
	os.Mkdir(filepath.Join(s.Dir, dirTok+".wav"), 0o750)
	if w := get(s, "/v/"+dirTok, "10.0.0.5:1", nil); w.Code != 404 {
		t.Errorf("directory = %d", w.Code)
	}
}

func TestOnlyTheHouseholdsOwnNetworksAreServed(t *testing.T) {
	s, tok, _ := serveFixture(t)
	s.local = func() ([]net.Addr, error) { return nil, nil } // a box with no global IPv6
	opened := 0
	s.open = func(p string) (*os.File, error) { opened++; return os.Open(p) }
	allowed := []string{
		"127.0.0.1:5000", "[::1]:5000", "10.1.2.3:1", "172.16.0.9:1", "172.31.255.255:1",
		"192.168.7.47:1", "169.254.10.10:1", "[fe80::1%eth0]:1", "[fd12:3456::1]:1",
		"100.64.0.1:1", "100.127.255.254:1", "[fd7a:115c:a1e0:ab12::1]:1", "[::ffff:192.168.1.10]:1",
	}
	for _, a := range allowed {
		if w := get(s, "/v/"+tok, a, nil); w.Code != 200 {
			t.Errorf("%s = %d, want 200", a, w.Code)
		}
	}
	refused := []string{
		"8.8.8.8:1", "1.1.1.1:443", "172.32.0.1:1", "100.128.0.1:1", "192.169.0.1:1",
		"[2001:4860:4860::8888]:1", "[::ffff:8.8.8.8]:1", "@", "", "not-an-address",
	}
	opened = 0
	for _, a := range refused {
		w := get(s, "/v/"+tok, a, map[string]string{"X-Forwarded-For": "192.168.1.1", "X-Real-IP": "127.0.0.1"})
		if w.Code != 403 {
			t.Errorf("%q = %d, want 403", a, w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%q: a refusal without the headers", a)
		}
	}
	if opened != 0 {
		t.Errorf("a refused request touched the filesystem %d times", opened)
	}
}

// A LAN with IPv6: the phone and the box both hold global addresses in the
// same /64, and a .local name can resolve to the box's. The phone is the
// household; a global address outside the box's own /64s is not.
func TestAnIPv6LANIsTheHouseholdsOwnNetwork(t *testing.T) {
	s, tok, _ := serveFixture(t)
	mustNet := func(cidr string) net.Addr {
		ip, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		return n
	}
	s.local = func() ([]net.Addr, error) {
		return []net.Addr{
			mustNet("127.0.0.1/8"), mustNet("::1/128"), mustNet("192.168.7.133/24"),
			mustNet("fe80::1/64"), mustNet("100.69.157.72/32"),
			mustNet("2a02:1234:5678:9a00::85/128"), // a DHCPv6 address: the /64 around it is the LAN
			mustNet("2600:1700:abcd:ef01:1::1/64"), // SLAAC on a second segment
		}, nil
	}
	for _, a := range []string{"[2a02:1234:5678:9a00:1c3e:77ff:fe01:2]:51000", "[2600:1700:abcd:ef01::42]:1", "[2a02:1234:5678:9a00::85]:1"} {
		if w := get(s, "/v/"+tok, a, nil); w.Code != 200 {
			t.Errorf("%s = %d, want 200", a, w.Code)
		}
	}
	for _, a := range []string{"[2a02:1234:5678:9a01::1]:1", "[2600:1700:abcd:ef02::1]:1", "[2001:4860:4860::8888]:1", "8.8.8.8:1", "[ff02::1]:1"} {
		if w := get(s, "/v/"+tok, a, nil); w.Code != 403 {
			t.Errorf("%s = %d, want 403", a, w.Code)
		}
	}
	s.local = func() ([]net.Addr, error) { return nil, errors.New("no interfaces") }
	if w := get(s, "/v/"+tok, "[2a02:1234:5678:9a00::2]:1", nil); w.Code != 403 {
		t.Errorf("with no interface list = %d, want 403", w.Code)
	}
}

// The real listener, end to end over loopback: what httptest.Server's
// address is, which is the point — the remote check sees a real RemoteAddr.
func TestTheHTTPServerServesOverLoopback(t *testing.T) {
	s, tok, _ := serveFixture(t)
	srv := NewHTTPServer("127.0.0.1:0", s)
	if srv.ReadHeaderTimeout == 0 || srv.MaxHeaderBytes == 0 {
		t.Error("a LAN listener without header limits")
	}
	ts := httptest.NewUnstartedServer(srv.Handler)
	ts.Config = srv
	ts.Start()
	defer ts.Close()
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(ts.URL + "/v/" + tok)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "RIFF") {
		t.Errorf("%d %q", resp.StatusCode, body)
	}
}
