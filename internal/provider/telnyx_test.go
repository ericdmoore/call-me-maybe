package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The key used throughout, so every test can assert it never comes back out.
// Fiction: nothing here may ever need a real account.
const testTelnyxKey = "KEY-not-a-real-telnyx-key-0123456789"

func telnyxServer(t *testing.T, handler http.HandlerFunc) *telnyx {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p, err := New("telnyx", Config{Key: testTelnyxKey, Endpoint: srv.URL, HTTP: srv.Client()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p.(*telnyx)
}

// telnyxAccount answers the three documented endpoints from fixtures, after
// checking the request is shaped the way the reference says it must be.
type telnyxAccount struct {
	t           *testing.T
	numbers     []string // JSON objects, one per number
	connections []string
	pageSize    int // what the fake serves per page; 0 means whatever was asked
	balance     string
	omitMeta    bool
}

func (a *telnyxAccount) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "Bearer "+testTelnyxKey {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":[{"code":"10009","title":"Authentication failed","detail":"key ` + got + `"}]}`))
		return
	}
	if strings.Contains(r.URL.RawQuery, testTelnyxKey) {
		a.t.Errorf("the key reached the query string: %s", r.URL.RawQuery)
	}
	switch r.URL.Path {
	case "/balance":
		_, _ = w.Write([]byte(a.balance))
	case "/phone_numbers":
		a.page(w, r, a.numbers)
	case "/credential_connections":
		a.page(w, r, a.connections)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *telnyxAccount) page(w http.ResponseWriter, r *http.Request, items []string) {
	q := r.URL.Query()
	number, _ := strconv.Atoi(q.Get("page[number]"))
	size, _ := strconv.Atoi(q.Get("page[size]"))
	if size != telnyxPageSize {
		a.t.Errorf("page[size] = %q, want the documented maximum %d", q.Get("page[size]"), telnyxPageSize)
	}
	if a.pageSize > 0 {
		size = a.pageSize
	}
	if number < 1 {
		a.t.Errorf("page[number] = %q, want 1-based", q.Get("page[number]"))
		number = 1
	}
	total := (len(items) + size - 1) / size
	lo, hi := (number-1)*size, number*size
	if lo > len(items) {
		lo = len(items)
	}
	if hi > len(items) {
		hi = len(items)
	}
	body := `{"data":[` + strings.Join(items[lo:hi], ",") + `]`
	if !a.omitMeta {
		body += fmt.Sprintf(`,"meta":{"page_number":%d,"total_pages":%d,"total_results":%d,"page_size":%d}`,
			number, total, len(items), size)
	}
	_, _ = w.Write([]byte(body + "}"))
}

func numberJSON(e164, status, connID, connName string) string {
	return fmt.Sprintf(`{"id":"n%s","record_type":"phone_number","phone_number":%q,"status":%q,`+
		`"connection_id":%q,"connection_name":%q,"emergency_enabled":false}`,
		strings.TrimPrefix(e164, "+"), e164, status, connID, connName)
}

func connectionJSON(id, name, user string) string {
	return fmt.Sprintf(`{"id":%q,"record_type":"credential_connection","connection_name":%q,"user_name":%q,"active":true}`,
		id, name, user)
}

func TestTelnyxListsNumbersWithTheConnectionsUsername(t *testing.T) {
	acct := &telnyxAccount{t: t,
		numbers: []string{
			numberJSON("+15125550142", "active", "1001", "house"),
			numberJSON("+15125550143", "active", "", ""),
			numberJSON("+15125550144", "port-pending", "2002", "office-fqdn"),
		},
		connections: []string{connectionJSON("1001", "house", "cmm-home")},
	}
	tx := telnyxServer(t, acct.ServeHTTP)

	got, err := tx.Numbers(context.Background())
	if err != nil {
		t.Fatalf("Numbers: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d numbers, want 3: %+v", len(got), got)
	}

	assigned := got[0]
	if assigned.E164 != "+15125550142" || !assigned.Active || assigned.Status != "active" {
		t.Errorf("first number = %+v", assigned)
	}
	if assigned.Connection != (Connection{ID: "1001", Name: "house", Username: "cmm-home"}) {
		t.Errorf("the connection's SIP username was not joined in: %+v", assigned.Connection)
	}
	if assigned.Emergency == nil || *assigned.Emergency {
		t.Errorf("emergency_enabled=false must come through as a known false: %v", assigned.Emergency)
	}

	// The case a portal makes easy: bought, and pointed at nothing.
	if got[1].Connection.Assigned() {
		t.Errorf("a number with no connection reads as assigned: %+v", got[1].Connection)
	}

	// An FQDN or IP connection is not a credential connection, so nothing
	// registers as it and there is no username to report.
	if got[2].Active || got[2].Status != "port-pending" {
		t.Errorf("a porting number is not active: %+v", got[2])
	}
	if got[2].Connection.Username != "" || got[2].Connection.Name != "office-fqdn" {
		t.Errorf("non-credential connection = %+v, want a name and no username", got[2].Connection)
	}
}

// The reference's examples give a number's connection_id and a credential
// connection's id in different shapes. Until a live answer settles it, a
// unique display name is the fallback — and an ambiguous one is not used.
func TestTelnyxFallsBackToAUniqueConnectionName(t *testing.T) {
	acct := &telnyxAccount{t: t,
		numbers: []string{
			numberJSON("+15125550142", "active", "1293384261075731499", "house"),
			numberJSON("+15125550143", "active", "1293384261075731500", "twice"),
		},
		connections: []string{
			connectionJSON("6a09cdc3-8948-47f0-aa62-74ac943d6c58", "house", "cmm-home"),
			connectionJSON("6a09cdc3-0000-0000-0000-000000000001", "twice", "one"),
			connectionJSON("6a09cdc3-0000-0000-0000-000000000002", "twice", "two"),
		},
	}
	got, err := telnyxServer(t, acct.ServeHTTP).Numbers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Connection.Username != "cmm-home" {
		t.Errorf("a unique name should resolve the username: %+v", got[0].Connection)
	}
	if got[1].Connection.Username != "" {
		t.Errorf("an ambiguous name must not pick a username: %+v", got[1].Connection)
	}
}

// Pagination is where a partial list would come from, and a partial list is
// a number reported missing that the account does hold.
func TestTelnyxReadsEveryPage(t *testing.T) {
	var numbers []string
	for i := 0; i < 7; i++ {
		numbers = append(numbers, numberJSON(fmt.Sprintf("+1512555%04d", 100+i), "active", "1001", "house"))
	}
	for _, omitMeta := range []bool{false, true} {
		acct := &telnyxAccount{t: t, numbers: numbers, pageSize: 3, omitMeta: omitMeta,
			connections: []string{connectionJSON("1001", "house", "cmm-home")}}
		if omitMeta {
			// Without meta the only end-of-list signal is a short page, so the
			// fake must serve full pages of the size that was asked for.
			acct.pageSize = 0
		}
		got, err := telnyxServer(t, acct.ServeHTTP).Numbers(context.Background())
		if err != nil {
			t.Fatalf("omitMeta=%v: %v", omitMeta, err)
		}
		if len(got) != 7 {
			t.Errorf("omitMeta=%v: read %d numbers across pages, want 7", omitMeta, len(got))
		}
	}
}

// A server that never says it is done must not be read forever, and must not
// be reported as the whole list either.
func TestTelnyxRefusesAListingWithNoEnd(t *testing.T) {
	full := make([]string, telnyxPageSize)
	for i := range full {
		full[i] = numberJSON(fmt.Sprintf("+1512555%04d", i), "active", "", "")
	}
	requests := 0
	tx := telnyxServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"data":[` + strings.Join(full, ",") + `]}`))
	})
	_, err := tx.Numbers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "partial list") {
		t.Fatalf("want a refusal to report a partial list, got %v", err)
	}
	if requests != telnyxMaxPages {
		t.Errorf("made %d requests, want the cap of %d", requests, telnyxMaxPages)
	}
}

func TestTelnyxAuthFailureSaysWhichKeyAndLeaksNothing(t *testing.T) {
	srv := httptest.NewServer(&telnyxAccount{t: t})
	t.Cleanup(srv.Close)
	p, err := New("telnyx", Config{Key: "KEY-wrong-" + testTelnyxKey, Endpoint: srv.URL, HTTP: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"Numbers": func() error { _, err := p.(NumberLister).Numbers(context.Background()); return err },
		"Balance": func() error { _, err := p.(Balance).Balance(context.Background()); return err },
	} {
		err := call()
		if err == nil {
			t.Fatalf("%s: a refused key must be an error", name)
		}
		for _, want := range []string{"401", "error 10009", "API Keys"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error does not mention %q: %v", name, want, err)
			}
		}
		assertNoTelnyxKey(t, err.Error())
		// The body's detail echoed the key back; it must not be repeated.
		if strings.Contains(err.Error(), "Authentication failed") {
			t.Errorf("%s: the API's own text was repeated: %v", name, err)
		}
	}
}

func TestTelnyxErrorsNameTheStatusAndNothingElse(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusForbidden:       "permissions",
		http.StatusNotFound:        "drifted",
		http.StatusTooManyRequests: "rate limited",
		http.StatusBadGateway:      "Telnyx's side",
		http.StatusTeapot:          "does not know",
	} {
		tx := telnyxServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"errors":[{"code":"<script>","detail":"` + r.Header.Get("Authorization") + `"}]}`))
		})
		_, err := tx.Numbers(context.Background())
		if err == nil || !strings.Contains(err.Error(), strconv.Itoa(status)) || !strings.Contains(err.Error(), want) {
			t.Errorf("HTTP %d: error = %v, want the status and %q", status, err, want)
			continue
		}
		if strings.Contains(err.Error(), "<script>") {
			t.Errorf("HTTP %d: a code that is not a code was echoed: %v", status, err)
		}
		assertNoTelnyxKey(t, err.Error())
	}
}

