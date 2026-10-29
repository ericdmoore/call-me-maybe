package main

import (
	"bytes"
	"callmemaybe/internal/podcastconfig"
	"os"
	"path/filepath"
	"testing"
)

func TestPodcastCLIConfigurationOnly(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "subscriptions.json")
	hs := filepath.Join(dir, "handsets.toml")
	if err := os.WriteFile(hs, []byte("[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	url := "https://podcasts.example.org/nature.xml"
	args := []string{"--config", config, "--handsets", hs, "--handset", "kitchen", "--feed-url", url}
	if code := runPodcasts(append([]string{"subscribe", "--title", "Nature"}, args...)); code != 0 {
		t.Fatal(code)
	}
	c, err := podcastconfig.Load(config)
	if err != nil || len(c.Feeds) != 1 || len(c.Subscriptions) != 1 {
		t.Fatal(c, err)
	}
	before, _ := os.ReadFile(config)
	if code := runPodcasts([]string{"subscribe", "--title", "Private", "--feed-url", "https://127.0.0.1/a", "--handset", "kitchen", "--config", config, "--handsets", hs}); code != 1 {
		t.Fatal(code)
	}
	after, _ := os.ReadFile(config)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected CLI edit changed subscriptions")
	}
	if code := runPodcasts([]string{"check", "--config", config, "--handsets", hs}); code != 0 {
		t.Fatal(code)
	}
	if code := runPodcasts(append([]string{"unsubscribe"}, args...)); code != 0 {
		t.Fatal(code)
	}
	c, err = podcastconfig.Load(config)
	if err != nil || c.Schema != 1 || len(c.Subscriptions) != 0 || len(c.Feeds) != 0 {
		t.Fatal(c, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "library")); !os.IsNotExist(err) {
		t.Fatal("CLI edit prepared audio")
	}
}
