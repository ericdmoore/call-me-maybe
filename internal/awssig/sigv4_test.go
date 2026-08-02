package awssig

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The credentials from Amazon's published signing examples. They are famously
// fake — every SigV4 document in existence uses this pair — and the whole
// point of a test vector is that the expected signature is only reproducible
// with the exact key that produced it.
const (
	testAccessKey = "AKIDEXAMPLE"
	testSecretKey = "wJalrXUtnFEMI/K7MDENG" + "+bPxRfiCYEXAMPLEKEY"
)

var testTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

func vanilla(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Amz-Date", testTime.Format(timeFormat))
	return req
}

// Each step is checked on its own. A wrong signature is a 403 with no detail,
// so the failure has to name which of the four steps drifted — otherwise the
// only debugging tool is guessing.

func TestCanonicalRequestMatchesTheVector(t *testing.T) {
	got, signed := CanonicalRequest(vanilla(t), nil)

	want := strings.Join([]string{
		"GET",
		"/",
		"",
		"host:example.amazonaws.com",
		"x-amz-date:20150830T123600Z",
		"",
		"host;x-amz-date",
		emptyPayloadHash,
	}, "\n")

	if got != want {
		t.Errorf("canonical request:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if signed != "host;x-amz-date" {
		t.Errorf("signed headers = %q", signed)
	}
}

// Unlike the two either side of it, the hash on the last line here is not an
// independently published value — it is SHA-256 of the canonical request the
// test above already pins. So this guards the assembly of the four lines and
// their order, not the hash itself.
func TestStringToSignMatchesTheVector(t *testing.T) {
	canonical, _ := CanonicalRequest(vanilla(t), nil)
	got := StringToSign(canonical, "20150830/us-east-1/service/aws4_request", "20150830T123600Z")

	want := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		"20150830T123600Z",
		"20150830/us-east-1/service/aws4_request",
		"bb579772317eb040ac9ed261061d46c1f17a8133879d6129b6e1c25292927e63",
	}, "\n")

	if got != want {
		t.Errorf("string to sign:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// The strongest check available: byte-for-byte agreement with a completely
// independent implementation, on the actual request this package exists to
// sign. Captured from botocore with
//
//	aws polly synthesize-speech --text "Good day." --voice-id Amy \
//	  --output-format pcm --sample-rate 8000 --debug /dev/null
//
// which prints its own CanonicalRequest, StringToSign and Signature. AWS
// answered that request with UnrecognizedClientException rather than
// SignatureDoesNotMatch — it parsed the signature and only failed to know the
// (fake) key, so the shape below is one real AWS accepts.
func TestMatchesBotocoreOnARealPollyRequest(t *testing.T) {
	// Exactly the bytes botocore sent, key order and spaces included: the
	// payload hash covers them, so reformatting this string breaks the test.
	body := []byte(`{"OutputFormat": "pcm", "SampleRate": "8000", "Text": "Good day.", "VoiceId": "Amy"}`)
	when := time.Date(2026, 8, 2, 2, 51, 29, 0, time.UTC)

	req, err := http.NewRequest(http.MethodPost,
		"https://polly.us-east-1.amazonaws.com/v1/speech", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Sign sets this itself; the canonical request is checked first, so it
	// has to be present before that check.
	req.Header.Set("X-Amz-Date", when.Format(timeFormat))

	canonical, _ := CanonicalRequest(req, body)

	wantCanonical := strings.Join([]string{
		"POST",
		"/v1/speech",
		"",
		"content-type:application/json",
		"host:polly.us-east-1.amazonaws.com",
		"x-amz-date:20260802T025129Z",
		"",
		"content-type;host;x-amz-date",
		"2b4e9aaa3353ace94b1d021839b11414d11e673e4910bf1952a45adc4ca93f13",
	}, "\n")
	if canonical != wantCanonical {
		t.Errorf("canonical request differs from botocore:\n--- got ---\n%s\n--- want ---\n%s",
			canonical, wantCanonical)
	}

	s := Signer{Region: "us-east-1", Service: "polly"}
	if err := s.Sign(req, body, Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey}, when); err != nil {
		t.Fatal(err)
	}

	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260802/us-east-1/polly/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-date, " +
		"Signature=90a2739916398c68d4f11f51a1cce94e356527a54561b9a57f4e3b93f02ab13f"

	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("authorization differs from botocore:\n got: %s\nwant: %s", got, want)
	}
}

func TestAuthorizationHeaderMatchesTheVector(t *testing.T) {
	req := vanilla(t)
	s := Signer{Region: "us-east-1", Service: "service"}

	if err := s.Sign(req, nil, Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey}, testTime); err != nil {
		t.Fatal(err)
	}

	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, " +
		"Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"

	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("authorization:\n got: %s\nwant: %s", got, want)
	}
}

