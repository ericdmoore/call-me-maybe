package serve

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/provision"
)

func window(t *testing.T) (*Server, *[]Event) {
	t.Helper()
	dir, state := t.TempDir(), t.TempDir()
	m, _ := provision.Lookup("grandstream-wp826")
	kitchen := provision.Phone{ID: "kitchen", MAC: "ec:74:d7:88:a2:54", Model: m, ProvisionPassword: "prov-k",
		Address: provision.Address{Host: "192.168.7.133", Port: 8443}}
	if err := os.WriteFile(filepath.Join(dir, kitchen.FileName()), []byte("<gs_provision/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kitchen-phonebook.xml"), []byte("<AddressBook/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	var events []Event
	s, err := New(Options{Dir: dir, StateDir: state, Phones: []provision.Phone{kitchen}, Address: kitchen.Address,
		OnEvent: func(e Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	return s, &events
}

func get(h http.Handler, path, user, pass string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "192.168.7.50:40000"
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func kinds(events []Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func TestFirstContactIsOpenThenTheCredentialIsRequired(t *testing.T) {
	s, events := window(t)
	h := s.Handler()
	if rec := get(h, "/prov/cfgec74d788a254.xml", "", ""); rec.Code != 200 || rec.Body.String() != "<gs_provision/>" {
		t.Fatalf("first fetch inside the window must be served: %d %q", rec.Code, rec.Body.String())
	}
	if at, ok := s.SeenAt("ec:74:d7:88:a2:54"); !ok || at.IsZero() {
		t.Fatal("first contact should be recorded with its time")
	}
	// The same phone, applying what it fetched, fetches again from the same
	// address inside the window: served. It cannot have the credential yet.
	if rec := get(h, "/prov/cfgec74d788a254.xml", "", ""); rec.Code != 200 {
		t.Fatalf("a re-fetch from the first-contact address inside the window must be served, got %d", rec.Code)
	}
	// Anybody else, without the credential: not.
	other := httptest.NewRequest(http.MethodGet, "/prov/cfgec74d788a254.xml", nil)
	other.RemoteAddr = "192.168.7.99:40000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, other)
	if rec.Code != 401 {
		t.Fatalf("a fetch from another address without the credential must be 401, got %d", rec.Code)
	}
	other.SetBasicAuth("kitchen", "wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, other)
	if rec.Code != 401 {
		t.Fatalf("wrong credential must be 401, got %d", rec.Code)
	}
	other.SetBasicAuth("kitchen", "prov-k")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, other)
	if rec.Code != 200 {
		t.Fatalf("the phone's own credential must be accepted from anywhere, got %d", rec.Code)
	}
	got := kinds(*events)
	want := []string{"connected", "fetched", "connected", "fetched", "connected", "unauthorized", "connected", "unauthorized", "connected", "fetched"}
	if len(got) != len(want) {
		t.Fatalf("events %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events %v, want %v", got, want)
		}
	}
	// First contact survives a new window: it is on disk — and the address
	// grace does not: a new window asks for the credential again.
	s2, err := New(s.opts)
	if err != nil || !s2.Seen("ec:74:d7:88:a2:54") {
		t.Fatal("first contact must persist across windows")
	}
	if rec := get(s2.Handler(), "/prov/cfgec74d788a254.xml", "", ""); rec.Code != 401 {
		t.Fatalf("in a new window the credential is required again, got %d", rec.Code)
	}
	s2.Forget("ec:74:d7:88:a2:54")
	if s2.Seen("ec:74:d7:88:a2:54") {
		t.Fatal("Forget should reopen first contact")
	}
}

func TestAnUnlistedPhoneIsRefusedAndNamed(t *testing.T) {
	s, events := window(t)
	if rec := get(s.Handler(), "/prov/cfg7c2e1a4b9c0d.xml", "", ""); rec.Code != 404 {
		t.Fatalf("unknown MAC must be 404, got %d", rec.Code)
	}
	e := (*events)[0]
	if e.Kind != "refused" || e.MAC != "7c:2e:1a:4b:9c:0d" || e.Remote != "192.168.7.50" {
		t.Fatalf("the refusal must name the asking MAC and address: %+v", e)
	}
	if want := `add mac = "7c:2e:1a:4b:9c:0d"`; !contains(e.Detail, want) {
		t.Fatalf("the hint should hand the operator the line to add: %q", e.Detail)
	}
}

func TestNothingIsListableAndThePhonebookNeedsTheCredential(t *testing.T) {
	s, events := window(t)
	h := s.Handler()
	for _, path := range []string{"/prov/", "/prov", "/", "/prov/kitchen/", "/prov/kitchen-phonebook.xml", "/prov/../etc/passwd"} {
		if rec := get(h, path, "", ""); rec.Code == 200 {
			t.Errorf("%s must not be served (got 200)", path)
		}
	}
	if rec := get(h, "/prov/kitchen/phonebook.xml", "", ""); rec.Code != 401 {
		t.Errorf("phonebook without credential must be 401, got %d", rec.Code)
	}
	if last := (*events)[len(*events)-1]; last.Kind != "unauthorized" || last.Handset != "kitchen" || !strings.Contains(last.Detail, "phonebook") {
		t.Errorf("a phonebook fetch without a credential must be reported, got %+v", last)
	}
	if rec := get(h, "/prov/kitchen/phonebook.xml", "kitchen", "prov-k"); rec.Code != 200 || rec.Body.String() != "<AddressBook/>" {
		t.Errorf("phonebook with credential must be served, got %d", rec.Code)
	}
	if rec := get(h, "/prov/nobody/phonebook.xml", "nobody", "x"); rec.Code != 404 {
		t.Errorf("unknown handset's phonebook must be 404, got %d", rec.Code)
	}
	// The token form: the phone's credential in the path, no header needed.
	tok := provision.PhonebookToken("prov-k")
	if rec := get(h, "/prov/kitchen/"+tok+"/phonebook.xml", "", ""); rec.Code != 200 || rec.Body.String() != "<AddressBook/>" {
		t.Errorf("phonebook by token must be served, got %d", rec.Code)
	}
	if rec := get(h, "/prov/kitchen/"+provision.PhonebookToken("wrong")+"/phonebook.xml", "", ""); rec.Code != 404 {
		t.Errorf("a wrong token must be 404, got %d", rec.Code)
	}
	if rec := get(h, "/prov/kitchen/"+tok+"/cfgec74d788a254.xml", "", ""); rec.Code != 404 {
		t.Errorf("the token path serves phone books only, got %d", rec.Code)
	}
	// The operator alias: same bytes, never without the credential — a room
	// name is guessable, a MAC is not, and only the MAC path is open at all.
	if rec := get(h, "/prov/kitchen.xml", "", ""); rec.Code != 401 {
		t.Errorf("kitchen.xml without credential must be 401, got %d", rec.Code)
	}
	if rec := get(h, "/prov/kitchen.xml", "kitchen", "prov-k"); rec.Code != 200 || rec.Body.String() != "<gs_provision/>" {
		t.Errorf("kitchen.xml with credential must be the phone's file, got %d", rec.Code)
	}
	if rec := get(h, "/prov/nobody.xml", "", ""); rec.Code != 404 {
		t.Errorf("an unknown alias is 404, got %d", rec.Code)
	}
}

func TestCertificateIsMintedOnceForTheAddress(t *testing.T) {
	s, _ := window(t)
	c1, err := s.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := s.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	if string(c1.Certificate[0]) != string(c2.Certificate[0]) {
		t.Fatal("the certificate must be minted once and reused")
	}
	pemBytes, err := s.CertificatePEM()
	if err != nil || !contains(string(pemBytes), "BEGIN CERTIFICATE") {
		t.Fatal("CertificatePEM should return the PEM")
	}
	// A different address mints a new one.
	s.opts.Address.Host = "192.168.7.200"
	c3, _ := s.Certificate()
	if string(c1.Certificate[0]) == string(c3.Certificate[0]) {
		t.Fatal("a certificate for another address must not be reused")
	}
}

func contains(s, sub string) bool { return len(sub) == 0 || (len(s) >= len(sub) && index(s, sub) >= 0) }
func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// The window is HTTPS and nothing else: plain HTTP gets a TLS alert, and
// the minted certificate carries the address, so a phone that validates
// servers can be given it (--export-cert) and succeed.
func TestTheWindowSpeaksOnlyTLSAndItsCertificateNamesTheAddress(t *testing.T) {
	s, _ := window(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// The certificate is minted for the configured address; the test
	// listener is loopback, so name the cert for it.
	s.opts.Address.Host = "127.0.0.1"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln, 0) }()
	addr := ln.Addr().String()

	// Plain HTTP: refused.
	plain := &http.Client{Timeout: 3 * time.Second}
	if resp, err := plain.Get("http://" + addr + "/prov/cfgec74d788a254.xml"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatal("plain HTTP must never be served a configuration")
		}
	}

	// HTTPS with the exported certificate as the only root: validates.
	pemBytes, err := s.CertificatePEM()
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("exported PEM is not a certificate")
	}
	secure := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	resp, err := secure.Get("https://" + addr + "/prov/cfgec74d788a254.xml")
	if err != nil {
		t.Fatalf("HTTPS with the exported certificate should validate: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("first fetch over TLS should be served, got %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("closing the window is not an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the window did not close on cancel")
	}
}

// The window is bounded: it closes on its own.
func TestTheWindowClosesByItself(t *testing.T) {
	s, _ := window(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.opts.Address.Host = "127.0.0.1"
	start := time.Now()
	if err := s.Serve(context.Background(), ln, 150*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("the window overstayed")
	}
	if _, err := net.DialTimeout("tcp", ln.Addr().String(), 500*time.Millisecond); err == nil {
		t.Fatal("the listener should be closed after the window")
	}
}

var _ = strings.Contains

// The directory is the other process: phonebooks with the credential and
// nothing else, ever — a configuration request against it is 404 even from
// a listed phone, and the book comes from the renderer, not a file.
func TestTheDirectoryServesPhonebooksAndNeverAConfiguration(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	m, _ := provision.Lookup("grandstream-wp826")
	kitchen := provision.Phone{ID: "kitchen", MAC: "ec:74:d7:88:a2:54", Model: m, ProvisionPassword: "prov-k",
		Address: provision.Address{Host: "192.168.7.133", Port: 8443}}
	if err := os.WriteFile(filepath.Join(dir, kitchen.FileName()), []byte("<gs_provision/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	renders := 0
	var events []Event
	s, err := New(Options{Dir: dir, StateDir: state, Phones: []provision.Phone{kitchen}, Address: kitchen.Address, Directory: true,
		Phonebook: func(id string) ([]byte, error) {
			renders++
			return []byte("<AddressBook>" + id + "</AddressBook>"), nil
		},
		OnEvent: func(e Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	if rec := get(h, "/prov/cfgec74d788a254.xml", "", ""); rec.Code != 404 {
		t.Fatalf("the directory must never serve a configuration, got %d", rec.Code)
	}
	if rec := get(h, "/prov/cfgec74d788a254.xml", "kitchen", "prov-k"); rec.Code != 404 {
		t.Fatalf("not even with the credential, got %d", rec.Code)
	}
	if rec := get(h, "/prov/kitchen.xml", "kitchen", "prov-k"); rec.Code != 404 {
		t.Fatalf("nor the operator alias, got %d", rec.Code)
	}
	if s.Seen(kitchen.MAC) {
		t.Fatal("the directory never records first contact")
	}
	if rec := get(h, "/prov/kitchen/phonebook.xml", "", ""); rec.Code != 401 {
		t.Fatalf("phonebook without credential must be 401, got %d", rec.Code)
	}
	rec := get(h, "/prov/kitchen/phonebook.xml", "kitchen", "prov-k")
	if rec.Code != 200 || rec.Body.String() != "<AddressBook>kitchen</AddressBook>" || renders != 1 {
		t.Fatalf("phonebook should come from the renderer: %d %q (renders %d)", rec.Code, rec.Body.String(), renders)
	}
	if len(events) == 0 || events[0].Kind != "refused" || !strings.Contains(events[0].Detail, "asked the directory") {
		t.Fatalf("the misdirected configuration request should be reported: %+v", events)
	}
}
