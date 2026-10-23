package voicemail

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"
)

// Server answers GET /v/<token> and nothing else: the recording as
// audio/wav, or — to a browser asking for a page — a page with one <audio>
// element. The link is only as private as the network it is reachable on,
// so the server adds one fence of its own: a request from an address that
// is not loopback, private, link-local or the tailnet is refused before
// the path is even read, which turns a mistaken port forward into a 403
// rather than a public recording. X-Forwarded-For is never consulted —
// nothing proxies this, and a header is the caller's to write.
type Server struct {
	Dir string
	Log *slog.Logger
	// open is os.Open; a test swaps it to prove a malformed token never
	// reaches the filesystem.
	open func(string) (*os.File, error)
}

// privateRanges are the networks a household's own devices arrive from.
var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fc00::/7"),            // IPv6 unique local
	netip.MustParsePrefix("100.64.0.0/10"),       // CGNAT, which is where Tailscale numbers its nodes
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"), // Tailscale's IPv6 (inside fc00::/7; named for the reader)
}

// Private reports whether a request's RemoteAddr is one of the household's
// own networks.
func Private(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap().WithZone("")
	for _, p := range privateRanges {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	if !Private(r.RemoteAddr) {
		s.log().Warn("voicemail link refused: request from outside the home network and tailnet")
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/v/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	path, ok := LinkPath(s.Dir, rest)
	if !ok {
		http.NotFound(w, r)
		return
	}
	open := s.open
	if open == nil {
		open = os.Open
	}
	f, err := open(path)
	if err != nil {
		s.log().Info("voicemail link not found (expired, or never made)")
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if !r.URL.Query().Has("audio") && wantsHTML(r) {
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Security-Policy", "default-src 'none'; media-src 'self'; style-src 'unsafe-inline'")
		// rest passed ValidToken: 22 characters of [A-Za-z0-9_-], nothing to escape.
		fmt.Fprintf(w, page, rest, rest)
		return
	}
	h.Set("Content-Type", "audio/wav")
	h.Set("Content-Disposition", `inline; filename="voicemail.wav"`)
	s.log().Info("voicemail recording served")
	// ServeContent answers Range requests, which Safari needs before it will
	// play anything at all. A zero time sends no Last-Modified.
	http.ServeContent(w, r, "", time.Time{}, f)
}

// wantsHTML is a browser opening the link from a mail; the <audio> element
// on the page asks with ?audio, and media fetches never ask for text/html.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

const page = `<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Voicemail</title>
<body style="font-family: system-ui, sans-serif; margin: 2rem">
<audio controls preload="metadata" src="/v/%s?audio" style="width: 100%%"></audio>
<p><a href="/v/%s?audio" download="voicemail.wav">Download</a></p>
</body></html>
`

// NewHTTPServer wraps the Server in the timeouts a LAN
// listener wants; address is where it listens.
func NewHTTPServer(address string, s *Server) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
}
