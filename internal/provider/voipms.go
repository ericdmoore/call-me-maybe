package provider

// The VoIP.ms REST API, as much of it as a balance check needs.
//
// # Verified against the live API, not from memory
//
// Checked on 2026-08-10 by probing the endpoint itself, because the published
// documentation at voip.ms/m/apidocs.php sits behind a bot challenge and the
// second-hand copies of it disagree in detail. What the API answered:
//
//	.../rest.php                                     {"status":"missing_method"}
//	...?api_username=&api_password=&method=getIP     {"status":"missing_credentials"}
//	...?api_username=x&api_password=y&method=getBalance
//	                                                 {"status":"invalid_credentials"}
//	...?...&method=notAMethod                        {"status":"invalid_method"}
//
// So: the endpoint is https://voip.ms/api/v1/rest.php, every call carries
// api_username, api_password and method, the answer is JSON with a `status`
// token, and getBalance is dispatched rather than rejected as unknown. The
// shape of a successful answer — a `balance` object holding `current_balance`
// — is the one thing no credential-free probe can show, and it is corroborated
// by two independent client libraries against the same documentation.
//
// A POST with a form body is answered with a SOAP fault, so this is a GET and
// the credentials ride in the query string. That is not a choice: it is the
// only shape the API accepts, and it is why every error path here is careful
// never to let a URL out. See [scrub].
//
// # Setup this will not do for you
//
// API access is off by default on a VoIP.ms account and has to be enabled in
// the portal, along with an allow-list of the IP addresses permitted to use
// it. Running this from a machine that is not on that list fails
// authentication in a way that looks exactly like a wrong password, which is
// why [explainVoIPMSStatus] says so rather than leaving somebody to guess.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// VoIPMSEndpoint is the REST API. Exported so the runbook and a test can name
// the same string rather than two copies of it.
const VoIPMSEndpoint = "https://voip.ms/api/v1/rest.php"

type voipms struct {
	endpoint string
	username string
	password string
	http     *http.Client
}

// Compile-time proof that this provider carries the capability, and that the
// capability is satisfied by a method rather than by hope.
var _ Balance = (*voipms)(nil)

func newVoIPMS(cfg Config) (Provider, error) {
	if cfg.Username == "" || cfg.Password == "" {
		return nil, ErrNoCredentials
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = VoIPMSEndpoint
	}
	return &voipms{
		endpoint: endpoint,
		username: cfg.Username,
		password: cfg.Password,
		http:     cfg.client(),
	}, nil
}

func (v *voipms) Name() string     { return "voip.ms" }
func (v *voipms) Billing() Billing { return Prepaid }

// voipmsResponse is the part of an answer this package reads. `status` is the
// error channel — the API returns HTTP 200 with a status token rather than an
// HTTP error — and `balance` is only present on success.
//
// current_balance is a json.Number because the API quotes it as a string
// ("10.00000") and a future version quoting it bare would otherwise stop the
// balance check on a box that was working yesterday. A balance is exactly the
// wrong thing to be brittle about.
type voipmsResponse struct {
	Status  string `json:"status"`
	Balance struct {
		CurrentBalance json.Number `json:"current_balance"`
	} `json:"balance"`
}

// Balance asks what is left on the account.
func (v *voipms) Balance(ctx context.Context) (Amount, error) {
	body, err := v.call(ctx, "getBalance")
	if err != nil {
		return Amount{}, err
	}

	var r voipmsResponse
	if err := json.Unmarshal(body, &r); err != nil {
		// Not %w, and not the body: a bot challenge or a proxy error page is
		// HTML that quotes the request line back, and the request line holds
		// the password.
		return Amount{}, fmt.Errorf("voip.ms: the answer was not JSON (%d bytes) — "+
			"something between here and the API answered instead of the API", len(body))
	}
	if r.Status != "success" {
		return Amount{}, fmt.Errorf("voip.ms: %s", explainVoIPMSStatus(r.Status))
	}
	if r.Balance.CurrentBalance == "" {
		return Amount{}, fmt.Errorf("voip.ms: the API reported success and no balance — " +
			"the response shape has changed and this client needs updating")
	}
	value, err := r.Balance.CurrentBalance.Float64()
	if err != nil {
		return Amount{}, fmt.Errorf("voip.ms: current_balance %q is not a number",
			r.Balance.CurrentBalance.String())
	}
	// No currency: an account is held in USD or CAD and getBalance does not say
	// which, so this reports the number and lets the operator know their own
	// account. Inventing a symbol would be a lie in the one output somebody
	// reads to decide whether to top up.
	return Amount{Value: value}, nil
}

// call performs one API method and returns the raw body.
func (v *voipms) call(ctx context.Context, method string) ([]byte, error) {
	// Built from the credential-free endpoint and given its query afterwards,
	// so a construction failure carries no password. Everything below refuses
	// to print a URL for the same reason.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("voip.ms: %w", err)
	}
	q := url.Values{}
	q.Set("api_username", v.username)
	q.Set("api_password", v.password)
	q.Set("method", method)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("User-Agent", "doorman")

	host := req.URL.Host
	resp, err := v.http.Do(req)
	if err != nil {
		return nil, scrub(err, host)
	}
	defer func() { _ = resp.Body.Close() }()

	// Bounded: an intermediary that answers with a megabyte is not our problem,
	// and neither is one that answers with a gigabyte.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, scrub(err, host)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("voip.ms: %s answered HTTP %d", host, resp.StatusCode)
	}
	return body, nil
}

// explainVoIPMSStatus turns a status token into something an operator can act
// on. The token is echoed and the API's own message is not: a token is a fixed
// vocabulary, while a message is text from the other end of a request that had
// a password in it.
func explainVoIPMSStatus(status string) string {
	switch status {
	case "":
		return "the answer carried no status — this was probably not the VoIP.ms API"
	case "invalid_credentials":
		return "invalid_credentials — the API username is the account email address and " +
			"the API password is the one set in Main Menu → Account Settings → API, " +
			"not the portal login and not the SIP sub-account. Check as well that API " +
			"access is enabled there and that this machine's IP is on its allow-list: " +
			"an unlisted address fails exactly like a wrong password"
	case "missing_credentials":
		return "missing_credentials — no API username or password reached the API"
	case "invalid_method", "missing_method":
		return status + " — the API did not recognise the call, which means this client " +
			"and the API have drifted apart"
	default:
		return status + " — VoIP.ms answers with a status token rather than an HTTP error. " +
			"The usual causes are API access not enabled for the account, or this " +
			"machine's IP not on the API allow-list (Main Menu → Account Settings → API)"
	}
}