// ── the encoding rules that Go's url package gets differently ────────────

func TestURIEncodingIsRFC3986NotQueryEscape(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"a b", "a%20b"},                 // QueryEscape would write "+"
		{"a=b", "a%3Db"},                 // PathEscape leaves "=" alone
		{"a&b", "a%26b"},                 // and "&"
		{"~tilde", "~tilde"},             // unreserved, must not be escaped
		{"a/b", "a%2Fb"},                 // in a query value
		{"ünïcode", "%C3%BCn%C3%AFcode"}, // UTF-8 byte at a time
		{"-_.~", "-_.~"},                 // the full unreserved set
	} {
		if got := uriEncode(c.in, false); got != c.want {
			t.Errorf("uriEncode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := uriEncode("/a/b", true); got != "/a/b" {
		t.Errorf("path separators must survive: %q", got)
	}
}

// The path in a URL is already encoded once; SigV4 wants it encoded again for
// every service except S3. A space therefore signs as %2520, and getting this
// wrong is invisible until a prompt name contains a space.
func TestPathIsEncodedTwice(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/my%20path/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := canonicalURI(req.URL); got != "/my%2520path/x" {
		t.Errorf("canonicalURI = %q, want /my%%2520path/x", got)
	}
}

func TestEmptyPathBecomesSlash(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com", nil)
	if got := canonicalURI(req.URL); got != "/" {
		t.Errorf("canonicalURI = %q, want /", got)
	}
}

func TestQueryParametersSortByEncodedBytes(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/?B=2&a=1&A=3&a=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Uppercase sorts before lowercase, and a repeated key sorts by value.
	if got, want := canonicalQuery(req.URL), "A=3&B=2&a=0&a=1"; got != want {
		t.Errorf("canonicalQuery = %q, want %q", got, want)
	}
}

// ── headers ─────────────────────────────────────────────────────────────

// Go keeps Host out of the header map, so a signer that walks req.Header alone
// omits it and gets a 403 that looks exactly like a bad secret key.
func TestHostIsSignedEvenThoughGoHidesIt(t *testing.T) {
	req := vanilla(t)
	canonical, signed := CanonicalRequest(req, nil)
	if !strings.Contains(canonical, "host:example.amazonaws.com") {
		t.Errorf("host missing from canonical request:\n%s", canonical)
	}
	if !strings.Contains(signed, "host") {
		t.Errorf("host missing from signed headers: %q", signed)
	}
}

func TestHeaderValuesAreTrimmedAndCollapsed(t *testing.T) {
	req := vanilla(t)
	req.Header.Set("X-Amz-Meta-Test", "  a   b  ")
	canonical, _ := CanonicalRequest(req, nil)
	if !strings.Contains(canonical, "x-amz-meta-test:a b\n") {
		t.Errorf("value not collapsed:\n%s", canonical)
	}
}

// Go's transport sets User-Agent after we sign. Signing one produces a
// mismatch indistinguishable from a wrong key, so it is excluded.
func TestVolatileHeadersAreNotSigned(t *testing.T) {
	req := vanilla(t)
	req.Header.Set("User-Agent", "doorman/1.0")
	req.Header.Set("Expect", "100-continue")
	_, signed := CanonicalRequest(req, nil)

	for _, h := range []string{"user-agent", "expect", "content-length"} {
		if strings.Contains(signed, h) {
			t.Errorf("%s must not be signed, got %q", h, signed)
		}
	}
}

func TestSessionTokenIsSignedWhenPresent(t *testing.T) {
	req := vanilla(t)
	s := Signer{Region: "us-east-1", Service: "polly"}
	creds := Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey, SessionToken: "TOKEN"}

	if err := s.Sign(req, nil, creds, testTime); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Amz-Security-Token") != "TOKEN" {
		t.Error("session token header not set")
	}
	if !strings.Contains(req.Header.Get("Authorization"), "x-amz-security-token") {
		t.Errorf("token must be signed, not merely sent: %s", req.Header.Get("Authorization"))
	}
}

