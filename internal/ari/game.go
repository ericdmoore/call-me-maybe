package ari

import (
	"context"
	"net/http"
	"net/url"
)

// Answer accepts an opt-in local game channel.
func (c *Client) Answer(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/channels/"+url.PathEscape(id)+"/answer", nil, nil)
}

// MuteInput controls only audio sent from a sideline handset to its game bridge.
func (c *Client) MuteInput(ctx context.Context, id string, mute bool) error {
	method := http.MethodDelete
	if mute {
		method = http.MethodPost
	}
	return c.do(ctx, method, "/channels/"+url.PathEscape(id)+"/mute", url.Values{"direction": {"in"}}, nil)
}

// PlayBridge gives the room one shared playout and one completion event.
func (c *Client) PlayBridge(ctx context.Context, id, media, playback string) error {
	return c.do(ctx, http.MethodPost, "/bridges/"+url.PathEscape(id)+"/play/"+url.PathEscape(playback), url.Values{"media": {media}}, nil)
}
