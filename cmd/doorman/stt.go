package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"callmemaybe/internal/setup"
	"callmemaybe/internal/stt"
)

// runSTT is `doorman stt`: where the *88 names are transcribed.
//
//	doorman stt                      say what is configured and whether it answers
//	doorman stt <url>                transcribe there (an OpenAI-compatible endpoint)
//	doorman stt none                 nowhere: numbers keep their number for a name
//
// Only `doorman phonebook` reads STT_ENDPOINT, on its next run, so a change
// here needs no restart. "here" — a whisper-server this box runs itself —
// waits on a build our release publishes (issue #32).
func runSTT(args []string) int {
	envPath := "./.env"
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-env" && i+1 < len(args) {
			envPath = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	env := secretLookup(envPath)
	switch {
	case len(rest) == 0 || rest[0] == "status":
		fmt.Println(describeSTT(env, 4*time.Second))
		return 0
	case rest[0] == "none":
		if err := setup.RotateSecrets(envPath, []string{"STT_ENDPOINT"}, func() (string, error) { return "", nil }); err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return 1
		}
		fmt.Println("✓ STT_ENDPOINT cleared: a number added with *88 keeps its number for a name.")
		return 0
	case rest[0] == "here":
		fmt.Fprintln(os.Stderr, "✗ a local whisper-server is not shipped yet (whisper.cpp publishes no binaries; ours will — issue #32).")
		fmt.Fprintln(os.Stderr, "  Meanwhile: `doorman stt http://<host>:<port>/v1/audio/transcriptions` — tools/speechd on a Mac, or whisper-server anywhere.")
		return 2
	default:
		u, err := url.Parse(rest[0])
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			fmt.Fprintf(os.Stderr, "✗ %q is not an http(s) URL; want e.g. http://alpaca:9001/v1/audio/transcriptions\n", rest[0])
			return 2
		}
		if u.Path == "" || u.Path == "/" {
			u.Path = "/v1/audio/transcriptions"
		}
		if err := setup.RotateSecrets(envPath, []string{"STT_ENDPOINT"}, func() (string, error) { return u.String(), nil }); err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return 1
		}
		fmt.Printf("✓ STT_ENDPOINT = %s\n", u.String())
		fmt.Println(describeSTT(secretLookup(envPath), 4*time.Second))
		// The voicemail transcripts run as the asterisk user, which cannot
		// read .env; they keep their own copy.
		fmt.Printf("  Voicemail transcripts read their own copy: STT_ENDPOINT=%s in /etc/asterisk/cmm-mail.env (RUNBOOK, \"The house mailbox\").\n", u.String())
		return 0
	}
}

// describeSTT is the one line `check` and `stt` print: none, or the host and
// whether anything answers there. The path is configuration, not a secret,
// but only the host is shown — the line is for a glance, not a copy.
func describeSTT(env func(string) (string, bool), timeout time.Duration) string {
	raw, ok := env("STT_ENDPOINT")
	raw = strings.TrimSpace(raw)
	if !ok || raw == "" {
		return "STT: none — *88 numbers keep their number for a name (`doorman stt <url>` to transcribe)"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "STT: " + raw + " is not a URL — fix STT_ENDPOINT"
	}
	return fmt.Sprintf("STT: remote %s (%s)", u.Host, probeSTT(u, timeout))
}

// probeSTT asks the service for anything at all. A whisper-server answers
// its root; tools/speechd answers /health; a wrong host answers nothing.
func probeSTT(u *url.URL, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for _, path := range []string{"/health", "/"} {
		probe := *u
		probe.Path, probe.RawQuery = path, ""
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, probe.String(), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		return "answering"
	}
	return "not answering"
}

// whisperFromEnv is the STT client STT_ENDPOINT, STT_MODEL and
// STT_TIMEOUT_MS describe, or nil when no endpoint is set. Two readers with
// two files: `doorman phonebook` through .env, and `doorman voicemail
// notify` through /etc/asterisk/cmm-mail.env — the same names and meanings
// in both, so a copied line means the same thing.
func whisperFromEnv(env func(string) (string, bool)) *stt.Whisper {
	ep, ok := env("STT_ENDPOINT")
	if !ok || strings.TrimSpace(ep) == "" {
		return nil
	}
	w := &stt.Whisper{Endpoint: strings.TrimSpace(ep)}
	if m, ok := env("STT_MODEL"); ok {
		w.Model = strings.TrimSpace(m)
	}
	if ms, ok := env("STT_TIMEOUT_MS"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(ms)); err == nil && n > 0 {
			w.Timeout = time.Duration(n) * time.Millisecond
		}
	}
	return w
}