// ── the body ────────────────────────────────────────────────────────────

func TestPayloadIsHashed(t *testing.T) {
	body := []byte(`{"Text":"Good day.","VoiceId":"Amy"}`)
	req, err := http.NewRequest(http.MethodPost, "https://polly.us-east-1.amazonaws.com/v1/speech", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Amz-Date", testTime.Format(timeFormat))

	canonical, _ := CanonicalRequest(req, body)
	if strings.HasSuffix(canonical, emptyPayloadHash) {
		t.Error("a POST with a body must not sign the empty-payload hash")
	}
	if got := hexSHA256(body); !strings.HasSuffix(canonical, got) {
		t.Errorf("canonical request does not end with the payload hash:\n%s", canonical)
	}
}

// Changing one character of the body must change the signature. This is the
// property that makes signing worth doing at all.
func TestSignatureCoversTheBody(t *testing.T) {
	s := Signer{Region: "us-east-1", Service: "polly"}
	creds := Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey}

	sign := func(body string) string {
		req, _ := http.NewRequest(http.MethodPost, "https://polly.us-east-1.amazonaws.com/v1/speech", strings.NewReader(body))
		if err := s.Sign(req, []byte(body), creds, testTime); err != nil {
			t.Fatal(err)
		}
		return req.Header.Get("Authorization")
	}

	if sign(`{"Text":"Good day."}`) == sign(`{"Text":"Good dat."}`) {
		t.Error("the signature does not cover the body")
	}
}

// The chain is what scopes a signature to one day, one region, one service.
func TestSigningKeyIsScoped(t *testing.T) {
	base := SigningKey(testSecretKey, "20150830", "us-east-1", "polly")
	for _, other := range [][]byte{
		SigningKey(testSecretKey, "20150831", "us-east-1", "polly"),
		SigningKey(testSecretKey, "20150830", "eu-west-1", "polly"),
		SigningKey(testSecretKey, "20150830", "us-east-1", "s3"),
		SigningKey(testSecretKey+"x", "20150830", "us-east-1", "polly"),
	} {
		if string(base) == string(other) {
			t.Error("signing keys collide across scopes")
		}
	}
}

func TestSignRejectsMissingInputs(t *testing.T) {
	for _, c := range []struct {
		name  string
		s     Signer
		creds Credentials
	}{
		{"no region", Signer{Service: "polly"}, Credentials{AccessKeyID: "a", SecretAccessKey: "b"}},
		{"no service", Signer{Region: "us-east-1"}, Credentials{AccessKeyID: "a", SecretAccessKey: "b"}},
		{"no key id", Signer{Region: "us-east-1", Service: "polly"}, Credentials{SecretAccessKey: "b"}},
		{"no secret", Signer{Region: "us-east-1", Service: "polly"}, Credentials{AccessKeyID: "a"}},
	} {
		if err := c.s.Sign(vanilla(t), nil, c.creds, testTime); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

// A signed request has to survive an actual round trip through net/http —
// specifically, the transport must not alter a header we signed.
func TestSignedRequestSurvivesTheTransport(t *testing.T) {
	var gotAuth, gotDate string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotDate = r.Header.Get("Authorization"), r.Header.Get("X-Amz-Date")
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/speech", nil)
	if err != nil {
		t.Fatal(err)
	}
	s := Signer{Region: "us-east-1", Service: "polly"}
	if err := s.Sign(req, nil, Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey}, testTime); err != nil {
		t.Fatal(err)
	}
	sent := req.Header.Get("Authorization")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if gotAuth != sent {
		t.Errorf("transport altered Authorization:\n sent: %s\n got:  %s", sent, gotAuth)
	}
	if gotDate != testTime.Format(timeFormat) {
		t.Errorf("X-Amz-Date = %q", gotDate)
	}
}
