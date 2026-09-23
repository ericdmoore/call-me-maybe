// Package serve is the LAN-facing half of provisioning: the HTTPS window a
// phone fetches its configuration from, and the always-on directory the
// phones poll. It is reachable from the provision subcommand and from
// nothing else — asserted by test, the same way internal/provider is — because
// a listener on the LAN that hands out SIP passwords must never share a
// process with the thing that answers the phone.
package serve

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"callmemaybe/internal/policy"
	"callmemaybe/internal/provision"
)

// Event is one thing the session should show the operator, as it happens.
type Event struct {
	Time time.Time
	// Kind: connected, fetched, refused (unknown MAC), unauthorized,
	// phonebook, error.
	Kind string
	// Handset id when known; the asking MAC otherwise.
	Handset string
	MAC     string
	Remote  string
	Detail  string
}

// Options configures one window.
type Options struct {
	// Dir holds the rendered files: cfg<mac>.xml and <id>-phonebook.xml.
	Dir string
	// Phones are the handsets the window will answer for — and nobody else.
	Phones []provision.Phone
	// StateDir keeps the certificate and which phones have made first
	// contact, across windows.
	StateDir string
	// Address is what the certificate is minted for and what is printed.
	Address provision.Address
	// OnEvent receives every event; nil means silent.
	OnEvent func(Event)
	// Directory makes this the always-on directory: phonebook paths only,
	// every configuration path 404, no first contact ever. It is a
	// different process with a different lifetime from the window.
	Directory bool
	// Phonebook, when set, renders a handset's directory on demand instead
	// of reading <id>-phonebook.xml from Dir — so a name added to
	// [[people]] is on the phones at their next poll, with no render run.
	Phonebook func(id string) ([]byte, error)
}

// Server is one provisioning window.
type Server struct {
	opts   Options
	byFile map[string]provision.Phone // cfg<mac>.xml -> phone
	byID   map[string]provision.Phone
	mu     sync.Mutex
	seen   map[string]time.Time // MAC canonical -> first contact
}

// New validates the options and loads first-contact state.
func New(opts Options) (*Server, error) {
	if opts.StateDir == "" || (opts.Dir == "" && opts.Phonebook == nil) {
		return nil, errors.New("serve: StateDir is required, and Dir unless Phonebook renders on demand")
	}
	if err := os.MkdirAll(opts.StateDir, 0o700); err != nil {
		return nil, err
	}
	s := &Server{opts: opts, byFile: map[string]provision.Phone{}, byID: map[string]provision.Phone{}, seen: map[string]time.Time{}}
	for _, p := range opts.Phones {
		s.byFile[p.FileName()] = p
		s.byID[p.ID] = p
		if at, ok := readSeen(opts.StateDir, p.MAC); ok {
			s.seen[p.MAC] = at
		}
	}
	return s, nil
}

// Seen reports whether a phone has fetched its configuration before — the
// point after which its fetches must carry its own credential.
func (s *Server) Seen(mac string) bool {
	_, ok := s.SeenAt(mac)
	return ok
}

// SeenAt is when the phone first fetched its configuration.
func (s *Server) SeenAt(mac string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.seen[mac]
	return at, ok
}

func (s *Server) markSeen(mac string) {
	s.mu.Lock()
	_, already := s.seen[mac]
	if !already {
		s.seen[mac] = time.Now()
	}
	s.mu.Unlock()
	if already {
		return
	}
	dir := filepath.Join(s.opts.StateDir, "seen")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, policy.MACFilename(mac)), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// readSeen reads first contact for a MAC from a state directory. Exported