func TestTelnyxRefusesAnAnswerThatIsNotJSON(t *testing.T) {
	tx := telnyxServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>proxy error for " + r.Header.Get("Authorization") + "</html>"))
	})
	if _, err := tx.Numbers(context.Background()); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("Numbers: want a not-JSON error, got %v", err)
	} else {
		assertNoTelnyxKey(t, err.Error())
	}
	if _, err := tx.Balance(context.Background()); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("Balance: want a not-JSON error, got %v", err)
	} else {
		assertNoTelnyxKey(t, err.Error())
	}
}

// JSON that does not fit is the API answering in a shape this client was not
// written for — not a proxy — and the error says which field, never its value.
func TestTelnyxNamesAShapeDriftInsteadOfBlamingAProxy(t *testing.T) {
	for name, tc := range map[string]struct{ body, says string }{
		"page count as a string": {`{"data":[],"meta":{"page_number":1,"total_pages":"3"}}`,
			"meta.total_pages is a JSON string, and this client expects a number"},
		"connection id a number": {`{"data":[{"id":"1","phone_number":"+15125550142","status":"active","connection_id":99017,"connection_name":"house"}],"meta":{"total_pages":1}}`,
			"data.connection_id is a JSON number, and this client expects a string"},
		"data an object": {`{"data":{"phone_number":"+15125550142"}}`,
			"data is a JSON object, and this client expects an array"},
	} {
		tx := telnyxServer(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(tc.body))
		})
		_, err := tx.Numbers(context.Background())
		if err == nil || !strings.Contains(err.Error(), "shape this client does not expect") ||
			!strings.Contains(err.Error(), tc.says) || strings.Contains(err.Error(), "not JSON") {
			t.Errorf("%s: want a shape error saying %q, got %v", name, tc.says, err)
			continue
		}
		if strings.Contains(err.Error(), "99017") || strings.Contains(err.Error(), "+1512") {
			t.Errorf("%s: a value from the body reached the error: %v", name, err)
		}
	}
	tx := telnyxServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"balance":true,"currency":"USD"}}`))
	})
	if _, err := tx.Balance(context.Background()); err == nil || !strings.Contains(err.Error(), "shape") {
		t.Errorf("Balance: want a shape error, got %v", err)
	}
}

// A failure on the second listing fails the whole answer: numbers without
// their connections' usernames would read as "cannot tell" across the board.
func TestTelnyxFailsWholeWhenTheConnectionsCannotBeRead(t *testing.T) {
	tx := telnyxServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/credential_connections" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page_number":1,"total_pages":0}}`))
	})
	if _, err := tx.Numbers(context.Background()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("want the connections listing's 403, got %v", err)
	}
}

