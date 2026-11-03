package usewire

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

const consent = "6f1c2a4e-8b3d-4c5e-9f60-000000000001"
const source = "6f1c2a4e-8b3d-4c5e-9f60-000000000002"

var now = time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)

func caps(enabled bool) Capabilities {
	return Capabilities{SchemaVersion: Version, CollectionEnabled: &enabled, DayTimeZone: "UTC", Metrics: []string{Metric}, MaxAgeDays: MaxDays, MaxBodyBytes: MaxBodyBytes}
}
func batch() Batch {
	return Batch{SchemaVersion: Version, ConsentEpoch: consent, SourceEpoch: source, Reports: []Report{{Day: "2026-11-03", Revision: 1, Counts: Counts{RecordedAudioProgress: 1}}}}
}
func encoded(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestConsentCannotComeFromEnrollmentCapabilitiesOrPayment(t *testing.T) {
	permission := Permission{Enabled: true, ConsentEpoch: consent, SourceEpoch: source, CopyVersion: CopyVersion}
	if !CanSend(permission, caps(true), source) {
		t.Fatal("explicit current grant refused")
	}
	for _, p := range []Permission{{}, {Enabled: true}, {Enabled: false, ConsentEpoch: consent, SourceEpoch: source, CopyVersion: CopyVersion}, {Enabled: true, ConsentEpoch: "bad", SourceEpoch: source, CopyVersion: CopyVersion}, {Enabled: true, ConsentEpoch: consent, SourceEpoch: "bad", CopyVersion: CopyVersion}, {Enabled: true, ConsentEpoch: consent, SourceEpoch: consent, CopyVersion: CopyVersion}, {Enabled: true, ConsentEpoch: consent, SourceEpoch: source, CopyVersion: "unreviewed-scope"}} {
		if CanSend(p, caps(true), source) {
			t.Fatal("implicit grant accepted")
		}
	}
	if CanSend(permission, caps(false), source) || CanSend(permission, Capabilities{}, source) || CanSend(permission, caps(true), "new-owner") {
		t.Fatal("receiver/ownership change ignored")
	}
}
func TestCapabilitiesAreBoundedExactAndNotConsent(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		got, err := ReadCapabilities(encoded(t, caps(enabled)))
		if err != nil || got.CollectionEnabled == nil || *got.CollectionEnabled != enabled {
			t.Fatal(got, err)
		}
	}
	for _, change := range []func(*Capabilities){func(c *Capabilities) { c.SchemaVersion = 2 }, func(c *Capabilities) { c.CollectionEnabled = nil }, func(c *Capabilities) { c.DayTimeZone = "local" }, func(c *Capabilities) { c.Metrics = nil }, func(c *Capabilities) { c.Metrics = []string{"private-feature"} }, func(c *Capabilities) { c.MaxAgeDays = 30 }, func(c *Capabilities) { c.MaxBodyBytes = 4096 }} {
		c := caps(true)
		change(&c)
		if _, err := ReadCapabilities(encoded(t, c)); !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
	}
	if _, err := ReadCapabilities([]byte(`{"schemaVersion":1,"email":"private@example.com"}`)); !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
}
func TestBatchCurrentAndLateUTCReportsAndOrdering(t *testing.T) {
	b := batch()
	b.Reports = append(b.Reports, Report{Day: "2026-10-28", Revision: 3, Counts: Counts{RecordedAudioProgress: 1000}})
	if _, err := ReadBatch(encoded(t, b), now); err != nil {
		t.Fatal(err)
	}
	b.Reports[0], b.Reports[1] = b.Reports[1], b.Reports[0]
	if _, err := ReadBatch(encoded(t, b), now.In(time.FixedZone("elsewhere", -6*3600))); err != nil {
		t.Fatal(err)
	}
}
func TestBatchRejectsAmbiguousPrivateOldAndFutureReports(t *testing.T) {
	for _, change := range []func(*Batch){func(b *Batch) { b.ConsentEpoch = "bad" }, func(b *Batch) { b.SourceEpoch = "bad" }, func(b *Batch) { b.SourceEpoch = b.ConsentEpoch }, func(b *Batch) { b.Reports = nil }, func(b *Batch) { b.Reports = append(b.Reports, b.Reports[0]) }, func(b *Batch) { b.Reports = make([]Report, 8) }, func(b *Batch) { b.Reports[0].Day = "2026-02-30" }, func(b *Batch) { b.Reports[0].Day = "2026-11-04" }, func(b *Batch) { b.Reports[0].Day = "2026-10-27" }, func(b *Batch) { b.Reports[0].Revision = 0 }, func(b *Batch) { b.Reports[0].Revision = 2147483648 }, func(b *Batch) { b.Reports[0].Counts.RecordedAudioProgress = 0 }, func(b *Batch) { b.Reports[0].Counts.RecordedAudioProgress = 1001 }} {
		b := batch()
		change(&b)
		if _, err := ReadBatch(encoded(t, b), now); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	b := batch()
	b.SchemaVersion = 2
	if _, err := ReadBatch(encoded(t, b), now); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	raw := encoded(t, batch())
	for _, private := range []string{`,"phoneNumber":"private"`, `,"contact":"private"`, `,"bookTitle":"private"`, `,"handset":"private"`, `,"transcript":"private"`, `,"account":"private"`} {
		body := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(private+"}")...)
		if _, err := ReadBatch(body, now); !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "private") {
			t.Fatal(err)
		}
	}
	for _, body := range [][]byte{nil, bytes.Repeat([]byte(" "), 2049), []byte("not JSON"), []byte("["), []byte(`{"reports":}`), []byte(`{"schemaVersion":1,"schemaVersion":2}`), []byte(`{"reports":[{"day":"x","day":"y"}]}`), append(raw, []byte(" {}")...), []byte(strings.Repeat("[", 10) + "0" + strings.Repeat("]", 10)), []byte(`{"schemaVersion":1,"reports":[1]}`)} {
		if _, err := ReadBatch(body, now); !errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed body: %v", err)
		}
	}
}

// The published schema must retain the same closed limits as the decoder. The
// runtime remains the authority for UTC windows, duplicate keys and epochs.
func TestPublishedSchemaMatchesClosedContract(t *testing.T) {
	data, err := os.ReadFile("schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Defs map[string]struct {
			AdditionalProperties bool                       `json:"additionalProperties"`
			Required             []string                   `json:"required"`
			Properties           map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	for name, count := range map[string]int{"capabilities": 6, "batch": 4} {
		d, ok := s.Defs[name]
		if !ok || d.AdditionalProperties || len(d.Required) != count || len(d.Properties) != count {
			t.Fatalf("open/incomplete %s definition", name)
		}
	}
	var version struct {
		Const int `json:"const"`
	}
	if err := json.Unmarshal(s.Defs["batch"].Properties["schemaVersion"], &version); err != nil || version.Const != Version {
		t.Fatal("schema version drift")
	}
	var limit struct {
		Const int `json:"const"`
	}
	if err := json.Unmarshal(s.Defs["capabilities"].Properties["maxBodyBytes"], &limit); err != nil || limit.Const != MaxBodyBytes {
		t.Fatal("byte limit drift")
	}
	var age struct {
		Const int `json:"const"`
	}
	if err := json.Unmarshal(s.Defs["capabilities"].Properties["maxAgeDays"], &age); err != nil || age.Const != MaxDays {
		t.Fatal("age limit drift")
	}
}
