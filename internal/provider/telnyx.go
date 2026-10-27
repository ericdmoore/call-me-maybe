package provider

// Telnyx, through its API v2: the balance, and which numbers the account
// holds and which connection each one is assigned to.
//
// # Written from the documentation, NOT verified against the live API
//
// Written on 2026-10-23 from the published reference at
// developers.telnyx.com, before anybody on this project held a Telnyx account.
// Unlike voipms.go, nothing here has been checked against a real answer, so
// treat the field names below as the documentation's claim rather than as
// observed fact. What the reference says, and what this client relies on:
//
//	GET https://api.telnyx.com/v2/balance
//	    {"data":{"balance":"300.00","currency":"USD",
//	             "credit_limit":"100.00","available_credit":"400.00"}}
//	GET https://api.telnyx.com/v2/phone_numbers?page[number]=N&page[size]=250
//	    {"data":[{"id","phone_number":"+1…","status":"active",
//	              "connection_id","connection_name","emergency_enabled"}],
//	     "meta":{"page_number","total_pages","total_results","page_size"}}
//	GET https://api.telnyx.com/v2/credential_connections?page[…]
//	    {"data":[{"id","connection_name","user_name","active"}],"meta":{…}}
//
// Every call carries `Authorization: Bearer <API key>`. page[size] is at most
// 250. The balance example in the reference is shown without the `data`
// envelope every other v2 answer has, so both shapes are accepted rather than
// one guessed at.
//
// The first live run is the verification. When it happens, replace this
// paragraph with what the API actually answered, the way voipms.go records it.
//
// # The key is in a header, and still never printed
//
// Better than VoIP.ms's query string: the URL carries no credential. The rule
// is held anyway — no error here quotes a URL with its query, a request, a
// header or a body from the other end — because the next person to touch this
// file should not have to work out which provider made it safe.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
)

// TelnyxEndpoint is the API v2 base. Exported so the runbook and a test can
// name the same string rather than two copies of it.
const TelnyxEndpoint = "https://api.telnyx.com/v2"

// telnyxPageSize is the documented maximum, so a household with a handful of
// numbers is one request.
const telnyxPageSize = 250

// telnyxMaxPages bounds a listing at 10,000 records. Past that the answer is
// an error rather than a partial list: a list cut short would report a number
// the account does hold as "not on this account", which is the false alarm
// this command must never raise.
const telnyxMaxPages = 40

type telnyx struct {
	endpoint string
	key      string
	http     *http.Client
}

var (
	_ Balance      = (*telnyx)(nil)
	_ NumberLister = (*telnyx)(nil)
)

func newTelnyx(cfg Config) (Provider, error) {
	if cfg.Key == "" {
		return nil, ErrNoCredentials
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = TelnyxEndpoint
	}
	return &telnyx{
		endpoint: strings.TrimRight(endpoint, "/"),
		key:      cfg.Key,
		http:     cfg.client(),
	}, nil
}

func (t *telnyx) Name() string { return "telnyx" }

// Billing is prepaid. A Telnyx account is topped up by default, and one with
// a credit agreement still has an available_credit that reaches zero — which
// is the number reported — so "how much is left" is a real question either
// way. site/src/data/providers.ts lists Telnyx as "either"; this is the half
// of that which has a balance to run out.
func (t *telnyx) Billing() Billing { return Prepaid }

type telnyxBalance struct {
	Balance         json.Number `json:"balance"`
	AvailableCredit json.Number `json:"available_credit"`
	Currency        string      `json:"currency"`
}

