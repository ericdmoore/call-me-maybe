package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sync/atomic"
	"syscall"
	"time"

	"callmemaybe/internal/account"
	"callmemaybe/internal/accountbook"
	"callmemaybe/internal/activation"
	"callmemaybe/internal/bell"
	"callmemaybe/internal/books"
	"callmemaybe/internal/config"
	"callmemaybe/internal/contentinventory"
	"callmemaybe/internal/mediadelivery"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/syncmachine"
	"callmemaybe/internal/syncwire"
	"callmemaybe/internal/webhookconfig"

	"github.com/gorilla/websocket"
)

func runSync(args []string) int {
	if len(args) > 0 && args[0] == "media" {
		return runSyncMedia(args[1:])
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: doorman sync run|once|snapshot|inventory [--state directory] [--content directory]")
		return 2
	}
	mode := args[0]
	if mode != "run" && mode != "once" && mode != "snapshot" && mode != "inventory" {
		return 2
	}
	fs := flag.NewFlagSet("sync "+mode, flag.ContinueOnError)
	state := fs.String("state", "/var/lib/doorman", "private state directory")
	p := fs.String("policy", "", "policy path")
	h := fs.String("handsets", "", "handset inventory path")
	tr := fs.String("trunks", "", "provider inventory path")
	contacts := fs.String("contacts", "", "contact source inventory path")
	phonebooks := fs.String("phonebooks", "", "directory containing own and shared handset books (default $PHONEBOOK_DIR or state/phonebook)")
	content := fs.String("content", "", "library directory to report (default state/content/library)")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	if *content == "" {
		*content = filepath.Join(*state, "content", "library")
	}
	if mode == "inventory" {
		if json.NewEncoder(os.Stdout).Encode(contentinventory.Scan(*content)) != nil {
			return 1
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := account.New(*state)
	status, err := syncAccount(ctx, client)
	if ctx.Err() != nil {
		return 0
	}
	if err == nil && status.Removed {
		return syncRemoved()
	}
	if err != nil || status.NeedsLogin || status.Box == "" {
		fmt.Fprintln(os.Stderr, "sync: sign in with doorman login")
		return 1
	}
	if *phonebooks == "" {
		*phonebooks = os.Getenv("PHONEBOOK_DIR")
		if *phonebooks == "" {
			*phonebooks = filepath.Join(*state, "phonebook")
		}
	}
	paths := syncmachine.Paths{Policy: policyPathArg(*p), Handsets: handsetsPathArg(*h), Trunks: trunksPathArg(*tr), Contacts: contactsPathArg(*contacts), CountryCode: defaultCountryCode(), Phonebooks: *phonebooks}
	paths.Webhook = filepath.Join(filepath.Dir(paths.Policy), "webhook.toml")
	dir := filepath.Join(*state, "sync")
	m, err := syncmachine.Open(dir, status.Box, paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer m.Close()
	stats := &syncmachine.Stats{}
	inventory := &contentinventory.Scanner{Path: *content}
	m.OpenWebhook = client.OpenWebhook
	m.SampleStatus = func() syncwire.Status {
		sample := stats.Sample(*state, version)
		caps := []string{webhookconfig.Capability}
		sample.Capabilities = &caps
		sample.ContentInventory = inventory.Sample()
		return sample
	}
	m.Applied = activationApplied(dir)
	m.BoxName = config.BoxName(os.Getenv, os.Hostname)
	if mode == "snapshot" {
		s, err := m.Observe()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if json.NewEncoder(os.Stdout).Encode(s) != nil {
			return 1
		}
		return 0
	}
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); m.Watch(ctx, nil) }()
	defer func() { stop(); <-watchDone }()
	// The exchange only reads the last finished scan. Shutdown deliberately
	// does not wait for this goroutine: a scan stuck on a hung mount must not
	// hold the process open, and it exits with the process either way. A
	// single exchange has no later turn to report a background result in.
	if mode == "once" {
		inventory.Refresh()
	} else {
		go inventory.Run(ctx)
	}
	var follow *applyFollow
	exchange := func(ctx context.Context) error {
		bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		err := m.Exchange(bounded, syncTransport(client.Sync))
		if follow != nil {
			follow.kick()
		}
		return err
	}
	if mode == "once" {
		return syncLoop(ctx, exchange, nil, true)
	}
	wake := newSyncWake()
	follow = newApplyFollow(m.AwaitingApply, wake.ring)
	followDone := make(chan struct{})
	go func() { defer close(followDone); follow.run(ctx) }()
	defer func() { stop(); <-followDone }()
	enabled, slow, issue := config.SyncBellSettings(os.Getenv)
	if issue != "" {
		fmt.Fprintln(os.Stderr, "sync: "+issue+"; using the default")
	}
	if enabled && slices.Contains(status.Capabilities, "sync") {
		wake.slow = slow
		b := &bell.Bell{
			Dial: func(ctx context.Context) (*websocket.Conn, error) {
				conn, err := client.DialBell(ctx)
				return conn, bellError(err)
			},
			Credential: func(ctx context.Context) (string, error) {
				tag, err := client.SyncCredentialTag(ctx)
				return tag, bellError(err)
			},
			Ring: wake.ring, Healthy: wake.setBell, Log: func(line string) { fmt.Fprintln(os.Stderr, line) }}
		bellCtx, stopBell := context.WithCancel(ctx)
		bellDone := make(chan struct{})
		go func() { defer close(bellDone); _ = b.Run(bellCtx) }()
		defer func() { stopBell(); <-bellDone }()
	}
	return syncLoop(ctx, exchange, wake, false)
}

// activationApplied proves a revision is running: the daemon's activation
// socket reports, for every line, the digest of exactly these files, and the
// hash of the lobby pass book it admits from.
func activationApplied(dir string) func(syncmachine.Paths, map[string][]byte) bool {
	return func(paths syncmachine.Paths, files map[string][]byte) bool {
		current, err := activation.Read(dir)
		if err != nil {
			return false
		}
		lines, _ := policy.DiscoverLines(paths.Policy)
		for _, line := range lines {
			data, ok := files[line.Path]
			if !ok || current[line.Name] != policy.SourceDigest(data, files[paths.Handsets]) {
				return false
			}
		}
		// The lobby pass book is running when the daemon admits from exactly
		// the bytes on disk ("" for none on either side).
		if paths.Phonebooks != "" {
			if current[passActivationKey] != passBookHash(filepath.Join(paths.Phonebooks, accountbook.Dir), files) {
				return false
			}
		}
		if paths.Webhook != "" {
			hash, ok := current[webhookconfig.ActivationKey]
			if !ok || hash != webhookconfig.Hash(files[paths.Webhook]) {
				return false
			}
		}
		return true
	}
}

// bellError reads the account's answers in the bell's terms. The bell
// package never sees the account client (nothing under internal/ may), only
// what each answer means for it: stop, wait for a new credential, or back off.
func bellError(err error) error {
	var refused *account.BellRefused
	var service *account.ServiceError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, account.ErrRemoved):
		return fmt.Errorf("%w: %w", bell.ErrGone, err)
	case errors.As(err, &refused):
		return &bell.Refused{Reason: fmt.Sprintf("%d %s", refused.Status, refused.Code)}
	case errors.Is(err, account.ErrLogin):
		return &bell.Refused{Reason: "sign in with doorman login"}
	case errors.As(err, &service) && service.RetryAfter > 0:
		return &bell.Later{Err: err, After: service.RetryAfter}
	}
	return err
}

