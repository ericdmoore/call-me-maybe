package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"callmemaybe/internal/books"
)

func runBooks(args []string) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// menu is a household handset's *2665; listen is an outside caller a
	// public line released into [cmm-listen] (#155). Both are AGIs Asterisk
	// runs, and neither has the daemon's credentials.
	if len(args) == 1 && (args[0] == "menu" || args[0] == "listen") {
		a, err := books.Connect(os.Stdin, os.Stdout)
		if err == nil && args[0] == "menu" {
			err = books.Run(ctx, a)
		} else if err == nil {
			err = books.RunListen(ctx, a)
		}
		if err != nil && !errors.Is(err, books.ErrHangup) {
			fmt.Fprintln(os.Stderr, "books: request failed; check prepared library, FFmpeg and private spool access")
			return 1
		}
		return 0
	}
	if len(args) == 0 || (args[0] != "prepare" && args[0] != "list" && args[0] != "prompts" && args[0] != "install-preview") {
		fmt.Fprintln(os.Stderr, "usage: doorman books prepare --source catalog.json [--library directory] | books list [--library directory] | books prompts --library directory [--voice Samantha] [--shelf id,id ...] | books install-preview --archive local.cmmpack [--library directory]")
		return 2
	}
	fs := flag.NewFlagSet("books "+args[0], flag.ContinueOnError)
	library := fs.String("library", books.DefaultLibrary, "prepared library directory")
	voice := fs.String("voice", "Samantha", "installed macOS say voice (prompts only)")
	menuBundle := fs.String("menu-bundle", "", "prepared menu bundle matching the next preview catalog")
	archive := fs.String("archive", "", "local preview .cmmpack (install-preview only)")
	source := fs.String("source", "", "local recording catalog JSON (prepare only)")
	// A public line's [[shelves]] titles, so its shelf pages are rendered in
	// the same voice (#155). Repeat it once per shelf.
	var shelves [][]string
	fs.Func("shelf", "a shelf's title ids, comma-separated, in its order (prompts only; repeatable)", func(v string) error {
		shelves = append(shelves, strings.Split(v, ","))
		return nil
	})
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	if args[0] == "install-preview" {
		if *archive == "" {
			fmt.Fprintln(os.Stderr, "books install-preview requires --archive")
			return 2
		}
		f, err := os.Open(*archive)
		if err != nil {
			fmt.Fprintln(os.Stderr, "books: cannot read preview archive")
			return 1
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			fmt.Fprintln(os.Stderr, "books: preview archive exceeds limit or cannot be read")
			return 1
		}
		sum := sha256.Sum256(data)
		if err := books.ImportPreviewWithMenus(ctx, data, fmt.Sprintf("%x", sum), *library, *menuBundle); err != nil {
			fmt.Fprintln(os.Stderr, "books: preview installation failed:", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "Preview installed in the local Books library.")
		return 0
	}
	if args[0] == "prompts" {
		if err := books.PrepareMenus(ctx, *library, *voice, shelves...); err != nil {
			fmt.Fprintln(os.Stderr, "books prompts:", err)
			return 1
		}
		return 0
	}
	if args[0] == "prepare" {
		if *source == "" {
			fmt.Fprintln(os.Stderr, "books prepare requires --source")
			return 2
		}
		if err := books.Prepare(ctx, *source, *library); err != nil {
			fmt.Fprintln(os.Stderr, "books prepare:", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "Books catalog prepared; reload the dialplan after installing the new binary.")
		return 0
	}
	c, err := books.Load(*library)
	if err != nil {
		fmt.Fprintln(os.Stderr, "books: cannot read prepared catalog")
		return 1
	}
	if json.NewEncoder(os.Stdout).Encode(c) != nil {
		return 1
	}
	return 0
}