// Balance reports available_credit — balance plus any credit limit — because
// that is what reaching zero on stops calls. A bare balance would read
// negative and alarming on an account with a credit line that is working
// perfectly. Falls back to balance when the answer carries no
// available_credit.
func (t *telnyx) Balance(ctx context.Context) (Amount, error) {
	body, err := t.get(ctx, "/balance", nil)
	if err != nil {
		return Amount{}, err
	}
	var wrapped struct {
		Data *telnyxBalance `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return Amount{}, errTelnyxUnreadable(body, err)
	}
	b := wrapped.Data
	if b == nil {
		// The unwrapped shape the reference's example shows.
		b = &telnyxBalance{}
		if err := json.Unmarshal(body, b); err != nil {
			return Amount{}, errTelnyxUnreadable(body, err)
		}
	}
	raw := b.AvailableCredit
	if raw == "" {
		raw = b.Balance
	}
	if raw == "" {
		return Amount{}, fmt.Errorf("telnyx: the API answered and carried no balance — " +
			"the response shape has changed and this client needs updating")
	}
	value, err := raw.Float64()
	if err != nil {
		return Amount{}, fmt.Errorf("telnyx: balance %q is not a number", raw.String())
	}
	return Amount{Value: value, Currency: strings.ToUpper(strings.TrimSpace(b.Currency))}, nil
}

type telnyxNumber struct {
	ID               string `json:"id"`
	PhoneNumber      string `json:"phone_number"`
	Status           string `json:"status"`
	ConnectionID     string `json:"connection_id"`
	ConnectionName   string `json:"connection_name"`
	EmergencyEnabled *bool  `json:"emergency_enabled"`
}

type telnyxCredentialConnection struct {
	ID             string `json:"id"`
	ConnectionName string `json:"connection_name"`
	UserName       string `json:"user_name"`
}

// Numbers lists every number on the account, with the SIP username of the
// connection each is assigned to when that connection is a credential one.
//
// Two listings rather than one, because a phone number names its connection
// by id and display name and never by the username that registers as it —
// and the username is the only thing trunks.toml knows. Joining them here is
// what lets the CLI say "assigned to the connection this trunk registers as"
// instead of printing an id nobody can check by eye.
func (t *telnyx) Numbers(ctx context.Context) ([]Number, error) {
	numbers, err := telnyxList[telnyxNumber](ctx, t, "/phone_numbers")
	if err != nil {
		return nil, err
	}
	conns, err := telnyxList[telnyxCredentialConnection](ctx, t, "/credential_connections")
	if err != nil {
		return nil, err
	}

	byID := make(map[string]telnyxCredentialConnection, len(conns))
	byName := make(map[string][]telnyxCredentialConnection, len(conns))
	for _, c := range conns {
		byID[c.ID] = c
		byName[c.ConnectionName] = append(byName[c.ConnectionName], c)
	}

	out := make([]Number, 0, len(numbers))
	for _, n := range numbers {
		conn := Connection{ID: n.ConnectionID, Name: n.ConnectionName}
		if c, ok := byID[n.ConnectionID]; ok && n.ConnectionID != "" {
			conn.Username = c.UserName
		} else if same := byName[n.ConnectionName]; n.ConnectionName != "" && len(same) == 1 {
			// The reference's examples give a number's connection_id and a
			// connection's id in different shapes. Until a live answer says
			// they line up, a unique display name is the fallback — never an
			// ambiguous one, which would pick a username at random.
			conn.Username = same[0].UserName
		}
		out = append(out, Number{
			E164:       n.PhoneNumber,
			Status:     n.Status,
			Active:     n.Status == "active",
			Connection: conn,
			Emergency:  n.EmergencyEnabled,
		})
	}
	return out, nil
}

// telnyxPage is the envelope every v2 listing shares.
type telnyxPage[T any] struct {
	Data []T `json:"data"`
	Meta struct {
		PageNumber int `json:"page_number"`
		TotalPages int `json:"total_pages"`
	} `json:"meta"`
}

// telnyxList reads every page of a listing, or fails whole. A partial answer
// is refused because the caller compares against it: a page that did not
// arrive is a number reported missing that is not.
func telnyxList[T any](ctx context.Context, t *telnyx, path string) ([]T, error) {
	var all []T
	for page := 1; ; page++ {
		if page > telnyxMaxPages {
			return nil, fmt.Errorf("telnyx: %s has more than %d pages of %d — more than this "+
				"client will read, and a partial list would report numbers missing that are not",
				path, telnyxMaxPages, telnyxPageSize)
		}
		q := url.Values{}
		q.Set("page[number]", strconv.Itoa(page))
		q.Set("page[size]", strconv.Itoa(telnyxPageSize))
		body, err := t.get(ctx, path, q)
		if err != nil {
			return nil, err
		}
		var p telnyxPage[T]
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, errTelnyxUnreadable(body, err)
		}
		all = append(all, p.Data...)

		switch {
		case len(p.Data) == 0:
			return all, nil
		case p.Meta.TotalPages > 0:
			if page >= p.Meta.TotalPages {
				return all, nil
			}
		case len(p.Data) < telnyxPageSize:
			// No meta at all: a short page is the last one.
			return all, nil
		}
	}
}

// get performs one request and returns the body of a 200.
func (t *telnyx) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.endpoint+path, nil)
	if err != nil {
		return nil, fmt.Errorf("telnyx: %w", err)
	}
	if query != nil {
		req.URL.RawQuery = query.Encode()
	}
	req.Header.Set("Authorization", "Bearer "+t.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "doorman")

	host := req.URL.Host
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, scrub(err, host)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, scrub(err, host)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telnyx: %s answered HTTP %d%s — %s", host, resp.StatusCode,
			telnyxErrorCode(body), explainTelnyxStatus(resp.StatusCode))
	}
	return body, nil
}

// telnyxErrorCode is the API's own error code, when it sent one, and nothing
// else from the body. A code is a fixed vocabulary somebody can look up; the
// title and detail beside it are text from the other end of a request that
// carried a key, and are not repeated.
func telnyxErrorCode(body []byte) string {
	var e struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &e) != nil || len(e.Errors) == 0 {
		return ""
	}
	code := e.Errors[0].Code
	if code == "" || len(code) > 8 || strings.Trim(code, "0123456789") != "" {
		return ""
	}
	return " (error " + code + ")"
}

func explainTelnyxStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "the API key was refused. It is a key from the API Keys page of the " +
			"Mission Control portal, not the SIP connection's password, and a key " +
			"that was deleted or rotated fails exactly like a typo"
	case status == http.StatusForbidden:
		return "the key is valid but may not read this — check the key's permissions " +
			"and that the account is not suspended"
	case status == http.StatusNotFound:
		return "the API did not recognise the path, which means this client and the " +
			"API have drifted apart"
	case status == http.StatusTooManyRequests:
		return "rate limited; try again in a minute"
	case status >= 500:
		return "Telnyx's side failed; nothing here to fix, try again later"
	default:
		return "an answer this client does not know how to read"
	}
}

// errTelnyxUnreadable says which of two different things went wrong when an
// answer would not decode. A body that is not JSON at all came from something
// between here and the API. A body that IS JSON but does not fit is the API
// itself, answering in a shape other than the one this client was written
// from — the likeliest first failure of a client nobody has run live — and
// blaming an intermediary for it sends the operator looking for a proxy that
// does not exist.
//
// The shape error names the field path and the JSON kind found there, both of
// which come from this client's own types and the JSON grammar. Never the
// value: encoding/json describes a misplaced number as "number <literal>", so
// only the first word of that description is kept.
func errTelnyxUnreadable(body []byte, err error) error {
	if !json.Valid(body) {
		return errTelnyxNotJSON(len(body))
	}
	where := ""
	var typ *json.UnmarshalTypeError
	if errors.As(err, &typ) {
		kind := "value"
		if f := strings.Fields(typ.Value); len(f) > 0 {
			kind = f[0]
		}
		field := typ.Field
		if field == "" {
			field = "the top level"
		}
		where = fmt.Sprintf(" (%s is a JSON %s, and this client expects %s)", field, kind, jsonKindOf(typ.Type))
	}
	return fmt.Errorf("telnyx: the API answered JSON in a shape this client does not expect%s — "+
		"the response shape has changed and this client needs updating", where)
}

// jsonKindOf names what a Go type decodes from, in the JSON grammar's words,
// so the shape error reads in terms of the answer rather than of this code.
func jsonKindOf(t reflect.Type) string {
	if t == nil {
		return "something else"
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Struct, reflect.Map:
		return "an object"
	case reflect.Bool:
		return "a boolean"
	case reflect.String:
		if t.Name() == "Number" {
			return "a number"
		}
		return "a string"
	case reflect.Pointer:
		return jsonKindOf(t.Elem())
	default:
		return "a number"
	}
}

func errTelnyxNotJSON(n int) error {
	// Not the body: an intermediary's error page is HTML nobody asked to see,
	// and it is not this client's to repeat.
	return fmt.Errorf("telnyx: the answer was not JSON (%d bytes) — "+
		"something between here and the API answered instead of the API", n)
}