// syncWake is how the bell reaches the poll loop: a ring asks for a pass now,
// and the bell's health picks the interval between passes. Without a bell it
// is never touched and the loop polls exactly as it always has.
type syncWake struct {
	rings   chan struct{}
	changed chan struct{}
	up      atomic.Bool
	slow    time.Duration
}

func newSyncWake() *syncWake {
	return &syncWake{rings: make(chan struct{}, 1), changed: make(chan struct{}, 1)}
}

// ring never blocks. One buffered ring is the whole merge: rings during a
// pass leave exactly one more pass to run after it, however many arrived.
func (w *syncWake) ring() {
	select {
	case w.rings <- struct{}{}:
	default:
	}
}

func (w *syncWake) setBell(up bool) {
	w.up.Store(up)
	select {
	case w.changed <- struct{}{}:
	default:
	}
}

// interval is the healthy cadence: the safety net while the bell is up,
// the ordinary five seconds otherwise.
func (w *syncWake) interval() time.Duration {
	if w != nil && w.slow > 0 && w.up.Load() {
		return w.slow
	}
	return syncInterval
}

// syncLoop runs passes until ctx ends. Every wait is interruptible by a ring,
// and by the bell going down, which brings the next pass back to the
// ordinary cadence instead of leaving it at the safety net's.
func syncLoop(ctx context.Context, exchange func(context.Context) error, wake *syncWake, once bool) int {
	var lastError string
	var pace syncPace
	var rings, changed chan struct{}
	if wake != nil {
		rings, changed = wake.rings, wake.changed
	}
	for {
		at := time.Now()
		err := exchange(ctx)
		if ctx.Err() != nil {
			return 0
		}
		if errors.Is(err, account.ErrRemoved) {
			return syncRemoved()
		}
		if err != nil {
			// Package errors are categorical: never print requests, responses,
			// local values or a credential-bearing HTTP error.
			if err.Error() != lastError {
				fmt.Fprintln(os.Stderr, err)
				lastError = err.Error()
			}
		} else {
			lastError = ""
		}
		if once {
			if err != nil {
				return 1
			}
			return 0
		}
		timer := time.NewTimer(pace.next(err, time.Since(at), wake.interval()))
	wait:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return 0
			case <-timer.C:
				break wait
			case <-rings:
				timer.Stop()
				break wait
			case <-changed:
				// Only a healthy pass waits on the bell's interval; a
				// failing one keeps its backoff either way.
				if err == nil {
					timer.Stop()
					timer = time.NewTimer(max(time.Until(at.Add(wake.interval())), 0))
				}
			}
		}
	}
}

