package backup

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"callmemaybe/internal/awssig"
)

// fakeS3 is enough of the API to prove the four verbs and the signature:
// path-style keys under one bucket, ListObjectsV2 with a prefix.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	auth    []string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Amz-Content-Sha256")+"|"+r.Header.Get("X-Amz-Date"))
	if !strings.HasPrefix(r.URL.Path, "/house/") {
		http.Error(w, "wrong bucket", 404)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/house/")
	switch {
	case r.Method == http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.objects[key] = b
	case r.Method == http.MethodGet && key == "":
		prefix := r.URL.Query().Get("prefix")
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		fmt.Fprint(w, `<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated>`)
		for _, k := range keys {
			fmt.Fprintf(w, "<Contents><Key>%s</Key></Contents>", k)
		}
		fmt.Fprint(w, "</ListBucketResult>")
	case r.Method == http.MethodGet:
		b, ok := f.objects[key]
		if !ok {
			http.Error(w, "no such key", 404)
			return
		}
		w.Write(b)
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(204)
	}
}

func TestS3DestinationSpeaksSignedPathStyleS3(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := S3Dest{Endpoint: srv.URL, Bucket: "house", Region: "auto", Prefix: "jepsen/",
		Creds: awssig.Credentials{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret"},
		Now:   func() time.Time { return time.Date(2026, 10, 4, 5, 10, 0, 0, time.UTC) }}
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 5, 10, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		res := Deliver(ctx, []Destination{d}, Name("jepsen", now.AddDate(0, 0, -i)), []byte("bundle"), 2, 0, now)
		if res[0].Err != nil {
			t.Fatal(res[0].Err)
		}
	}
	names, err := d.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || strings.HasPrefix(names[0], "jepsen/") {
		t.Errorf("after pruning to two dailies: %v", names)
	}
	rc, err := d.Get(ctx, names[0])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "bundle" {
		t.Errorf("get = %q", b)
	}
	for _, a := range f.auth {
		if !strings.HasPrefix(a, "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/20261004/auto/s3/aws4_request") || !strings.Contains(a, "|20261004T051000Z") {
			t.Errorf("request not signed as expected: %s", a)
		}
		if strings.Contains(a, "||") {
			t.Errorf("payload hash header missing: %s", a)
		}
	}
	if err := d.Delete(ctx, "never-there.age"); err != nil {
		t.Errorf("deleting a missing key is not an error: %v", err)
	}
	bad := S3Dest{Endpoint: srv.URL, Bucket: "other", Creds: d.Creds}
	if err := bad.Put(ctx, "x.age", strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("a refused request must surface its status: %v", err)
	}
}
