package podcasts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type feedState struct {
	Title     string `json:"title"`
	NoStore   bool   `json:"noStore"`
	Body      string `json:"body"`
	ETag      string `json:"etag"`
	Modified  string `json:"modified"`
	NextCheck int64  `json:"nextCheck"`
	Failed    bool   `json:"failed"`
}

func readFeedState(path string) *feedState {
	f, err := os.Open(path)
	if err != nil {
		return &feedState{}
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 20<<20))
	var s feedState
	if err != nil || len(data) >= 20<<20 || json.Unmarshal(data, &s) != nil || len(s.Body) > 8<<20 || len(s.ETag) > 1024 || len(s.Modified) > 1024 {
		return &feedState{}
	}
	return &s
}
func saveFeedState(root, path string, s *feedState) error {
	var body bytes.Buffer
	e := json.NewEncoder(&body)
	e.SetEscapeHTML(false)
	if err := e.Encode(s); err != nil {
		return err
	}
	used, err := diskBytes(root)
	if err != nil || used+int64(body.Len()) > MaxCacheBytes {
		return errors.New("podcast feed cache needs space")
	}
	f, err := os.CreateTemp(root, ".refresh-feed-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(body.Bytes())
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func loadFeed(ctx context.Context, client *http.Client, sourceURL string, state *feedState, now time.Time, scheduled bool) (string, error) {
	if scheduled && now.Unix() < state.NextCheck {
		if state.Failed {
			return "", errors.New("podcast feed retry waiting")
		}
		if state.Body != "" {
			return state.Body, nil
		}
	}
	job, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(context.WithValue(job, feedRequestKey{}, true), "GET", sourceURL, nil)
	if err != nil {
		return "", errors.New("podcast feed request invalid")
	}
	req.Header.Set("User-Agent", "CallMeMaybe-Podcasts/1")
	if state.Body != "" {
		if state.ETag != "" {
			req.Header.Set("If-None-Match", state.ETag)
		}
		if state.Modified != "" {
			req.Header.Set("If-Modified-Since", state.Modified)
		}
	}
	res, err := client.Do(req)
	if err != nil {
		return "", errors.New("podcast feed fetch failed")
	}
	defer res.Body.Close()
	if res.StatusCode == 304 && state.Body != "" {
		return state.Body, nil
	}
	if res.StatusCode != 200 || res.ContentLength > 8<<20 {
		return "", errors.New("podcast feed response refused")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || len(body) == 0 || len(body) > 8<<20 {
		return "", errors.New("podcast feed exceeds limit")
	}
	state.ETag = res.Header.Get("ETag")
	state.Modified = res.Header.Get("Last-Modified")
	if len(state.ETag) > 1024 {
		state.ETag = ""
	}
	if len(state.Modified) > 1024 {
		state.Modified = ""
	}
	state.NoStore = strings.Contains(strings.ToLower(res.Header.Get("Cache-Control")), "no-store")
	if state.NoStore {
		state.ETag = ""
		state.Modified = ""
	}
	return string(body), nil
}
