// Package awssig signs HTTP requests with AWS Signature Version 4.
//
// Hand-rolled for the same reason internal/ari is hand-rolled: one endpoint
// (Polly's SynthesizeSpeech) needs one signing algorithm, and aws-sdk-go-v2
// brings a large dependency tree into a binary whose whole promise is that it
// is a single file you copy to a Pi. SigV4 has not changed since 2012 and is
// specified precisely enough to implement from the document.
//
// The algorithm is four steps — canonical request, string to sign, signing
// key, Authorization header — and sigv4_test.go checks each one separately.
// That granularity is the point: a wrong signature fails opaquely, as a 403
// with no indication of which step drifted, so the tests have to be able to
// say which one.
package awssig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	algorithm  = "AWS4-HMAC-SHA256"
	terminator = "aws4_request"

	// AWS's two date layouts. The long one goes in X-Amz-Date and the string
	// to sign; the short one in the credential scope.
	timeFormat = "20060102T150405Z"
	dateFormat = "20060102"

	// SHA-256 of the empty string, which is the payload hash for every GET.
	emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// unsignedHeaders are omitted from the signature because they are not stable
// between signing and sending. Go's transport fills in User-Agent after we
// sign, and a proxy may add its own Expect — signing either produces a
// mismatch that looks exactly like a bad secret key. Matches the set botocore
// excludes, for the same reason.
var unsignedHeaders = map[string]bool{
	"authorization":   true,
	"content-length":  true, // set by the transport, not present in Header
	"expect":          true,
	"user-agent":      true,
	"x-amzn-trace-id": true,
}

// Credentials for one request. SessionToken is set only for temporary
// credentials (STS, instance roles); when present it is signed, which is what
// binds the token to the request.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// A Signer signs requests for one service in one region.
type Signer struct {
	Region  string
	Service string
}

// Sign adds X-Amz-Date and Authorization to req.
//
// The payload is passed separately rather than read from req.Body because
// signing needs its hash and reading would consume it. Callers hold the body
// in memory already — a Polly request is a few hundred bytes of JSON — so
// this avoids the rewind dance for no benefit.
//
// t is explicit rather than time.Now() so the tests can pin it: a signature is
// only reproducible against a fixed clock.
func (s Signer) Sign(req *http.Request, payload []byte, c Credentials, t time.Time) error {
	if s.Region == "" || s.Service == "" {
		return errors.New("awssig: signer needs a region and a service")
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return errors.New("awssig: missing credentials")
	}

	t = t.UTC()
	amzDate, dateStamp := t.Format(timeFormat), t.Format(dateFormat)

	// Set before the headers are canonicalised — these are signed.
	req.Header.Set("X-Amz-Date", amzDate)
	if c.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.SessionToken)
	}

	scope := strings.Join([]string{dateStamp, s.Region, s.Service, terminator}, "/")
	canonical, signedHeaders := CanonicalRequest(req, payload)
	sts := StringToSign(canonical, scope, amzDate)
	sig := hex.EncodeToString(hmacSHA256(SigningKey(c.SecretAccessKey, dateStamp, s.Region, s.Service), []byte(sts)))

	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, c.AccessKeyID, scope, signedHeaders, sig))
	return nil
}

// CanonicalRequest builds step one and returns it alongside the signed-header
// list, which the Authorization header repeats verbatim. Exported so the tests
// can compare this step alone against AWS's published vectors — when a
// signature is wrong this is almost always where.
func CanonicalRequest(req *http.Request, payload []byte) (canonical, signedHeaders string) {
	headers, signed := canonicalHeaders(req)

	hash := emptyPayloadHash
	if len(payload) > 0 {
		hash = hexSHA256(payload)
	}

	return strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery(req.URL),
		headers,
		signed,
		hash,
	}, "\n"), signed
}

// StringToSign is step two: the algorithm, the timestamp, the credential
// scope, and a hash of the canonical request.
func StringToSign(canonical, scope, amzDate string) string {
	return strings.Join([]string{algorithm, amzDate, scope, hexSHA256([]byte(canonical))}, "\n")
}

// SigningKey is step three: four chained HMACs, each keyed by the last. The
// chain is what scopes a signature to one day, one region, and one service, so
// a leaked signature cannot be replayed against anything else.
func SigningKey(secret, dateStamp, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte(service))
	return hmacSHA256(k, []byte(terminator))
}

// canonicalURI is the path, percent-encoded again. The path in a URL is
// already encoded once, and SigV4 wants it encoded twice for every service
// except S3 — so a literal space arrives as %20 and is signed as %2520.
func canonicalURI(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	return uriEncode(p, true)
}

// canonicalQuery sorts parameters by name, then by value for repeats, and
// encodes both halves. Sorting is by encoded byte order, which is why it
// happens after encoding rather than before.
func canonicalQuery(u *url.URL) string {
	q := u.Query()
	pairs := make([]string, 0, len(q))
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, uriEncode(k, false)+"="+uriEncode(v, false))
		}
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

// canonicalHeaders lowercases names, collapses runs of spaces in values, and
// sorts. Host comes from req.Host rather than req.Header because Go keeps it
// out of the header map, and an unsigned Host is the most common way to get a
// 403 out of a signer that otherwise looks right.
func canonicalHeaders(req *http.Request) (canonical, signed string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	values := map[string]string{"host": host}
	for name, vs := range req.Header {
		lower := strings.ToLower(name)
		if unsignedHeaders[lower] {
			continue
		}
		trimmed := make([]string, len(vs))
		for i, v := range vs {
			trimmed[i] = collapseSpaces(v)
		}
		values[lower] = strings.Join(trimmed, ",")
	}

	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(':')
		b.WriteString(values[n])
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(names, ";")
}

// uriEncode percent-encodes per RFC 3986, which is what SigV4 means by
// "URI-encode" and is not what Go's url package does: QueryEscape writes a
// space as "+", and PathEscape leaves sub-delimiters like "=" and "&" alone.
// Both produce a canonical request AWS will not agree with.
func uriEncode(s string, keepSlash bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// collapseSpaces trims a header value and reduces internal runs of spaces to
// one, per the spec. Values inside double quotes are meant to be left alone;
// no AWS API this signs sends one, and getting it wrong loudly beats getting
// it subtly wrong, so a quoted value is left exactly as given.
func collapseSpaces(v string) string {
	if strings.Contains(v, `"`) {
		return strings.TrimSpace(v)
	}
	return strings.Join(strings.Fields(v), " ")
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
