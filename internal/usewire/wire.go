// Package usewire specifies a staged, optional recorded-audio activity protocol.
// Nothing imports it on the call path and it sends, records and enables nothing.
package usewire

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"
)

const (
	Version          = 1
	Metric           = "recordedAudioProgress"
	MaxBodyBytes     = 2048
	MaxDays          = 7
	MaxCount         = 1000
	CopyVersion      = "help-improve-v1"
	ConsentCopy      = "Help make Doorman better. Share a small daily summary of recorded-audio use to help us decide what to improve. We don't include phone numbers, contacts, audio or transcripts. Sharing is optional, off by default, and you can turn it off anytime."
	CapabilitiesPath = "/v1/sync/usage/capabilities"
	ReportsPath      = "/v1/sync/usage"
)

var (
	ErrInvalid     = errors.New("use reporting: invalid bounded contract")
	ErrUnsupported = errors.New("use reporting: unsupported receiver contract")
	uuid           = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// Capabilities are receiver support, never household consent. A disabled receiver
// may describe the supported shape while refusing every optional report.
type Capabilities struct {
	SchemaVersion     int      `json:"schemaVersion"`
	CollectionEnabled *bool    `json:"collectionEnabled"`
	DayTimeZone       string   `json:"dayTimeZone"`
	Metrics           []string `json:"metrics"`
	MaxAgeDays        int      `json:"maxAgeDays"`
	MaxBodyBytes      int      `json:"maxBodyBytes"`
}
type Counts struct {
	RecordedAudioProgress uint16 `json:"recordedAudioProgress"`
}

// Report is a cumulative snapshot for one UTC day. Never send a zero-use report:
// absence of optional reporting cannot establish zero use.
type Report struct {
	Day      string `json:"day"`
	Revision uint32 `json:"revision"`
	Counts   Counts `json:"counts"`
}
type Batch struct {
	SchemaVersion int      `json:"schemaVersion"`
	ConsentEpoch  string   `json:"consentEpoch"`
	SourceEpoch   string   `json:"sourceEpoch"`
	Reports       []Report `json:"reports"`
}

// Permission is local owner-controlled state, not a field a receiver may enable.
// Enrollment and payment do not construct this grant. Persistence/control and
// source integration are deliberately not implemented by this contract package.
type Permission struct {
	Enabled      bool
	ConsentEpoch string
	SourceEpoch  string
	CopyVersion  string
}

func supported(c Capabilities) bool {
	return c.SchemaVersion == Version && c.CollectionEnabled != nil && c.DayTimeZone == "UTC" && len(c.Metrics) == 1 && c.Metrics[0] == Metric && c.MaxAgeDays == MaxDays && c.MaxBodyBytes == MaxBodyBytes
}
func CanSend(p Permission, c Capabilities, currentSourceEpoch string) bool {
	return p.Enabled && uuid.MatchString(p.ConsentEpoch) && uuid.MatchString(p.SourceEpoch) && p.SourceEpoch != p.ConsentEpoch && p.SourceEpoch == currentSourceEpoch && p.CopyVersion == CopyVersion && supported(c) && *c.CollectionEnabled
}
func ReadCapabilities(data []byte) (Capabilities, error) {
	var c Capabilities
	if err := decode(data, &c); err != nil {
		return Capabilities{}, err
	}
	if !supported(c) {
		return Capabilities{}, ErrUnsupported
	}
	return c, nil
}
func ReadBatch(data []byte, now time.Time) (Batch, error) {
	var b Batch
	if err := decode(data, &b); err != nil {
		return Batch{}, err
	}
	if b.SchemaVersion != Version {
		return Batch{}, ErrUnsupported
	}
	if !uuid.MatchString(b.ConsentEpoch) || !uuid.MatchString(b.SourceEpoch) || b.ConsentEpoch == b.SourceEpoch || len(b.Reports) < 1 || len(b.Reports) > MaxDays {
		return Batch{}, ErrInvalid
	}
	utc := now.UTC()
	today := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	oldest := today.AddDate(0, 0, -MaxDays+1)
	seen := map[string]bool{}
	for _, r := range b.Reports {
		day, err := time.Parse("2006-01-02", r.Day)
		if err != nil || day.Before(oldest) || day.After(today) || seen[r.Day] || r.Revision < 1 || r.Revision > 2147483647 || r.Counts.RecordedAudioProgress < 1 || r.Counts.RecordedAudioProgress > MaxCount {
			return Batch{}, ErrInvalid
		}
		seen[r.Day] = true
	}
	return b, nil
}

// Reject duplicate JSON keys as well as unknown keys. Ambiguous retries cannot
// acquire different meaning in Go, a hosted validator or a future consumer.
func decode(data []byte, target any) error {
	if len(data) == 0 || len(data) > MaxBodyBytes {
		return ErrInvalid
	}
	tokens := json.NewDecoder(bytes.NewReader(data))
	if err := value(tokens, 0); err != nil {
		return ErrInvalid
	}
	if _, err := tokens.Token(); err != io.EOF {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return ErrInvalid
	}
	return nil
}
func value(d *json.Decoder, depth int) error {
	if depth > 8 {
		return ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return ErrInvalid
	}
	delim, container := t.(json.Delim)
	if !container {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return ErrInvalid
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return ErrInvalid
			}
			keys[name] = true
			if err := value(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := value(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrInvalid
	}
	if _, err := d.Token(); err != nil {
		return ErrInvalid
	}
	return nil
}
