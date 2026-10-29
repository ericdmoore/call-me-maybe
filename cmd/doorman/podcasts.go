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
		reported := false
		a, err := books.Connect(os.Stdin, os.Stdout)
		if err == nil {
			err = books.RunPodcastFeeds(ctx, a, func(handset, feed string) bool {
				c, e := podcastconfig.Load(podcasts.DefaultConfig)
				if e != nil && !reported {
					fmt.Fprintln(os.Stderr, "podcasts: subscriptions unreadable or invalid")
					reported = true
				}
				return e == nil && c.AllowsFeed(handset, feed)
			})
		}
		if err != nil && !errors.Is(err, books.ErrHangup) {
			fmt.Fprintln(os.Stderr, "podcasts: local playback unavailable")
			return 1
		}
		return 0
	}
	if len(args) == 0 || (args[0] != "refresh" && args[0] != "status" && args[0] != "check" && args[0] != "subscribe" && args[0] != "unsubscribe") {
		fmt.Fprintln(os.Stderr, "usage: doorman podcasts refresh|status|check|subscribe|unsubscribe [--config file] [--pool directory]")
		return 2
	}
	scheduled := false
	fs := flag.NewFlagSet("podcasts", flag.ContinueOnError)
	fs.BoolVar(&scheduled, "scheduled", false, "respect per-feed polling and retry intervals")
	handsets := fs.String("handsets", handsetsPathArg(""), "handset inventory (check)")
	handset := fs.String("handset", "", "handset ID (subscribe/unsubscribe)")
	feedURL := fs.String("feed-url", "", "public HTTPS feed (subscribe/unsubscribe)")
	title := fs.String("title", "", "show title (subscribe/unsubscribe)")
	config := fs.String("config", podcasts.DefaultConfig, "handset subscription JSON")
	root := fs.String("pool", podcasts.DefaultRoot, "shared podcast cache pool")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	if args[0] == "check" || args[0] == "subscribe" || args[0] == "unsubscribe" {
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
		if args[0] != "check" {
			canonical, e := podcastconfig.CanonicalURL(*feedURL)
			id, e2 := podcastconfig.IDForURL(canonical)
			if e != nil || e2 != nil || !known[*handset] {
				fmt.Fprintln(os.Stderr, "podcasts: invalid public feed or handset")
				return 1
			}
			name := *title
			if name == "" && id == podcastconfig.Feed {
				name = "Brains On!"
			}
			if args[0] == "unsubscribe" && name == "" {
				for _, f := range c.Feeds {
					if f.ID == id {
						name = f.Title
					}
				}
			}
			before, e := json.Marshal(c)
			if e != nil {
				return 1
			}
			next, e := podcastconfig.SelectFeed(before, *handset, podcastconfig.Source{ID: id, URL: canonical, Title: name}, args[0] == "subscribe")
			if e != nil || podcastconfig.Save(*config, next) != nil {
				fmt.Fprintln(os.Stderr, "podcasts: subscriptions unchanged; invalid update or write failed")
				return 1
			}
			fmt.Fprintln(os.Stdout, "Podcast subscription saved; audio preparation is separate.")
			return 0
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
		refresh := podcasts.Refresh
		if scheduled {
			refresh = podcasts.RefreshScheduled
		}
		if err := refresh(ctx, *config, *root); err != nil {
			fmt.Fprintln(os.Stderr, "podcasts: refresh failed; previous prepared episodes remain available:", err)
			return 1
		}
	}
	if json.NewEncoder(os.Stdout).Encode(podcasts.ReadStatus(*root)) != nil {
		return 1
	}
	return 0
}
