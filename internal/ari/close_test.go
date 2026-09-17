package ari

import (
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCloseJoinsDisconnectCallback(t *testing.T) {
	up := make(chan struct{})
	down := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer c.Close()
		_, _, _ = c.ReadMessage()
	}))
	defer server.Close()
	c := New(Options{BaseURL: server.URL, OnConnection: func(connected bool) {
		if connected {
			close(up)
		} else {
			close(down)
		}
	}})
	c.Connect(func(Event) {})
	select {
	case <-up:
	case <-time.After(time.Second):
		t.Fatal("connect timeout")
	}
	c.Close()
	select {
	case <-down:
	default:
		t.Fatal("Close returned before disconnected callback")
	}
	c.Close()
}
