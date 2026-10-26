// Package webhookconfig owns the private webhook file and its URL rules.
// Neither paths nor parsing errors are safe to include in logs or snapshots.
package webhookconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

const Suffix = "/cc.dialdoorman/events"
const Capability = "webhook.set"
const ActivationKey = "webhook"

var ErrInvalid = errors.New("webhook: invalid configuration")
var origin = regexp.MustCompile(`(?i)^https?://[^/?#\\\s@]+/?$`)
var prefixChars = regexp.MustCompile(`(?i)^[a-z0-9/_.~!$&'()*+,;=:@%\-]*$`)

type Config struct {
	Domain     string `json:"domain" toml:"domain"`
	PathPrefix string `json:"pathPrefix" toml:"path_prefix"`
}

// Normalise permits an empty pair to explicitly disable managed delivery.
func Normalise(c Config) (Config, error) {
	if len(c.Domain) > 1024 || len(c.PathPrefix) > 1024 {
		return Config{}, ErrInvalid
	}
	c.Domain = strings.TrimSpace(c.Domain)
	c.PathPrefix = strings.Trim(strings.TrimSpace(c.PathPrefix), "/")
	if c.Domain == "" {
		if c.PathPrefix != "" {
			return Config{}, ErrInvalid
		}
		return Config{}, nil
	}
	u, err := url.Parse(c.Domain)
	if err != nil || !origin.MatchString(c.Domain) || u.Hostname() == "" || u.User != nil {
		return Config{}, ErrInvalid
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		u.Host = strings.TrimSuffix(u.Host, ":"+u.Port())
	}
	u.Path = ""
	c.Domain = u.String()
	if !prefixChars.MatchString(c.PathPrefix) || strings.Contains(c.PathPrefix, "://") {
		return Config{}, ErrInvalid
	}
	for _, part := range strings.Split(c.PathPrefix, "/") {
		decoded, err := url.PathUnescape(part)
		if err != nil || !utf8.ValidString(decoded) || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "/\\?#") || strings.ContainsFunc(decoded, unicode.IsControl) {
			return Config{}, ErrInvalid
		}
	}
	if c.PathPrefix != "" {
		c.PathPrefix = "/" + c.PathPrefix
	}
	return c, nil
}
func (c Config) URL() string {
	if c.Domain == "" {
		return ""
	}
	return c.Domain + c.PathPrefix + Suffix
}
func Parse(data []byte) (Config, error) {
	if len(data) > 8192 {
		return Config{}, ErrInvalid
	}
	var c Config
	md, err := toml.Decode(string(data), &c)
	if err != nil || len(md.Undecoded()) != 0 {
		return Config{}, ErrInvalid
	}
	return Normalise(c)
}
func Encode(c Config) []byte {
	var out bytes.Buffer
	_ = toml.NewEncoder(&out).Encode(c)
	return out.Bytes()
}

// NormaliseJSON is shared with the hosted validator's WASM reactor.
func NormaliseJSON(data []byte) []byte {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil {
		return nil
	}
	c, err := Normalise(c)
	if err != nil {
		return nil
	}
	out, _ := json.Marshal(c)
	return out
}