// syncTransport hands the machine the account's closed sync surface, and
// marks a removal as the end of the transport so the pass stops at once.
func syncTransport(send func(context.Context, account.SyncEndpoint, []byte) ([]byte, error)) syncmachine.Transport {
	return func(ctx context.Context, endpoint string, body []byte) ([]byte, error) {
		reply, err := send(ctx, account.SyncEndpoint(endpoint), body)
		var service *account.ServiceError
		switch {
		case errors.Is(err, account.ErrRemoved):
			err = fmt.Errorf("%w: %w", syncmachine.ErrTransportEnded, err)
		case errors.As(err, &service) && service.Status == http.StatusBadRequest:
			err = fmt.Errorf("%w: %w", syncmachine.ErrBadRequest, err)
		}
		return reply, err
	}
}

// syncExitRemoved is the exit status for an owner's deliberate sign-out. The
// unit lists it in SuccessExitStatus, so systemd neither restarts the process
// nor counts it against the start limit, and the unit does not read as failed.
// It is LSB's "program is not configured", which is what the box now is.
const syncExitRemoved = 6

// syncRemovedMessage is the one line a removed box logs. It is categorical:
// no token, header, box or house identifier, nothing from the response.
const syncRemovedMessage = "sync: this box was removed from its account; hosted sync and cloud backups have stopped; calls keep working; run `doorman login` to enrol it again"

// syncRemoved stops for good rather than polling: the service revoked every
// token, so another pass would only ask the same question again.
func syncRemoved() int {
	fmt.Fprintln(os.Stderr, syncRemovedMessage)
	return syncExitRemoved
}

// Separate, explicit command: ordinary configuration sync cannot start a media
// conversion, and no response can opt a box into a different credential origin.
func runSyncMedia(args []string) int {
	if len(args) == 0 || (args[0] != "list" && args[0] != "once") {
		fmt.Fprintln(os.Stderr, "usage: doorman sync media list|once --source https://trusted-sandbox-origin [--state directory] [--library directory]")
		return 2
	}
	fs := flag.NewFlagSet("sync media "+args[0], flag.ContinueOnError)
	origin := fs.String("source", "", "operator-approved sandbox HTTPS origin (receives the sync credential)")
	state := fs.String("state", "/var/lib/doorman", "private account state directory")
	library := fs.String("library", books.DefaultLibrary, "prepared local playback library")
	menuBundle := fs.String("menu-bundle", "", "prepared menu bundle matching the next catalog")
	audiobookSKU := fs.String("audiobook-sku", "", "explicitly opt into a full recorded audiobook SKU and disk-backed installation")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	source, err := account.New(*state).SandboxMedia(*origin)
	if *audiobookSKU != "" {
		source, err = account.New(*state).SandboxAudiobook(*origin, *audiobookSKU)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sync media: an explicit trusted HTTPS source is required")
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if args[0] == "list" {
		offers, err := source.List(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if json.NewEncoder(os.Stdout).Encode(offers) != nil {
			return 1
		}
		return 0
	}
	var installErr error
	if *audiobookSKU != "" {
		err = mediadelivery.ReceiveAudiobook(ctx, source, filepath.Join(*state, "media-audiobook"), func(ctx context.Context, file, hash string) error {
			installErr = books.ImportAudiobook(ctx, file, hash, *library, *menuBundle)
			return installErr
		})
	} else {
		err = mediadelivery.Receive(ctx, source, filepath.Join(*state, "media-preview"), func(ctx context.Context, data []byte, hash string) error {
			installErr = books.ImportPreviewWithMenus(ctx, data, hash, *library, *menuBundle)
			return installErr
		})
	}
	if err != nil {
		if errors.Is(installErr, books.ErrMenuRefresh) {
			fmt.Fprintln(os.Stderr, books.ErrMenuRefresh)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		return 1
	}
	fmt.Fprintln(os.Stdout, "Sandbox media pass complete; installation receipts reflect local playback readiness, not a handset listening test.")
	return 0
}
