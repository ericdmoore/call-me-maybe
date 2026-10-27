package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"callmemaybe/internal/voicemail"
)

// runVoicemail is `doorman voicemail`: the house's side of a message after
// Asterisk has saved it. Both halves run as the asterisk user and read
// /etc/asterisk/cmm-mail.env — never doorman's .env, which holds the ARI,
// SIP and account credentials this process must not have:
//
//	doorman voicemail notify <context> <mailbox> [new] [old] [urgent]
//	    externnotify's work (scripts/voicemail-notify starts it detached):
//	    the box's own address or MAIL_TO, a transcript through STT_ENDPOINT,
//	    a recording link per VOICEMAIL_LINK_BASE, one mail through MAIL_HOOK
//	doorman voicemail serve
//	    the links, on VOICEMAIL_LINK_LISTEN, to the home network and the
//	    tailnet only (doorman-voicemail.service)
func runVoicemail(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: doorman voicemail notify <context> <mailbox> [new] [old] [urgent] | doorman voicemail serve")
		return 2
	}
	switch args[0] {
	case "notify":
		return runVoicemailNotify(args[1:])
	case "serve":
		return runVoicemailServe(args[1:])
	}
	fmt.Fprintf(os.Stderr, "✗ doorman voicemail %s: want notify or serve\n", args[0])
	return 2
}

const (
	cmmMailEnvDefault  = "/etc/asterisk/cmm-mail.env"
	voicemailStateDir  = "/var/lib/asterisk/cmm-voicemail"
	voicemailLinkPort  = ":8095"
	defaultMailHookCmd = "/opt/call-me-maybe/scripts/mail-hook-bullmoose"
)

// cmmMailEnvPath is the settings file; CMM_MAIL_ENV moves it, as it moved
// the shell script's.
func cmmMailEnvPath() string {
	if p := os.Getenv("CMM_MAIL_ENV"); p != "" {
		return p
	}
	return cmmMailEnvDefault
}

// mailSettings reads the file the way the shell script did when it sourced
// it: a key the file sets wins, and one it does not falls back to the
// process environment.
func mailSettings(path string) (func(string) (string, bool), error) {
	file, err := voicemail.ReadEnvFile(path)
	return func(key string) (string, bool) {
		if v, ok := file[key]; ok && v != "" {
			return v, true
		}
		v, ok := os.LookupEnv(key)
		return v, ok && v != ""
	}, err
}

// voicemailLog is where notify's lines go: stderr, which the wrapper hands
// to syslog as cmm-voicemail. A test reads it instead.
var voicemailLog io.Writer = os.Stderr

// Asterisk context and mailbox names, as the wrapper checks them.
var vmName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func runVoicemailNotify(args []string) int {
	fs := flag.NewFlagSet("voicemail notify", flag.ContinueOnError)
	envPath := fs.String("env", cmmMailEnvPath(), "settings file (MAIL_TO, MAIL_HOOK, STT_*, VOICEMAIL_LINK_*)")
	spoolFlag := fs.String("spool", "", "voicemail spool (default $VOICEMAIL_SPOOL or /var/spool/asterisk/voicemail)")
	conf := fs.String("conf", "/etc/asterisk/voicemail.conf", "Asterisk's voicemail configuration, for each box's own address")
	state := fs.String("state", voicemailStateDir, "links and the list of messages already mailed")
	if fs.Parse(args) != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) < 2 || len(rest) > 5 || !vmName.MatchString(rest[0]) || !vmName.MatchString(rest[1]) {
		fmt.Fprintln(os.Stderr, "voicemail notify: want <context> <mailbox> [new] [old] [urgent]")
		return 2
	}
	vmContext, mailbox := rest[0], rest[1]
	logf := func(format string, a ...any) { fmt.Fprintf(voicemailLog, format+"\n", a...) }

	env, err := mailSettings(*envPath)
	if err != nil {
		logf("cannot read %s (%v); without it nothing is mailed", *envPath, err)
		return 0
	}
	spool := *spoolFlag
	if spool == "" {
		spool = "/var/spool/asterisk/voicemail"
		if v, ok := env("VOICEMAIL_SPOOL"); ok {
			spool = v
		}
	}
	hook := defaultMailHookCmd
	if v, ok := env("MAIL_HOOK"); ok {
		hook = strings.TrimSpace(v)
	}
	mailTo, _ := env("MAIL_TO")
	n := &voicemail.Notifier{
		Spool: spool, Conf: *conf, MailTo: mailTo, StateDir: *state, Logf: logf,
		Send: func(to, subject, body string) error {
			send, err := newMailer(func(k string) (string, bool) {
				switch k {
				case "MAIL_HOOK":
					return hook, true
				case "MAIL_TO":
					return to, true
				}
				return "", false
			})
			if err != nil {
				return err
			}
			return send(subject, body)
		},
	}
	if w := whisperFromEnv(env); w != nil {
		n.STT = w
	}
	if v, ok := env("VOICEMAIL_LINK_BASE"); ok {
		good, bad := voicemail.ParseLinkBases(v)
		for _, b := range bad {
			logf("VOICEMAIL_LINK_BASE: %q is not an http(s) root like http://jepsen.local:8095; left out", b)
		}
		n.LinkBases = good
	}
	n.LinkDays = linkDays(env, logf)
	// Generous: a transcription has its own timeout (STT_TIMEOUT_MS), and
	// this bounds a run whose hook or service hangs past every one of them.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := n.Run(ctx, vmContext, mailbox); err != nil {
		logf("%v", err)
	}
	return 0
}

