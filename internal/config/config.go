// Package config parses doorman's runtime configuration from environment
// variables. Names and defaults match examples/.env.example exactly. In production,
// systemd's EnvironmentFile loads .env; in development, `make run` sources it.
//
// The voicemail keys in examples/.env.example (VOICEMAIL_*, STT_*, SMTP_*) are
// intentionally not read yet — the config shape is settled ahead of the
// feature so .env does not churn when it lands.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ARIBaseURL   string
	ARIUsername  string
	ARIPassword  string
	ARIApp       string
	ReconnectMin time.Duration
	ReconnectMax time.Duration
	// AllowRemoteARI is the explicit opt-out from the loopback requirement.
	AllowRemoteARI bool

	PolicyPath   string
	HandsetsPath string
	PolicyWatch  bool

	DefaultCountryCode string

	ExtensionLength   int
	FirstDigitTimeout time.Duration
	InterDigitTimeout time.Duration
	RingTimeout       time.Duration
	MaxPinAttempts    int

	RateLimitEnabled     bool
	RateLimitMaxFailures int
	RateLimitWindow      time.Duration

	// MaxConcurrentCalls caps simultaneous inbound sessions. 0 means no limit.
	MaxConcurrentCalls int

	PromptMediaPrefix string

	LogLevel       slog.Level
	LogFormat      string // "json" | "pretty"
	RedactCallerID bool

	// CallLogPath enables the per-call record log. Empty — the default —
	// means no call log at all: it holds full caller IDs, so it is something
	// an operator turns on deliberately rather than something that appears.
	CallLogPath string
	// CallLogMaxBytes caps the live file before it rotates. One previous
	// generation is kept.
	CallLogMaxBytes int64

	// WebhookURL enables the event webhook. Empty — the default — means no
	// webhook at all. Treat the value as a secret: a Home Assistant webhook
	// id is the whole credential, which is why doorman logs only its host.
	WebhookURL string
	// WebhookToken, when set, is sent as `Authorization: Bearer`.
	WebhookToken string
	// WebhookTimeout bounds one delivery attempt.
	WebhookTimeout time.Duration
	// WebhookRedactCallerID narrows the caller ID in the payload. On by
	// default, unlike the call log: the destination is another program on the
	// network rather than a 0600 file on the box.
	WebhookRedactCallerID bool
}

var truthy = regexp.MustCompile(`^(?i)(1|true|yes|on)$`)

