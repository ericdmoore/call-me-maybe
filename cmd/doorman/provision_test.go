package main

import (
	"context"
	"flag"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"callmemaybe/internal/ari"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/provision"
	provserve "callmemaybe/internal/provision/serve"
)

// fakeEndpoints is the ARI endpoint feed: a phone is offline until the test
// says otherwise.
type fakeEndpoints struct {
	mu     sync.Mutex
	online map[string]bool
}

func (f *fakeEndpoints) set(id string, up bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.online == nil {
		f.online = map[string]bool{}
	}
	f.online[id] = up
}

func (f *fakeEndpoints) Endpoint(_ context.Context, tech, resource string) (ari.EndpointState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := "offline"
	if f.online[resource] {
		st = "online"
	}
	return ari.EndpointState{Technology: tech, Resource: resource, State: st}, nil
}

func sessionPhones(t *testing.T) (dir string, phones []provision.Phone) {
	t.Helper()
	dir = t.TempDir()
	wp, _ := provision.Lookup("grandstream-wp826")
	addr := provision.Address{Host: "192.168.7.133", Port: 8443}
	phones = []provision.Phone{
		{ID: "kitchen", Label: "Kitchen", MAC: "ec:74:d7:88:a2:54", Model: wp, Number: 101, Address: addr, ProvisionPassword: "prov-k"},
		{ID: "theater", Label: "Theater", MAC: "ec:74:d7:88:bd:5a", Model: wp, Number: 102, Address: addr, ProvisionPassword: "prov-t"},
	}
	for _, p := range phones {
		if err := os.WriteFile(filepath.Join(dir, p.FileName()), []byte("<gs_provision/>"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, phones
}

type sessionHarness struct {
	session *provisionSession
	url     string
	out     *strings.Builder
	feed    *fakeEndpoints
}

func newSession(t *testing.T, named []string, window time.Duration) *sessionHarness {
	t.Helper()
	dir, phones := sessionPhones(t)
	events := make(chan provserve.Event, 64)
	srv, err := provserve.New(provserve.Options{Dir: dir, StateDir: t.TempDir(), Phones: phones, Address: phones[0].Address,
		OnEvent: func(e provserve.Event) { events <- e }})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	var chosen []provision.Phone
	for _, p := range phones {
		for _, id := range named {
			if p.ID == id {
				chosen = append(chosen, p)
			}
		}
	}
	feed := &fakeEndpoints{}
	out := &strings.Builder{}
	return &sessionHarness{
		feed: feed, out: out, url: ts.URL,
		session: &provisionSession{
			phones: chosen, server: srv, reader: feed, window: window, poll: 20 * time.Millisecond,
			events: events, out: out,
			listen: func(ctx context.Context) error { <-ctx.Done(); return nil },
		},
	}
}

func fetch(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// The session's whole promise: connect → fetch → registered, in order, and
// exit 0 once every named phone is there.
func TestSessionReportsConnectFetchRegisteredInOrderAndExitsZero(t *testing.T) {
	h := newSession(t, []string{"kitchen"}, 5*time.Second)
	code := make(chan int, 1)
	go func() { code <- h.session.run(context.Background()) }()

	time.Sleep(50 * time.Millisecond)
	if got := fetch(t, h.url+"/prov/cfgec74d788a254.xml"); got != 200 {
		t.Fatalf("first fetch should be served, got %d", got)
	}
	time.Sleep(50 * time.Millisecond)
	h.feed.set("kitchen", true)

	select {
	case c := <-code:
		if c != 0 {
			t.Fatalf("exit = %d, want 0\n%s", c, h.out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("session did not finish\n%s", h.out.String())
	}
	out := h.out.String()
	iConn, iFetch, iReg := strings.Index(out, "kitchen    connected from"), strings.Index(out, "fetched cfgec74d788a254.xml"), strings.Index(out, "registered  PJSIP/kitchen")
	if iConn < 0 || iFetch < 0 || iReg < 0 || !(iConn < iFetch && iFetch < iReg) {
		t.Fatalf("connect → fetch → registered not reported in order:\n%s", out)
	}
	if !strings.Contains(out, "✓ 1 of 1 handsets registered — window closed") {
		t.Fatalf("missing the closing line:\n%s", out)
	}
	// The instructions never carry a password.
	for _, secret := range []string{"prov-k", "prov-t"} {
		if strings.Contains(out, secret) {
			t.Fatalf("the session printed a credential:\n%s", out)
		}
	}
}

// A closed window with a phone still missing is a non-zero exit that names
// the phone — the operator went to the wrong room, or the phone is off.
func TestSessionExitsNonZeroWhenTheWindowClosesFirst(t *testing.T) {
	h := newSession(t, []string{"kitchen", "theater"}, 300*time.Millisecond)
	go func() {
		time.Sleep(50 * time.Millisecond)
		fetch(t, h.url+"/prov/cfgec74d788a254.xml")
		h.feed.set("kitchen", true)
	}()
	code := h.session.run(context.Background())
	if code != provisionExitMissing {
		t.Fatalf("exit = %d, want %d\n%s", code, provisionExitMissing, h.out.String())
	}
	out := h.out.String()
	if !strings.Contains(out, "1 of 2 handsets registered") || !strings.Contains(out, "still missing: theater") {
		t.Fatalf("the missing phone must be named:\n%s", out)
	}
}

// Ctrl-C closes the window cleanly, with the same accounting.
func TestSessionClosesCleanlyOnCancel(t *testing.T) {
	h := newSession(t, []string{"kitchen"}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(60 * time.Millisecond); cancel() }()
	if code := h.session.run(ctx); code != provisionExitMissing {
		t.Fatalf("exit = %d, want %d\n%s", code, provisionExitMissing, h.out.String())
	}
	if !strings.Contains(h.out.String(), "until Ctrl-C") {
		t.Fatalf("--forever should say so:\n%s", h.out.String())
	}
}

// The most useful line in the log: a phone that is not in the inventory is
// refused and its MAC is printed, ready to paste into handsets.toml.
func TestSessionPrintsTheUnknownMACHint(t *testing.T) {
	h := newSession(t, []string{"kitchen"}, 400*time.Millisecond)
	go func() {
		time.Sleep(50 * time.Millisecond)
		fetch(t, h.url+"/prov/cfg7c2e1a4b9c0d.xml")
	}()
	_ = h.session.run(context.Background())
	out := h.out.String()
	if !strings.Contains(out, `add mac = "7c:2e:1a:4b:9c:0d"`) || !strings.Contains(out, "not in handsets.toml") {
		t.Fatalf("the hint with the asking MAC is missing:\n%s", out)
	}
}

// With no ARI, the session watches fetches and says registration is not
// confirmed — it never pretends.
func TestSessionWithoutARIWatchesFetchesOnly(t *testing.T) {
	h := newSession(t, []string{"kitchen"}, 2*time.Second)
	h.session.reader = nil
	go func() {
		time.Sleep(50 * time.Millisecond)
		fetch(t, h.url+"/prov/cfgec74d788a254.xml")
	}()
	if code := h.session.run(context.Background()); code != 0 {
		t.Fatalf("exit = %d\n%s", code, h.out.String())
	}
	if !strings.Contains(h.out.String(), "registration is not confirmed") {
		t.Fatalf("should say ARI was not there:\n%s", h.out.String())
	}
}

// The instruction text: what to type, per model — and never a secret.
func TestInstructionsNameThePathAndNeverASecret(t *testing.T) {
	_, phones := sessionPhones(t)
	out := &strings.Builder{}
	printInstructions(out, phones[0])
	s := out.String()
	for _, want := range []string{"Grandstream WP826", "ec:74:d7:88:a2:54", "192.168.7.133:8443/prov", "HTTPS", "DHCP option 66 = https://192.168.7.133:8443/prov"} {
		if !strings.Contains(s, want) {
			t.Errorf("instructions missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "prov-k") {
		t.Errorf("instructions carry a credential:\n%s", s)
	}
}

// The inventory view: every PJSIP handset, the manual-path ones included,
// and no listener anywhere.
func TestInventoryViewListsEveryHandset(t *testing.T) {
	handsets := []policy.Handset{
		{ID: "kitchen", Endpoint: "PJSIP/kitchen", Number: 101, MAC: "ec:74:d7:88:a2:54", Model: "grandstream-wp826"},
		{ID: "hall", Endpoint: "PJSIP/hall", Number: 103},
		{ID: "conference", Endpoint: "Local/600@internal"},
	}
	dir, phones := sessionPhones(t)
	built := &provision.Built{Phones: phones[:1]}
	srv, err := provserve.New(provserve.Options{Dir: dir, StateDir: t.TempDir(), Phones: built.Phones, Address: phones[0].Address})
	if err != nil {
		t.Fatal(err)
	}
	feed := &fakeEndpoints{}
	feed.set("kitchen", true)
	out := &strings.Builder{}
	printInventory(context.Background(), out, handsets, built, srv, feed)
	s := out.String()
	for _, want := range []string{"window closed", "kitchen", "WP826", "ec:74:d7:88:a2:54", "cfgec74d788a254.xml", "registered", "hall", "no mac — manual path", "inspect:  https://192.168.7.133:8443/prov/kitchen.xml"} {
		if !strings.Contains(s, want) {
			t.Errorf("inventory missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "conference") {
		t.Errorf("a pseudo-handset has nothing to provision:\n%s", s)
	}
}

func TestModelsListsTheRegistry(t *testing.T) {
	out := &strings.Builder{}
	printModels(out)
	if !strings.Contains(out.String(), "grandstream-wp826") || !strings.Contains(out.String(), "configures itself") {
		t.Fatalf("models list:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "yealink-t31p") || !strings.Contains(out.String(), "no template yet") {
		t.Fatalf("untemplated models should be listed as such:\n%s", out.String())
	}
}

// ── the daemon must not link the LAN listener ─────────────────────────────

// A listener that hands out SIP passwords must not share a process with the
// thing that answers the phone. `doorman` is one binary, so the boundary is
// the import graph: only this subcommand's file names the serving package,
// and nothing under internal/ reaches it except itself.
func TestOnlyTheProvisionCommandNamesTheServingPackage(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || name == "provision.go" || name == "provision_test.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "internal/provision/serve") {
			t.Errorf("%s mentions internal/provision/serve — the LAN listener is reached from "+
				"`doorman provision` and nothing else, deliberately", name)
		}
	}
}

func TestNothingUnderInternalImportsTheServingPackage(t *testing.T) {
	fset := token.NewFileSet()
	root := filepath.Join("..", "..", "internal")
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(dir) != "serve" {
			pkgs, err := parser.ParseDir(fset, dir, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parsing %s: %v", dir, err)
			}
			for _, pkg := range pkgs {
				for path, file := range pkg.Files {
					for _, imp := range file.Imports {
						if strings.Contains(imp.Path.Value, "internal/provision/serve") {
							t.Errorf("%s imports internal/provision/serve — the daemon must never link the LAN listener", path)
						}
					}
				}
			}
		}
		for _, e := range entries {
			if e.IsDir() {
				walk(filepath.Join(dir, e.Name()))
			}
		}
	}
	walk(root)
}

// Re-provisioning: the NOTIFY goes out once the window is open, each phone
// is named, and a phone that fetches afterwards counts as done.
func TestNotifySessionSendsCheckSyncThenWatchesTheFetch(t *testing.T) {
	h := newSession(t, []string{"kitchen"}, 3*time.Second)
	var notified []string
	h.session.notify = func(id string) (string, error) {
		notified = append(notified, id)
		go func() {
			time.Sleep(30 * time.Millisecond)
			req, _ := http.NewRequest(http.MethodGet, h.url+"/prov/cfgec74d788a254.xml", nil)
			req.SetBasicAuth("kitchen", "prov-k")
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
			}
			h.feed.set("kitchen", true)
		}()
		return "Sending NOTIFY of type 'check-sync' to endpoint kitchen", nil
	}
	if code := h.session.run(context.Background()); code != 0 {
		t.Fatalf("exit = %d\n%s", code, h.out.String())
	}
	if len(notified) != 1 || notified[0] != "kitchen" {
		t.Fatalf("notified = %v", notified)
	}
	if !strings.Contains(h.out.String(), "sent check-sync") {
		t.Fatalf("should report the NOTIFY:\n%s", h.out.String())
	}
}

func TestNotifyFailureIsReportedAndTheWindowStaysOpen(t *testing.T) {
	h := newSession(t, []string{"kitchen"}, 300*time.Millisecond)
	h.session.notify = func(id string) (string, error) { return "", context.DeadlineExceeded }
	if code := h.session.run(context.Background()); code != provisionExitMissing {
		t.Fatalf("exit = %d\n%s", code, h.out.String())
	}
	if !strings.Contains(h.out.String(), "notify failed") {
		t.Fatalf("the failure must be visible:\n%s", h.out.String())
	}
}

// `doorman provision kitchen --window 45m` is how a person types it; the
// flag must not be mistaken for a handset id.
func TestProvisionFlagsMayFollowTheIds(t *testing.T) {
	fs := flag.NewFlagSet("provision", flag.ContinueOnError)
	window := fs.Duration("window", 0, "")
	all := fs.Bool("all", false, "")
	ids := parseInterleaved(fs, []string{"kitchen", "--window", "45m", "theater", "-all"})
	if len(ids) != 2 || ids[0] != "kitchen" || ids[1] != "theater" {
		t.Fatalf("ids = %v", ids)
	}
	if *window != 45*time.Minute || !*all {
		t.Fatalf("flags after ids were not parsed: window=%v all=%v", *window, *all)
	}
}

// After a factory reset, or when the credential on the phone is wrong, the
// operator reopens first contact for that phone and nothing else.
func TestResetReopensFirstContactForTheNamedPhoneOnly(t *testing.T) {
	dir, phones := sessionPhones(t)
	state := t.TempDir()
	srv, err := provserve.New(provserve.Options{Dir: dir, StateDir: state, Phones: phones, Address: phones[0].Address})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	fetch(t, ts.URL+"/prov/cfgec74d788a254.xml") // kitchen: first contact
	fetch(t, ts.URL+"/prov/cfgec74d788bd5a.xml") // theater: first contact
	srv2, _ := provserve.New(provserve.Options{Dir: dir, StateDir: state, Phones: phones, Address: phones[0].Address})
	srv2.Forget(phones[0].MAC)
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()
	if got := fetch(t, ts2.URL+"/prov/cfgec74d788a254.xml"); got != 200 {
		t.Fatalf("kitchen after reset should be served without a credential, got %d", got)
	}
	if got := fetch(t, ts2.URL+"/prov/cfgec74d788bd5a.xml"); got != 401 {
		t.Fatalf("theater was not reset and must still need its credential, got %d", got)
	}
}
