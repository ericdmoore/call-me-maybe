package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"callmemaybe/internal/account"
	"callmemaybe/internal/activation"
	"callmemaybe/internal/contentinventory"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/syncmachine"
	"callmemaybe/internal/syncwire"
)

func runSync(args []string) int {
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
	dir := filepath.Join(*state, "sync")
	m, err := syncmachine.Open(dir, status.Box, paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer m.Close()
	stats := &syncmachine.Stats{}
	inventory := &contentinventory.Scanner{Path: *content}
	m.SampleStatus = func() syncwire.Status {
		sample := stats.Sample(*state, version)
		sample.ContentInventory = inventory.Sample()
		return sample
	}
	m.Applied = func(paths syncmachine.Paths, files map[string][]byte) bool {
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
		return true
	}
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
	var lastError string
	var pace syncPace
	for {
		at := time.Now()
		bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = m.Exchange(bounded, syncTransport(client.Sync))
		cancel()
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
		if mode == "once" {
			if err != nil {
				return 1
			}
			return 0
		}
		timer := time.NewTimer(pace.next(err, time.Since(at)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0
		case <-timer.C:
		}
	}
}

// syncTransport hands the machine the account's closed sync surface, and
// marks a removal as the end of the transport so the pass stops at once.
func syncTransport(send func(context.Context, account.SyncEndpoint, []byte) ([]byte, error)) syncmachine.Transport {
	return func(ctx context.Context, endpoint string, body []byte) ([]byte, error) {
		reply, err := send(ctx, account.SyncEndpoint(endpoint), body)
		if errors.Is(err, account.ErrRemoved) {
			err = fmt.Errorf("%w: %w", syncmachine.ErrTransportEnded, err)
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