// Load reads and validates the environment. All problems are reported at
// once — nobody enjoys fixing env vars one restart at a time.
func Load() (Config, error) {
	var issues []string
	need := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			issues = append(issues, fmt.Sprintf("  %s: required", key))
		}
		return v
	}
	str := func(key, fallback string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return fallback
	}
	boolean := func(key string, fallback bool) bool {
		v := os.Getenv(key)
		if v == "" {
			return fallback
		}
		return truthy.MatchString(v)
	}
	integer := func(key string, fallback int) int {
		v := os.Getenv(key)
		if v == "" {
			return fallback
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			issues = append(issues, fmt.Sprintf("  %s: must be a positive integer, got %q", key, v))
			return fallback
		}
		return n
	}
	ms := func(key string, fallback int) time.Duration {
		return time.Duration(integer(key, fallback)) * time.Millisecond
	}

	c := Config{
		ARIBaseURL:   str("ARI_BASE_URL", "http://127.0.0.1:8088"),
		ARIUsername:  need("ARI_USERNAME"),
		ARIPassword:  need("ARI_PASSWORD"),
		ARIApp:       str("ARI_APP", "doorman"),
		ReconnectMin: ms("ARI_RECONNECT_MIN_MS", 500),
		ReconnectMax: ms("ARI_RECONNECT_MAX_MS", 30_000),

		PolicyPath:   str("POLICY_PATH", "./policy.toml"),
		HandsetsPath: str("HANDSETS_PATH", "./handsets.toml"),
		PolicyWatch:  boolean("POLICY_WATCH", true),

		DefaultCountryCode: str("DEFAULT_COUNTRY_CODE", "1"),

		ExtensionLength:   integer("EXTENSION_LENGTH", 6),
		FirstDigitTimeout: ms("FIRST_DIGIT_TIMEOUT_MS", 10_000),
		InterDigitTimeout: ms("INTER_DIGIT_TIMEOUT_MS", 3_000),
		RingTimeout:       time.Duration(integer("RING_TIMEOUT_S", 30)) * time.Second,
		MaxPinAttempts:    integer("MAX_PIN_ATTEMPTS", 2),

		RateLimitEnabled:     boolean("RATELIMIT_ENABLED", true),
		RateLimitMaxFailures: integer("RATELIMIT_MAX_FAILURES", 3),
		RateLimitWindow:      ms("RATELIMIT_WINDOW_MS", 3_600_000),

		MaxConcurrentCalls: integer("MAX_CONCURRENT_CALLS", 5),

		PromptMediaPrefix: str("PROMPT_MEDIA_PREFIX", "call-me-maybe"),

		LogFormat:      str("LOG_FORMAT", "json"),
		RedactCallerID: boolean("LOG_REDACT_CALLER_ID", true),

		CallLogPath:     str("CALL_LOG_PATH", ""),
		CallLogMaxBytes: int64(integer("CALL_LOG_MAX_BYTES", 32<<20)),

		WebhookURL:            str("WEBHOOK_URL", ""),
		WebhookToken:          str("WEBHOOK_TOKEN", ""),
		WebhookTimeout:        ms("WEBHOOK_TIMEOUT_MS", 3_000),
		WebhookRedactCallerID: boolean("WEBHOOK_REDACT_CALLER_ID", true),
	}

	switch strings.ToLower(str("LOG_LEVEL", "info")) {
	case "trace", "debug":
		c.LogLevel = slog.LevelDebug
	case "info":
		c.LogLevel = slog.LevelInfo
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		issues = append(issues, "  LOG_LEVEL: must be one of trace|debug|info|warn|error")
	}

	// CLAUDE.md invariant 2: never widen the ARI bind past loopback. The
	// shipped asterisk/http.conf is correct, but nothing stopped doorman being
	// pointed at a LAN or WAN ARI — and ARI is effectively full call control
	// (originate, bridge, hang up) behind a password that lives in a plaintext
	// file. On the wire that is Basic Auth plus an api_key query parameter.
	//
	// So the binary now enforces what the docs assert. The escape hatch exists
	// because the invariant itself allows one case: "if doorman ever moves
	// off-box, it goes over the tailnet, never the LAN."
	c.AllowRemoteARI = boolean("ARI_ALLOW_REMOTE", false)
	if host, err := httpHost(c.ARIBaseURL); err != nil {
		issues = append(issues, fmt.Sprintf("  ARI_BASE_URL: %v", err))
	} else if !isLoopback(host) && !c.AllowRemoteARI {
		issues = append(issues, fmt.Sprintf(
			"  ARI_BASE_URL: host %q is not loopback.\n"+
				"      ARI is full call control behind a plaintext password; keep it on\n"+
				"      127.0.0.1. If you genuinely run doorman off-box, it must be over a\n"+
				"      tailnet and never the LAN — set ARI_ALLOW_REMOTE=true to confirm.",
			host))
	}
	if c.LogFormat != "json" && c.LogFormat != "pretty" {
		issues = append(issues, "  LOG_FORMAT: must be json or pretty")
	}
	// A typo here must fail at startup, where an operator is looking, rather
	// than as a warn line after the first call nobody heard announced. The
	// message is deliberately terse and never repeats the value: a Home
	// Assistant webhook id is the whole credential, and under systemd this
	// text goes to journald.
	if c.WebhookURL != "" {
		if _, err := httpHost(c.WebhookURL); err != nil {
			issues = append(issues, "  WEBHOOK_URL: must be an http or https URL with a host")
		}
	}

	if len(issues) > 0 {
		return Config{}, fmt.Errorf("invalid environment configuration:\n%s\n\nSee examples/.env.example", strings.Join(issues, "\n"))
	}
	return c, nil
}

// httpHost extracts the hostname from an http(s) URL, rejecting anything that
// is not one. Shared by ARI_BASE_URL and WEBHOOK_URL.
func httpHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("no host in %q", raw)
	}
	return host, nil
}

// isLoopback reports whether a host is the local machine. Names other than
// "localhost" are not resolved: a DNS lookup at startup would make the check
// depend on a resolver that could answer differently later.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
