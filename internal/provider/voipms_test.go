package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The password used throughout, so every test can assert it never comes back
// out. It is fiction: nothing here needs a real account, and nothing here may
// ever need one.
const testAPIPassword = "not-a-real-api-key-0123456789"

func voipmsServer(t *testing.T, handler http.HandlerFunc) (*voipms, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p, err := New("voip.ms", Config{
		Username: "owner@example.invalid",
		Password: testAPIPassword,
		Endpoint: srv.URL,
		HTTP:     srv.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p.(*voipms), srv
}

func TestVoIPMSAsksTheDocumentedQuestion(t *testing.T) {
	var got struct{ user, password, method string }
	v, _ := voipmsServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		got.user, got.password, got.method = q.Get("api_username"), q.Get("api_password"), q.Get("method")
		_, _ = w.Write([]byte(`{"status":"success","balance":{"current_balance":"12.34500"}}`))
	})

	amount, err := v.Balance(context.Background())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	// Verified against the live API on 2026-08-10: these three parameter names
	// and this method name are what it dispatches on.
	if got.user != "owner@example.invalid" || got.password != testAPIPassword {
		t.Errorf("credentials not sent as api_username/api_password: %+v", got)
	}
	if got.method != "getBalance" {
		t.Errorf("method = %q, want getBalance", got.method)
	}
	if amount.Value < 12.344 || amount.Value > 12.346 {
		t.Errorf("balance = %v, want 12.345", amount.Value)
	}
	// VoIP.ms does not say which currency an account is held in, and inventing
	// one would be a lie in the output somebody reads to decide whether to pay.
	if amount.Currency != "" {
		t.Errorf("currency = %q, want empty — the API does not report one", amount.Currency)
	}
}

// The API answers HTTP 200 with a status token rather than an HTTP error, so a
// client that only checked the status code would read "invalid credentials" as
// a balance of zero — the exact confusion this milestone exists to prevent.
func TestVoIPMSTreatsAStatusTokenAsAnError(t *testing.T) {
	v, _ := voipmsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"invalid_credentials","message":"Username or Password is incorrect"}`))
	})
	_, err := v.Balance(context.Background())
	if err == nil {
		t.Fatal("an invalid_credentials answer must not read as a zero balance")
	}
	// The setup step people actually hit: API access is off by default and the
	// calling IP has to be allow-listed in the portal.
	for _, want := range []string{"invalid_credentials", "allow-list", "API"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
	assertNoCredentials(t, err.Error())
}

func TestVoIPMSExplainsAnUnknownStatus(t *testing.T) {
	v, _ := voipmsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"some_new_token"}`))
	})
	_, err := v.Balance(context.Background())
	if err == nil || !strings.Contains(err.Error(), "some_new_token") {
		t.Fatalf("an unrecognised status should be echoed: %v", err)
	}
}

// A bot challenge, a captive portal, or a proxy error page. All of them quote
// the request line back, and the request line holds the password.
func TestVoIPMSRefusesAnAnswerThatIsNotJSONAndQuotesNoneOfIt(t *testing.T) {
	v, _ := voipmsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>Attention Required! " + r.URL.String() + "</body></html>"))
	})
	_, err := v.Balance(context.Background())
	if err == nil {
		t.Fatal("HTML is not a balance")
	}
	assertNoCredentials(t, err.Error())
}

func TestVoIPMSReportsAnHTTPErrorWithoutTheURL(t *testing.T) {
	v, _ := voipmsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("blocked: " + r.URL.String()))
	})
	_, err := v.Balance(context.Background())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("want an error naming the status code, got %v", err)
	}
	assertNoCredentials(t, err.Error())
}

// The *url.Error trap. net/http wraps every transport failure together with
// the full URL, and here the URL carries the password — the API takes
// credentials no other way. No name-based analyzer can catch it, because by
// then the identifier is `err`.
func TestATransportErrorNeverCarriesTheURL(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	client := srv.Client()
	endpoint := srv.URL
	srv.Close() // nothing is listening now

	p, err := New("voip.ms", Config{
		Username: "owner@example.invalid",
		Password: testAPIPassword,
		Endpoint: endpoint,
		HTTP:     client,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.(Balance).Balance(context.Background())
	if err == nil {
		t.Fatal("expected a transport error against a closed server")
	}
	assertNoCredentials(t, err.Error())
	// The host survives, because an error with nothing in it is unactionable.
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("the host is the part that is safe to print, and it is missing: %v", err)
	}
}

func TestATimeoutIsReportedWithoutTheURL(t *testing.T) {
	v, _ := voipmsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"status":"success","balance":{"current_balance":"1.00"}}`))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := v.Balance(ctx)
	if err == nil {
		t.Fatal("expected a deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a caller should be able to tell a timeout apart: %v", err)
	}
	assertNoCredentials(t, err.Error())
}

// A future API quoting the number bare rather than as a string must not stop a
// balance check on a box that was working yesterday.
func TestVoIPMSAcceptsTheBalanceQuotedOrNot(t *testing.T) {
	for _, body := range []string{
		`{"status":"success","balance":{"current_balance":"7.50"}}`,
		`{"status":"success","balance":{"current_balance":7.5}}`,
	} {
		v, _ := voipmsServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		amount, err := v.Balance(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if amount.Value != 7.5 {
			t.Errorf("%s: balance = %v, want 7.5", body, amount.Value)
		}
	}
}

// Success with nothing in it is the shape change worth failing loudly on:
// silently reporting zero would be indistinguishable from an empty account.
func TestVoIPMSRefusesASuccessWithNoBalance(t *testing.T) {
	v, _ := voipmsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success"}`))
	})
	if amount, err := v.Balance(context.Background()); err == nil {
		t.Fatalf("expected an error, got %v", amount)
	}
}

func TestVoIPMSRefusesABalanceThatIsNotANumber(t *testing.T) {
	v, _ := voipmsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","balance":{"current_balance":"lots"}}`))
	})
	if _, err := v.Balance(context.Background()); err == nil {
		t.Fatal("expected an error for a non-numeric balance")
	}
}

func TestVoIPMSDefaultsToTheDocumentedEndpoint(t *testing.T) {
	p, err := New("voip.ms", Config{Username: "a@example.invalid", Password: "x"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.(*voipms).endpoint; got != VoIPMSEndpoint {
		t.Errorf("endpoint = %q, want %q", got, VoIPMSEndpoint)
	}
	if p.Name() != "voip.ms" || p.Billing() != Prepaid {
		t.Errorf("voip.ms should identify as prepaid: %q %q", p.Name(), p.Billing())
	}
}

// assertNoCredentials is the rule this package exists under: nothing it
// produces may carry the password, and nothing may carry the query string that
// holds it.
func assertNoCredentials(t *testing.T, text string) {
	t.Helper()
	for _, forbidden := range []string{testAPIPassword, "api_password", "api_username"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("output leaks %q: %s", forbidden, text)
		}
	}
}