// through SeenAt on a Server; the inventory view builds one without listening.
func readSeen(stateDir, mac string) (time.Time, bool) {
	b, err := os.ReadFile(filepath.Join(stateDir, "seen", policy.MACFilename(mac)))
	if err != nil {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if err != nil {
		return time.Time{}, false
	}
	return at.Local(), true
}

// Forget clears first contact for a phone — after a factory reset, or a
// rotation the phone must fetch unauthenticated once.
func (s *Server) Forget(mac string) {
	s.mu.Lock()
	delete(s.seen, mac)
	s.mu.Unlock()
	_ = os.Remove(filepath.Join(s.opts.StateDir, "seen", policy.MACFilename(mac)))
}

func (s *Server) emit(e Event) {
	e.Time = time.Now()
	if s.opts.OnEvent != nil {
		s.opts.OnEvent(e)
	}
}

// Handler is the whole HTTP surface: two kinds of file, nothing listable.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/prov/", s.handle)
	return mux
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	remote, _, _ := net.SplitHostPort(r.RemoteAddr)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/prov/")

	// The phone's own file: cfg<mac>.xml. First contact is unauthenticated
	// — a factory phone has no credential yet — and only inside a window
	// the operator opened; every later fetch presents the credential the
	// first one delivered.
	if strings.HasPrefix(rest, "cfg") && strings.HasSuffix(rest, ".xml") && !strings.Contains(rest, "/") {
		if s.opts.Directory {
			// The directory never hands out a configuration, whoever asks:
			// a listed phone gets 404 here exactly like a stranger, and the
			// event says so, because a phone pointed at the wrong port is a
			// mistake worth a line.
			s.emit(Event{Kind: "refused", Remote: remote, Detail: fmt.Sprintf("a phone at %s asked the directory for %s — configuration is only served by `doorman provision <id>`", remote, rest)})
			http.NotFound(w, r)
			return
		}
		p, ok := s.byFile[rest]
		if !ok {
			mac := strings.TrimSuffix(strings.TrimPrefix(rest, "cfg"), ".xml")
			canon, isMAC := policy.NormaliseMAC(mac)
			if !isMAC {
				canon = mac
			}
			s.emit(Event{Kind: "refused", MAC: canon, Remote: remote,
				Detail: fmt.Sprintf("a phone at %s asked for %s — not in handsets.toml; if that is the new phone, add mac = %q to its block and run this again", remote, rest, canon)})
			http.NotFound(w, r)
			return
		}
		s.emit(Event{Kind: "connected", Handset: p.ID, MAC: p.MAC, Remote: remote})
		if s.Seen(p.MAC) && !s.authorised(r, p) {
			s.emit(Event{Kind: "unauthorized", Handset: p.ID, MAC: p.MAC, Remote: remote, Detail: "fetch after first contact without the phone's credential"})
			w.Header().Set("WWW-Authenticate", `Basic realm="doorman"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, err := os.ReadFile(filepath.Join(s.opts.Dir, rest))
		if err != nil {
			s.emit(Event{Kind: "error", Handset: p.ID, MAC: p.MAC, Detail: "rendered file missing — run doorman render"})
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
		s.markSeen(p.MAC)
		s.emit(Event{Kind: "fetched", Handset: p.ID, MAC: p.MAC, Remote: remote, Detail: fmt.Sprintf("%s (%d bytes)", rest, len(body))})
		return
	}

	// The operator-facing alias: <id>.xml is the same content as the phone's
	// file, for a browser or curl to inspect — and it always asks for the
	// phone's credential, so a guessable room name never fetches a password.
	if strings.HasSuffix(rest, ".xml") && !strings.Contains(rest, "/") {
		id := strings.TrimSuffix(rest, ".xml")
		p, ok := s.byID[id]
		if !ok || s.opts.Directory {
			http.NotFound(w, r)
			return
		}
		if !s.authorised(r, p) {
			w.Header().Set("WWW-Authenticate", `Basic realm="doorman"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, err := os.ReadFile(filepath.Join(s.opts.Dir, p.FileName()))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
		s.emit(Event{Kind: "inspected", Handset: p.ID, MAC: p.MAC, Remote: remote, Detail: rest})
		return
	}

	// The phone's directory: <id>/phonebook.xml. Caller data, so never
	// on the unauthenticated path — the phone always presents its credential.
	if parts := strings.Split(rest, "/"); len(parts) == 2 && parts[1] == "phonebook.xml" {
		p, ok := s.byID[parts[0]]
		if !ok || !s.authorised(r, p) {
			if ok {
				w.Header().Set("WWW-Authenticate", `Basic realm="doorman"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.NotFound(w, r)
			return
		}
		var body []byte
		var err error
		if s.opts.Phonebook != nil {
			body, err = s.opts.Phonebook(p.ID)
		} else {
			body, err = os.ReadFile(filepath.Join(s.opts.Dir, p.ID+"-phonebook.xml"))
		}
		if err != nil {
			s.emit(Event{Kind: "error", Handset: p.ID, MAC: p.MAC, Detail: "phonebook: " + err.Error()})
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
		s.emit(Event{Kind: "phonebook", Handset: p.ID, MAC: p.MAC, Remote: remote, Detail: fmt.Sprintf("%d bytes", len(body))})
		return
	}

	http.NotFound(w, r)
}

func (s *Server) authorised(r *http.Request, p provision.Phone) bool {
	user, pass, ok := r.BasicAuth()
	return ok && user == p.ID && pass != "" && pass == p.ProvisionPassword
}

// Certificate returns the window's TLS certificate, minting it once into
// the state directory with every address it will be offered on as a SAN,
// so a phone that validates can.
func (s *Server) Certificate() (tls.Certificate, error) {
	crt := filepath.Join(s.opts.StateDir, "tls.crt")
	key := filepath.Join(s.opts.StateDir, "tls.key")
	if c, err := tls.LoadX509KeyPair(crt, key); err == nil {
		if leaf, perr := x509.ParseCertificate(c.Certificate[0]); perr == nil && s.certCovers(leaf) && time.Now().Before(leaf.NotAfter.Add(-30*24*time.Hour)) {
			return c, nil
		}
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "doorman provisioning", Organization: []string{"Call Me Maybe"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(s.opts.Address.Host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{s.opts.Address.Host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(crt, certPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(key, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}

func (s *Server) certCovers(leaf *x509.Certificate) bool {
	if ip := net.ParseIP(s.opts.Address.Host); ip != nil {
		for _, have := range leaf.IPAddresses {
			if have.Equal(ip) {
				return true
			}
		}
		return false
	}
	for _, n := range leaf.DNSNames {
		if n == s.opts.Address.Host {
			return true
		}
	}
	return false
}

// CertificatePEM is the certificate for a phone that validates servers.
func (s *Server) CertificatePEM() ([]byte, error) {
	if _, err := s.Certificate(); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(s.opts.StateDir, "tls.crt"))
}

// ListenAndServe serves HTTPS on the address until ctx is done — the
// window — then shuts down cleanly. window <= 0 means until cancelled.
func (s *Server) ListenAndServe(ctx context.Context, window time.Duration) error {
	ln, err := net.Listen("tcp", s.opts.Address.HostPort())
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln, window)
}

// Serve is ListenAndServe on a listener the caller opened. TLS only: a
// plain-HTTP request to it is answered with a TLS alert, never a file.
func (s *Server) Serve(ctx context.Context, ln net.Listener, window time.Duration) error {
	cert, err := s.Certificate()
	if err != nil {
		_ = ln.Close()
		return err
	}
	if window > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, window)
		defer cancel()
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ServeTLS(ln, "", "") }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
		return nil
	}
}
