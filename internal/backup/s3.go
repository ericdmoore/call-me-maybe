package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"callmemaybe/internal/awssig"
)

// S3Dest is any S3-compatible store — AWS S3, Cloudflare R2, Backblaze B2,
// MinIO — through four requests signed with the SigV4 signer the tree
// already has for Polly. Path-style addressing (`endpoint/bucket/key`) is
// what every one of them accepts, so there is no per-vendor switch.
type S3Dest struct {
	Endpoint string // https://<account>.r2.cloudflarestorage.com, https://s3.us-east-1.amazonaws.com, …
	Bucket   string
	Region   string // "auto" for R2, "us-west-004" for B2, a real one for AWS
	Prefix   string // optional key prefix, e.g. "jepsen/"
	Creds    awssig.Credentials
	Client   *http.Client
	Now      func() time.Time
}

func (s S3Dest) Name() string { return "s3" }

func (s S3Dest) do(ctx context.Context, method, key string, query url.Values, payload []byte) (*http.Response, error) {
	base, err := url.Parse(strings.TrimRight(s.Endpoint, "/"))
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("BACKUP_S3_ENDPOINT: not a URL")
	}
	u := *base
	u.Path = strings.TrimRight(base.Path, "/") + "/" + s.Bucket + "/"
	if key != "" {
		u.Path += s.Prefix + key
	}
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	// S3 insists on the payload hash as a header, signed.
	sum := sha256.Sum256(payload)
	req.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(sum[:]))
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = int64(len(payload))
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	region := s.Region
	if region == "" {
		region = "auto"
	}
	if err := (awssig.Signer{Region: region, Service: "s3"}).Sign(req, payload, s.Creds, now()); err != nil {
		return nil, err
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3: %w", err)
	}
	if resp.StatusCode/100 != 2 && !(method == http.MethodDelete && resp.StatusCode == 404) {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, fmt.Errorf("s3: %s %s answered %d: %s", method, key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

func (s S3Dest) Put(ctx context.Context, name string, r io.Reader) error {
	payload, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodPut, name, nil, payload)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (s S3Dest) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	resp, err := s.do(ctx, http.MethodGet, name, nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (s S3Dest) List(ctx context.Context) ([]string, error) {
	var names []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {s.Prefix + "callmemaybe-"}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := s.do(ctx, http.MethodGet, "", q, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Contents []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("s3: list: %w", err)
		}
		for _, c := range page.Contents {
			names = append(names, strings.TrimPrefix(c.Key, s.Prefix))
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return names, nil
		}
		token = page.NextContinuationToken
	}
}

func (s S3Dest) Delete(ctx context.Context, name string) error {
	resp, err := s.do(ctx, http.MethodDelete, name, nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

var errNoS3 = errors.New("s3: not configured")
