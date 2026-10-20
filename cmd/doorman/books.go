package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"callmemaybe/internal/books"
)

func runBooks(args []string) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(args) == 1 && args[0] == "menu" {
		a, err := books.Connect(os.Stdin, os.Stdout)
		if err == nil {
			err = books.Run(ctx, a)
		}
		if err != nil && !errors.Is(err, books.ErrHangup) {
			fmt.Fprintln(os.Stderr, "books: request failed; check prepared library, FFmpeg and private spool access")
			return 1
		}
		return 0
	}
	if len(args) == 0 || (args[0] != "prepare" && args[0] != "list") {
		fmt.Fprintln(os.Stderr, "usage: doorman books prepare --source catalog.json [--library directory] | books list [--library directory]")
		return 2
	}
	fs := flag.NewFlagSet("books "+args[0], flag.ContinueOnError)
	library := fs.String("library", books.DefaultLibrary, "prepared library directory")
	source := fs.String("source", "", "local recording catalog JSON (prepare only)")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
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
