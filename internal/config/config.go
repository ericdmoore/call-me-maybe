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

	PromptMediaPrefix string

	LogLevel       slog.Level
	LogFormat      string // "json" | "pretty"
	RedactCallerID bool
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

		PromptMediaPrefix: str("PROMPT_MEDIA_PREFIX", "call-me-maybe"),

		LogFormat:      str("LOG_FORMAT", "json"),
		RedactCallerID: boolean("LOG_REDACT_CALLER_ID", false),
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
	if c.LogFormat != "json" && c.LogFormat != "pretty" {
		issues = append(issues, "  LOG_FORMAT: must be json or pretty")
	}

	if len(issues) > 0 {
		return Config{}, fmt.Errorf("invalid environment configuration:\n%s\n\nSee examples/.env.example", strings.Join(issues, "\n"))
	}
	return c, nil
}
