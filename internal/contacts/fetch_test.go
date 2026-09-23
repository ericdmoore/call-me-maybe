package contacts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"callmemaybe/internal/policy"
)

const fetchedCards = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Grandma Mertaugh\r\nTEL;TYPE=CELL:+1 512 555 0101\r\nEND:VCARD\r\n"

// A source server that can be told what to answer.
type bookServer struct {
	*httptest.Server
	status   atomic.Int32
	etag     string
	body     string
	hits     atomic.Int32
	lastAuth atomic.Value // string
	lastINM  atomic.Value // string
}

func newBookServer(t *testing.T) *bookServer {
	t.Helper()
	b := &bookServer{etag: `"v1"`, body: fetchedCards}
	b.status.Store(200)
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.hits.Add(1)
		b.lastAuth.Store(r.Header.Get("Authorization"))
		b.lastINM.Store(r.Header.Get("If-None-Match"))
		switch st := int(b.status.Load()); {
		case st != 200:
			http.Error(w, "the request, echoed: "+r.Header.Get("Authorization"), st)
		case r.Header.Get("If-None-Match") == b.etag:
			w.WriteHeader(http.StatusNotModified)
		default:
			w.Header().Set("ETag", b.etag)
			w.Header().Set("Content-Type", "text/vcard")
			_, _ = w.Write([]byte(b.body))
		}
	}))
	t.Cleanup(b.Close)
	return b
}

func fetcher(t *testing.T, tok string) *Fetcher {
	t.Helper()
	return &Fetcher{Dir: filepath.Join(t.TempDir(), "cache"), Secret: func(name string) (string, bool) {
		if name == "CONTACTS_ERIC_TOKEN" && tok != "" {
			return tok, true
		}
		return "", false
	}}
}