func TestTelnyxBalanceIsTheAvailableCredit(t *testing.T) {
	for _, tc := range []struct {
		body string
		want float64
		cur  string
	}{
		// What is left to spend: balance plus the credit line. A bare balance
		// reads negative on an account whose credit line is working.
		{`{"data":{"record_type":"balance","balance":"-20.00","credit_limit":"100.00","available_credit":"80.00","currency":"USD"}}`, 80, "USD"},
		// The unwrapped shape the reference's example shows.
		{`{"balance":"300.00","available_credit":"300.00","currency":"usd"}`, 300, "USD"},
		// No available_credit: the balance is the answer.
		{`{"data":{"balance":"12.5","currency":"EUR"}}`, 12.5, "EUR"},
		// Quoted or bare, a number must not stop a check that worked yesterday.
		{`{"data":{"available_credit":7.25,"currency":"USD"}}`, 7.25, "USD"},
	} {
		tx := telnyxServer(t, (&telnyxAccount{t: t, balance: tc.body}).ServeHTTP)
		got, err := tx.Balance(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		if got.Value != tc.want || got.Currency != tc.cur {
			t.Errorf("%s: balance = %+v, want %v %s", tc.body, got, tc.want, tc.cur)
		}
	}
}

func TestTelnyxBalanceRefusesAShapeItCannotRead(t *testing.T) {
	for _, body := range []string{
		`{"data":{"currency":"USD"}}`,
		`{"data":{"balance":"lots","currency":"USD"}}`,
		`{"data":"nope"}`,
	} {
		tx := telnyxServer(t, (&telnyxAccount{t: t, balance: body}).ServeHTTP)
		if got, err := tx.Balance(context.Background()); err == nil {
			t.Errorf("%s: want an error, got %+v", body, got)
		}
	}
}

func TestTelnyxNeedsAKeyAndOnlyAKey(t *testing.T) {
	_, err := New("telnyx", Config{Username: "owner@example.invalid", Password: "x"})
	if !errors.Is(err, ErrNoCredentials) {
		t.Errorf("a login and password are not a Telnyx key: err = %v", err)
	}
	p, err := New("Telnyx", Config{Key: testTelnyxKey})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "telnyx" || p.Billing() != Prepaid {
		t.Errorf("telnyx should identify as prepaid: %q %q", p.Name(), p.Billing())
	}
	if got := p.(*telnyx).endpoint; got != TelnyxEndpoint {
		t.Errorf("endpoint = %q, want %q", got, TelnyxEndpoint)
	}
}

func TestTelnyxTransportErrorCarriesTheHostAndNotTheKey(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	client, endpoint := srv.Client(), srv.URL
	srv.Close()
	p, err := New("telnyx", Config{Key: testTelnyxKey, Endpoint: endpoint, HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.(NumberLister).Numbers(context.Background())
	if err == nil {
		t.Fatal("expected a transport error against a closed server")
	}
	assertNoTelnyxKey(t, err.Error())
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("the host is the part that is safe to print, and it is missing: %v", err)
	}
	if strings.Contains(err.Error(), "page%5B") || strings.Contains(err.Error(), "page[") {
		t.Errorf("the request URL leaked into the error: %v", err)
	}
}

func assertNoTelnyxKey(t *testing.T, text string) {
	t.Helper()
	for _, forbidden := range []string{testTelnyxKey, "Bearer"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("output leaks %q: %s", forbidden, text)
		}
	}
}
