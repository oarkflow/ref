package platform

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	"golang.org/x/net/websocket"
)

const realtimeApp = `
name "rt"
environment "test"

resource "keys" {
  kind "auth.api_key"
  config {
    keys [
      { value "ada-secret-key-0123456789" principal_id "ada" roles ["member"] }
      { value "bob-secret-key-0123456789" principal_id "bob" roles ["member"] }
      { value "eve-secret-key-0123456789" principal_id "eve" roles ["guest"] }
    ]
  }
}

resource "live" {
  kind "realtime.hub"
  config {
    authenticator "keys"
    callable ["chat.echo"]
    rules {
      "room:" { subscribe ["member"] publish ["member"] }
    }
  }
}

intent "chat.say" {
  response "response"
  node "send" {
    uses "realtime.publish"
    resource "live"
    kind effect
    requires [input]
    provides [sent]
    config { topic_fact "input.room" event "chat" data_fact "input.text" }
  }
  node "who" {
    uses "realtime.presence"
    resource "live"
    kind read
    requires [input, sent]
    provides [present]
    config { topic_fact "input.room" }
  }
  node "response" {
    uses "collect"
    requires [sent, present]
    provides [response]
  }
}

intent "chat.echo" {
  response "response"
  node "response" {
    uses "collect"
    requires [input]
    provides [response]
    config { unwrap true }
  }
}

route "chat.say" {
  method POST
  path "/say"
  intent "chat.say"
  auth "keys"
}
`

func TestRealtimeHubEndToEnd(t *testing.T) {
	p, err := Compile(context.Background(), []byte(realtimeApp), t.TempDir(), DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	app := fh.NewFast()
	if err := p.Mount(app); err != nil {
		t.Fatalf("mount: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Serve(ln) }()
	t.Cleanup(func() { _ = app.ShutdownWithTimeout(time.Second); _ = ln.Close() })
	addr := ln.Addr().String()

	dial := func(key, query string) (*websocket.Conn, error) {
		cfg, _ := websocket.NewConfig("ws://"+addr+"/realtime/ws?"+query, "http://"+addr)
		if key != "" {
			cfg.Header.Set("X-API-Key", key)
		}
		return websocket.DialConfig(cfg)
	}
	recv := func(ws *websocket.Conn) map[string]any {
		t.Helper()
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		var m map[string]any
		if err := websocket.JSON.Receive(ws, &m); err != nil {
			t.Fatalf("receive: %v", err)
		}
		return m
	}

	recvType := func(ws *websocket.Conn, typ string) map[string]any {
		t.Helper()
		for {
			if m := recv(ws); m["type"] == typ {
				return m
			}
		}
	}

	if _, err := dial("", ""); err == nil {
		t.Fatal("connected without credentials")
	}
	if _, err := dial("wrong-key-wrong-key-wrong", ""); err == nil {
		t.Fatal("connected with a bad key")
	}

	bob, err := dial("bob-secret-key-0123456789", "")
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close()
	if m := recv(bob); m["type"] != "ready" || m["user"] != "bob" {
		t.Fatalf("ready: %v", m)
	}

	// The rule: only members may enter room:*.
	eve, err := dial("eve-secret-key-0123456789", "topics=room:1")
	if err != nil {
		t.Fatal(err)
	}
	defer eve.Close()
	recv(eve) // ready
	if m := recv(eve); m["code"] != "forbidden" {
		t.Fatalf("guest in a member room: %v", m)
	}

	_ = websocket.JSON.Send(bob, map[string]any{"id": "1", "op": "subscribe", "topic": "room:1", "track": true})
	if m := recvType(bob, "reply"); m["ok"] != true {
		t.Fatalf("subscribe: %v", m)
	}

	// An intent publishes; the subscriber receives it.
	req, _ := http.NewRequest("POST", "http://"+addr+"/say", strings.NewReader(`{"room":"room:1","text":"hello from an intent"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "ada-secret-key-0123456789")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("say: %d %v", resp.StatusCode, body)
	}
	m := recvType(bob, "message")
	if m["type"] != "message" || m["event"] != "chat" || m["from"] != "ada" || string(mustMarshal(m["data"])) != `"hello from an intent"` {
		t.Fatalf("bob got %v", m)
	}

	// The intent publishes with the caller's own rights: a guest cannot use it to
	// post into a members-only room, even though the node itself is trusted.
	req, _ = http.NewRequest("POST", "http://"+addr+"/say", strings.NewReader(`{"room":"room:1","text":"let me in"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "eve-secret-key-0123456789")
	if resp, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("guest published through an intent: %d", resp.StatusCode)
	}

	// A connection may call only the intents the hub lists.
	_ = websocket.JSON.Send(bob, map[string]any{"id": "2", "op": "call", "name": "chat.echo", "data": map[string]any{"x": 1}})
	m = recvType(bob, "reply")
	if m["ok"] != true {
		t.Fatalf("call: %v", m)
	}
	_ = websocket.JSON.Send(bob, map[string]any{"id": "3", "op": "call", "name": "chat.say", "data": map[string]any{}})
	if m = recvType(bob, "reply"); m["ok"] != false {
		t.Fatalf("an unlisted intent was callable: %v", m)
	}
}

func mustMarshal(v any) []byte { b, _ := json.Marshal(v); return b }