func TestFirstFetchCachesThenConditionalRequestsKeepIt(t *testing.T) {
	srv := newBookServer(t)
	f := fetcher(t, "shh-not-a-real-token")
	src := policy.ContactSource{ID: "eric", URL: srv.URL + "/eric.vcf", TokenEnv: "CONTACTS_ERIC_TOKEN", Kind: policy.ContactAdmit}

	d, out := f.Fetch(context.Background(), src, time.Hour)
	if out.Status != "fresh" || d.Err != nil || string(d.Data) != fetchedCards {
		t.Fatalf("first fetch: %+v %v", out, d.Err)
	}
	if got := srv.lastAuth.Load(); got != "Bearer shh-not-a-real-token" {
		t.Fatalf("the token must travel as a bearer header, got %q", got)
	}
	info, err := os.Stat(filepath.Join(f.Dir, "eric.vcf"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache must be 0600: %v %v", err, info)
	}
	if d.FetchedAt.IsZero() {
		t.Fatal("FetchedAt should be set")
	}

	d, out = f.Fetch(context.Background(), src, time.Hour)
	if out.Status != "unchanged" || d.Err != nil || string(d.Data) != fetchedCards {
		t.Fatalf("second fetch should be a 304 served from cache: %+v %v", out, d.Err)
	}
	if got := srv.lastINM.Load(); got != `"v1"` {
		t.Fatalf("second fetch should be conditional, If-None-Match=%q", got)
	}

	// Without the network, the cache is what Load sees.
	c := f.Cached(src, time.Hour)
	if c.Err != nil || string(c.Data) != fetchedCards || c.Warning != "" {
		t.Fatalf("Cached: %+v", c)
	}
}

func TestAFailedFetchServesTheLastGoodCopyAndSaysSo(t *testing.T) {
	srv := newBookServer(t)
	f := fetcher(t, "shh-not-a-real-token")
	src := policy.ContactSource{ID: "eric", URL: srv.URL + "/eric.vcf", TokenEnv: "CONTACTS_ERIC_TOKEN"}
	if _, out := f.Fetch(context.Background(), src, time.Hour); out.Status != "fresh" {
		t.Fatalf("setup: %+v", out)
	}
	srv.status.Store(500)
	d, out := f.Fetch(context.Background(), src, time.Hour)
	if out.Status != "stale" || out.Err == nil {
		t.Fatalf("a failed refresh with a cache is stale, got %+v", out)
	}
	if d.Err != nil || string(d.Data) != fetchedCards {
		t.Fatalf("the last good copy must still be served: %+v", d)
	}
	if !strings.Contains(d.Warning, "fetching has failed since") || !strings.Contains(d.Warning, "HTTP 500") {
		t.Fatalf("the warning should say how old and why: %q", d.Warning)
	}
	// The server echoed the credential in its error body; none of it may
	// reach the message.
	for _, text := range []string{d.Warning, out.Err.Error()} {
		if strings.Contains(text, "shh-not-a-real-token") {
			t.Fatalf("a credential leaked into a message: %q", text)
		}
	}
	// Recovery clears the warning.
	srv.status.Store(200)
	d, out = f.Fetch(context.Background(), src, time.Hour)
	if out.Status != "unchanged" || d.Warning != "" {
		t.Fatalf("after recovery: %+v warning=%q", out, d.Warning)
	}
}

func TestASourceThatNeverSucceededContributesNothingAndStopsNobody(t *testing.T) {
	srv := newBookServer(t)
	srv.status.Store(401)
	f := fetcher(t, "shh-not-a-real-token")
	src := policy.ContactSource{ID: "eric", URL: srv.URL + "/eric.vcf", TokenEnv: "CONTACTS_ERIC_TOKEN"}
	d, out := f.Fetch(context.Background(), src, time.Hour)
	if out.Status != "failed" || d.Err == nil || d.Data != nil {
		t.Fatalf("got %+v / %+v", out, d)
	}
	if !strings.Contains(d.Err.Error(), "never fetched") || !strings.Contains(d.Err.Error(), "HTTP 401") {
		t.Fatalf("the report should say it never succeeded and why: %v", d.Err)
	}
	// And in the merged set, the source is unread, and the others are fine.
	inv, err := policy.ContactsFromTOML([]byte("[[sources]]\nid = \"eric\"\nurl = \"" + srv.URL + "/eric.vcf\"\ntoken_env = \"CONTACTS_ERIC_TOKEN\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	set, outcomes := Fetch(context.Background(), inv, "1", f)
	if len(outcomes) != 1 || outcomes[0].Status != "failed" {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if rep := set.Sources()[0]; rep.Unread == "" || rep.Missing {
		t.Fatalf("an unfetched url source is unread and not an operator mistake: %+v", rep)
	}
}

func TestAMissingTokenVariableIsRefusedBeforeAnyRequest(t *testing.T) {
	srv := newBookServer(t)
	f := fetcher(t, "")
	src := policy.ContactSource{ID: "eric", URL: srv.URL + "/eric.vcf", TokenEnv: "CONTACTS_ERIC_TOKEN"}
	d, out := f.Fetch(context.Background(), src, time.Hour)
	if out.Status != "failed" || !strings.Contains(out.Err.Error(), "CONTACTS_ERIC_TOKEN is not set") {
		t.Fatalf("got %+v", out)
	}
	if srv.hits.Load() != 0 {
		t.Fatal("no request may be made without the credential the source asks for")
	}
	if d.Err == nil {
		t.Fatal("the document must be unread")
	}
}

func TestATokenlessSourceSendsNoAuthorization(t *testing.T) {
	srv := newBookServer(t)
	f := fetcher(t, "")
	src := policy.ContactSource{ID: "public", URL: srv.URL + "/public.vcf"}
	if _, out := f.Fetch(context.Background(), src, time.Hour); out.Status != "fresh" {
		t.Fatalf("got %+v", out)
	}
	if got := srv.lastAuth.Load(); got != "" {
		t.Fatalf("no token_env means no Authorization header, got %q", got)
	}
}

func TestLoadReadsURLSourcesFromTheCacheWithoutTheNetwork(t *testing.T) {
	srv := newBookServer(t)
	dir := t.TempDir()
	inv, err := policy.ContactsFromTOML([]byte("cache_dir = \"" + filepath.Join(dir, "cache") + "\"\nrefresh = \"2h\"\n[[sources]]\nid = \"eric\"\nurl = \"" + srv.URL + "/eric.vcf\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	inv.Path = filepath.Join(dir, "contacts.toml")
	// Before any fetch: reported, never fatal, with the way forward.
	set := Load(inv, "1")
	rep := set.Sources()[0]
	if rep.Unread == "" || !strings.Contains(rep.Unread, "every 2h") || !strings.Contains(rep.Unread, "--fetch") {
		t.Fatalf("an unfetched source should say what fetches it: %+v", rep)
	}
	// After one fetch, Load sees the cache and never touches the server.
	if _, outcomes := Fetch(context.Background(), inv, "1", &Fetcher{}); outcomes[0].Status != "fresh" {
		t.Fatalf("fetch: %+v", outcomes)
	}
	hits := srv.hits.Load()
	set = Load(inv, "1")
	if srv.hits.Load() != hits {
		t.Fatal("Load must not touch the network")
	}
	rep = set.Sources()[0]
	if rep.Unread != "" || rep.Personal != 1 || rep.FetchedAt.IsZero() {
		t.Fatalf("the cached source should be read and dated: %+v", rep)
	}
	if e, ok := set.Lookup("+15125550101"); !ok || e.Source != "eric" {
		t.Fatalf("Grandma should be in the set from source eric: %+v %v", e, ok)
	}
}

func TestRefreshMustBeADurationOfAtLeastAMinute(t *testing.T) {
	for _, bad := range []string{"refresh = \"soon\"", "refresh = \"30s\""} {
		if _, err := policy.ContactsFromTOML([]byte(bad + "\n[[sources]]\nid = \"x\"\npath = \"x.vcf\"\n")); err == nil {
			t.Errorf("%s should be refused", bad)
		}
	}
	inv, err := policy.ContactsFromTOML([]byte("[[sources]]\nid = \"x\"\npath = \"x.vcf\"\n"))
	if err != nil || inv.Refresh() != policy.DefaultContactsRefresh {
		t.Fatalf("default refresh: %v %v", err, inv.Refresh())
	}
}
