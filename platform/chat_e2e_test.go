package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	"golang.org/x/net/websocket"
	_ "modernc.org/sqlite"
)

func chatApp(dbPath string) string {
	return fmt.Sprintf(`
name "chat"
environment "test"

resource "keys" {
  kind "auth.api_key"
  config {
    keys [
      { value "ada-secret-key-0123456789" principal_id "ada" }
      { value "bob-secret-key-0123456789" principal_id "bob" }
      { value "eve-secret-key-0123456789" principal_id "eve" }
    ]
  }
}
resource "db" {
  kind "database.sql"
  config { driver "sqlite" dsn "file:%s?_pragma=busy_timeout(5000)" max_open_connections 1 }
}
resource "live" {
  kind "realtime.hub"
  config { authenticator "keys" }
}
resource "chat" {
  kind "chat.store"
  config { database "db" hub "live" }
}

intent "dm.open" {
  response "r"
  node "dm" { uses "chat.dm" resource "chat" kind effect requires [input] provides [r] config { user_fact "input.user" } }
}
intent "msg.send" {
  response "r"
  node "m" { uses "chat.send" resource "chat" kind effect requires [input] provides [r] config { channel_fact "input.channel" body_fact "input.body" } }
}
intent "msg.history" {
  response "r"
  node "h" { uses "chat.history" resource "chat" kind read requires [input] provides [r] config { channel_fact "input.channel" } }
}
intent "msg.read" {
  response "r"
  node "h" { uses "chat.mark_read" resource "chat" kind effect requires [input] provides [r] config { channel_fact "input.channel" } }
}
intent "chat.list" {
  response "r"
  node "l" { uses "chat.channels" resource "chat" kind read provides [r] }
}

route "dm.open"     { method POST path "/dm"      intent "dm.open"     auth "keys" }
route "msg.send"    { method POST path "/send"    intent "msg.send"    auth "keys" }
route "msg.history" { method POST path "/history" intent "msg.history" auth "keys" }
route "msg.read"    { method POST path "/read"    intent "msg.read"    auth "keys" }
route "chat.list"   { method GET  path "/channels" intent "chat.list"  auth "keys" }
`, filepath.ToSlash(dbPath))
}

func TestChatEndToEnd(t *testing.T) {
	p, err := Compile(context.Background(), []byte(chatApp(filepath.Join(t.TempDir(), "c.db"))), t.TempDir(), DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	app := fh.NewFast()
	if err := p.Mount(app); err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() { _ = app.Serve(ln) }()
	t.Cleanup(func() { _ = app.ShutdownWithTimeout(time.Second); _ = ln.Close() })
	addr := ln.Addr().String()

	call := func(user, method, path, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", user+"-secret-key-0123456789")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	recvType := func(ws *websocket.Conn, typ string) map[string]any {
		t.Helper()
		for {
			_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
			var m map[string]any
			if err := websocket.JSON.Receive(ws, &m); err != nil {
				t.Fatalf("waiting for %s: %v", typ, err)
			}
			if m["type"] == typ {
				return m
			}
		}
	}
	dial := func(user string) *websocket.Conn {
		cfg, _ := websocket.NewConfig("ws://"+addr+"/realtime/ws", "http://"+addr)
		cfg.Header.Set("X-API-Key", user+"-secret-key-0123456789")
		ws, err := websocket.DialConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ws.Close() })
		recvType(ws, "ready")
		return ws
	}

	status, dm := call("ada", "POST", "/dm", `{"user":"bob"}`)
	if status != 200 {
		t.Fatalf("dm: %d %v", status, dm)
	}
	channel := dm["id"].(string)

	bob, eve := dial("bob"), dial("eve")
	_ = websocket.JSON.Send(eve, map[string]any{"op": "subscribe", "topic": "chat:" + channel})
	if m := recvType(eve, "reply"); m["code"] != "forbidden" {
		t.Fatalf("non-member subscribed to a DM: %v", m)
	}
	_ = websocket.JSON.Send(bob, map[string]any{"op": "subscribe", "topic": "chat:" + channel})
	if m := recvType(bob, "reply"); m["ok"] != true {
		t.Fatalf("member could not subscribe: %v", m)
	}
	// Clients cannot post straight to a chat topic and impersonate a sender.
	_ = websocket.JSON.Send(bob, map[string]any{"op": "publish", "topic": "chat:" + channel, "event": "message"})
	if m := recvType(bob, "reply"); m["code"] != "forbidden" {
		t.Fatalf("direct publish to chat topic: %v", m)
	}

	if status, body := call("ada", "POST", "/send", fmt.Sprintf(`{"channel":%q,"body":"hello bob"}`, channel)); status != 200 {
		t.Fatalf("send: %d %v", status, body)
	}
	pushed := recvType(bob, "message")
	if pushed["event"] != "message" || pushed["from"] != "ada" || !strings.Contains(fmt.Sprint(pushed["data"]), "hello bob") {
		t.Fatalf("pushed: %v", pushed)
	}

	if status, _ := call("eve", "POST", "/send", fmt.Sprintf(`{"channel":%q,"body":"let me in"}`, channel)); status != 403 {
		t.Fatalf("non-member send: %d", status)
	}
	if status, _ := call("eve", "POST", "/history", fmt.Sprintf(`{"channel":%q}`, channel)); status != 403 {
		t.Fatalf("non-member history: %d", status)
	}

	_, list := call("bob", "GET", "/channels", "")
	chs := list["channels"].([]any)
	if len(chs) != 1 || chs[0].(map[string]any)["unread"].(float64) != 1 {
		t.Fatalf("bob channels: %v", list)
	}
	if status, _ := call("bob", "POST", "/read", fmt.Sprintf(`{"channel":%q}`, channel)); status != 200 {
		t.Fatalf("read: %d", status)
	}
	_, list = call("bob", "GET", "/channels", "")
	if list["channels"].([]any)[0].(map[string]any)["unread"].(float64) != 0 {
		t.Fatalf("unread after read: %v", list)
	}
	_, hist := call("bob", "POST", "/history", fmt.Sprintf(`{"channel":%q}`, channel))
	if msgs := hist["messages"].([]any); len(msgs) != 1 || msgs[0].(map[string]any)["body"] != "hello bob" {
		t.Fatalf("history: %v", hist)
	}
}