func linkDays(env func(string) (string, bool), logf func(string, ...any)) int {
	v, ok := env("VOICEMAIL_LINK_DAYS")
	if !ok {
		return 14
	}
	d, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || d < 1 || d > 3650 {
		logf("VOICEMAIL_LINK_DAYS=%q is not a number of days (1–3650); using 14", v)
		return 14
	}
	return d
}

func runVoicemailServe(args []string) int {
	fs := flag.NewFlagSet("voicemail serve", flag.ContinueOnError)
	envPath := fs.String("env", cmmMailEnvPath(), "settings file (VOICEMAIL_LINK_BASE, VOICEMAIL_LINK_LISTEN, VOICEMAIL_LINK_DAYS)")
	state := fs.String("state", voicemailStateDir, "where notify made the links")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	env, err := mailSettings(*envPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ cannot read %s: %v\n", *envPath, err)
		return 1
	}
	if v, _ := env("VOICEMAIL_LINK_BASE"); strings.TrimSpace(v) == "" {
		fmt.Fprintf(os.Stderr, "VOICEMAIL_LINK_BASE is not set in %s: no link is ever made, so there is nothing to serve\n", *envPath)
		return 0
	}
	listen := voicemailLinkPort
	if v, ok := env("VOICEMAIL_LINK_LISTEN"); ok {
		listen = strings.TrimSpace(v)
	}
	days := linkDays(env, func(f string, a ...any) { log.Warn(fmt.Sprintf(f, a...)) })
	dir := voicemail.LinksDir(*state)
	prune := func() {
		if n, err := voicemail.Prune(dir, time.Duration(days)*24*time.Hour, time.Now()); err != nil {
			log.Warn("voicemail links: prune failed", "err", err)
		} else if n > 0 {
			log.Info("voicemail links: pruned expired links", "count", n)
		}
	}
	prune()

	srv := voicemail.NewHTTPServer(listen, &voicemail.Server{Dir: dir, Log: log})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				prune()
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	log.Info("voicemail links: listening", "addr", listen, "days", days)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("voicemail links: listener failed", "err", err)
		return 1
	}
	return 0
}

// voicemailLinksWanted is init services' rule for doorman-voicemail.service:
// wanted when VOICEMAIL_LINK_BASE is set in the settings file. Root reads
// the file (0640 root:asterisk); the service account usually cannot, and
// `doorman check` says so rather than guessing.
func voicemailLinksWanted(path string) (bool, string) {
	env, err := mailSettings(path)
	if err != nil {
		return false, "cannot read " + path + " as this user (root decides: sudo doorman init services)"
	}
	if v, _ := env("VOICEMAIL_LINK_BASE"); strings.TrimSpace(v) != "" {
		return true, "VOICEMAIL_LINK_BASE is set in " + path
	}
	return false, "VOICEMAIL_LINK_BASE is not set in " + path
}
