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
	"callmemaybe/internal/podcastconfig"
	"callmemaybe/internal/podcasts"
	"callmemaybe/internal/policy"
)

func runPodcasts(args []string) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(args) == 1 && args[0] == "menu" {
		a, err := books.Connect(os.Stdin, os.Stdout)
		if err == nil {
			err = books.RunPodcasts(ctx, a, func(handset string) bool {
				c, e := podcastconfig.Load(podcasts.DefaultConfig)
				return e == nil && c.Allows(handset)
			})
		}
		if err != nil && !errors.Is(err, books.ErrHangup) {
			fmt.Fprintln(os.Stderr, "podcasts: local playback unavailable")
			return 1
		}
		return 0
	}
	if len(args) == 0 || (args[0] != "refresh" && args[0] != "status" && args[0] != "check") {
		fmt.Fprintln(os.Stderr, "usage: doorman podcasts refresh|status|check [--config file] [--pool directory]")
		return 2
	}
	fs := flag.NewFlagSet("podcasts", flag.ContinueOnError)
	handsets := fs.String("handsets", handsetsPathArg(""), "handset inventory (check)")
	config := fs.String("config", podcasts.DefaultConfig, "handset subscription JSON")
	root := fs.String("pool", podcasts.DefaultRoot, "shared podcast cache pool")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	if args[0] == "check" {
		c, err := podcastconfig.Load(*config)
		if err != nil {
			fmt.Fprintln(os.Stderr, "podcasts: invalid subscriptions")
			return 1
		}
		data, err := os.ReadFile(*handsets)
		if err != nil {
			fmt.Fprintln(os.Stderr, "podcasts: cannot read handsets")
			return 1
		}
		hs, _, err := policy.HandsetsFromTOML(data)
		if err != nil {
			return 1
		}
		known := map[string]bool{}
		for _, h := range hs {
			known[h.ID] = true
		}
		for _, sub := range c.Subscriptions {
			if !known[sub.Handset] {
				fmt.Fprintln(os.Stderr, "podcasts: unknown handset")
				return 1
			}
		}
		fmt.Fprintln(os.Stdout, "Podcast subscriptions valid.")
		return 0
	}
	if args[0] == "refresh" {
		if err := podcasts.Refresh(ctx, *config, *root); err != nil {
			fmt.Fprintln(os.Stderr, "podcasts: refresh failed; previous prepared episodes remain available:", err)
			return 1
		}
	}
	if json.NewEncoder(os.Stdout).Encode(podcasts.ReadStatus(*root)) != nil {
		return 1
	}
	return 0
}
