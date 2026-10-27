package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"callmemaybe/internal/ari"
	"callmemaybe/internal/phonegame"
	"callmemaybe/internal/policy"
)

// This preview is a separate Stasis application, never the inbound lobby.
func runGames(args []string) int {
	if len(args) == 0 || (args[0] != "check" && args[0] != "serve") {
		fmt.Fprintln(os.Stderr, "usage: doorman games check|serve --pack DIR [--handsets FILE --rooms id,id]")
		return 2
	}
	fs := flag.NewFlagSet("games", flag.ContinueOnError)
	dir := fs.String("pack", "", "validated local game bundle")
	inventory := fs.String("handsets", "handsets.toml", "handset inventory")
	rooms := fs.String("rooms", "", "explicit comma-separated internal handset ids (maximum eight)")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *dir == "" {
		return 2
	}
	pack, err := phonegame.Load(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "games: invalid or incomplete local pack")
		return 1
	}
	if args[0] == "check" {
		fmt.Println("game pack valid:", pack.ID)
		return 0
	}
	hs, _, err := policy.LoadHandsets(*inventory)
	if err != nil {
		fmt.Fprintln(os.Stderr, "games: cannot load handset inventory")
		return 1
	}
	allowed := strings.Split(*rooms, ",")
	selected := []phonegame.Handset{}
	for _, id := range allowed {
		found := false
		for _, h := range hs {
			if h.ID == id {
				selected = append(selected, phonegame.Handset{ID: h.ID, Endpoint: h.Endpoint, Number: h.Number})
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintln(os.Stderr, "games: every preview room must name a configured handset")
			return 1
		}
	}
	base := os.Getenv("ARI_BASE_URL")
	if base == "" {
		base = "http://127.0.0.1:8088"
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || os.Getenv("ARI_USERNAME") == "" || os.Getenv("ARI_PASSWORD") == "" {
		fmt.Fprintln(os.Stderr, "games: loopback ARI credentials required")
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	disconnected := make(chan struct{}, 1)
	client := ari.New(ari.Options{BaseURL: base, Username: os.Getenv("ARI_USERNAME"), Password: os.Getenv("ARI_PASSWORD"), App: "doorman-games-preview", Log: slog.Default(), OnConnection: func(up bool) {
		if !up {
			select {
			case disconnected <- struct{}{}:
			default:
			}
		}
	}})
	defer client.Close()
	manager, err := phonegame.New(ctx, client, pack, selected)
	if err != nil {
		fmt.Fprintln(os.Stderr, "games: invalid preview room selection")
		return 1
	}
	manager.Observe = func(stage string) { slog.Warn("game preview media failure", "stage", stage) }
	defer manager.Close()
	if _, err = client.Ping(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "games: ARI unavailable")
		return 1
	}
	client.Connect(manager.Event)
	// A lost media event stream aborts owned calls. Reconnection never resumes a stale keypad window.
	select {
	case <-ctx.Done():
	case <-disconnected:
		cancel()
		return 1
	}
	return 0
}
