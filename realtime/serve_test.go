package realtime

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	"golang.org/x/net/websocket"
)

// startServer runs an fh app with the hub mounted. Requests authenticate with
// "Authorization: Bearer <user>".
func startServer(t *testing.T, h *Hub) string {
	t.Helper()
	app := fh.NewFast()
	err := h.Mount(app, MountConfig{
		Heartbeat: time.Second,
		Identify: func(c fh.Ctx) (Subject, error) {
			tok := strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
			if tok == "" {
				tok = c.Query("token")
			}
			if tok == "" {
				return Subject{}, errors.New("no credentials")
			}
			return Subject{ID: tok}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Serve(ln) }()
	t.Cleanup(func() { _ = app.ShutdownWithTimeout(time.Second); _ = ln.Close() })
	return ln.Addr().String()
}

func dialWS(t *testing.T, addr, query, origin string) (*websocket.Conn, error) {
	t.Helper()
	cfg, err := websocket.NewConfig("ws://"+addr+"/realtime/ws?"+query, origin)
	if err != nil {
		t.Fatal(err)
	}
	return websocket.DialConfig(cfg)
}

func readJSON(t *testing.T, ws *websocket.Conn) map[string]any {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	var m map[string]any
	if err := websocket.JSON.Receive(ws, &m); err != nil {
		t.Fatalf("receive: %v", err)
	}
	return m
}

func TestWebSocketEndToEnd(t *testing.T) {
	h := newHub(t, Options{})
	addr := startServer(t, h)
	origin := "http://" + addr

	if _, err := dialWS(t, addr, "", origin); err == nil {
		t.Fatal("unauthenticated handshake succeeded")
	}
	if _, err := dialWS(t, addr, "token=ada", "http://evil.example"); err == nil {
		t.Fatal("cross-origin handshake succeeded")
	}

	ada, err := dialWS(t, addr, "token=ada&topics=room", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer ada.Close()
	bob, err := dialWS(t, addr, "token=bob&topics=room", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close()
	if m := readJSON(t, ada); m["type"] != "ready" || m["user"] != "ada" {
		t.Fatalf("ready: %v", m)
	}
	readJSON(t, bob) // ready

	if err := websocket.Message.Send(ada, `{"id":"7","op":"publish","topic":"room","event":"chat","data":{"text":"hello"}}`); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for i := 0; i < 2; i++ { // ada's own message and the reply, in either order
		m := readJSON(t, ada)
		got[m["type"].(string)] = true
	}
	if !got["message"] || !got["reply"] {
		t.Fatalf("ada saw %v", got)
	}
	m := readJSON(t, bob)
	if m["type"] != "message" || m["from"] != "ada" || m["event"] != "chat" {
		t.Fatalf("bob: %v", m)
	}

	// A frame larger than the cap closes the connection instead of buffering.
	_ = websocket.Message.Send(bob, strings.Repeat("x", 70<<10))
	_ = bob.SetReadDeadline(time.Now().Add(3 * time.Second))
	var sink string
	for {
		if err := websocket.Message.Receive(bob, &sink); err != nil {
			break
		}
	}
}

func TestSSEEndToEnd(t *testing.T) {
	h := newHub(t, Options{})
	addr := startServer(t, h)

	req, _ := http.NewRequest("GET", "http://"+addr+"/realtime/events?topics=feed", nil)
	req.Header.Set("Authorization", "Bearer ada")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type %q", ct)
	}
	events := make(chan [2]string, 8)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var ev, id string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				events <- [2]string{ev, strings.TrimPrefix(line, "data: ")}
				ev, id = "", ""
				_ = id
			}
		}
	}()
	wait := func() [2]string {
		select {
		case e := <-events:
			return e
		case <-time.After(3 * time.Second):
			t.Fatal("no SSE event")
		}
		return [2]string{}
	}
	ready := wait()
	if ready[0] != "ready" {
		t.Fatalf("first event %v", ready)
	}
	var r struct{ Client string }
	_ = json.Unmarshal([]byte(ready[1]), &r)

	// Subscribe and publish through the companion endpoint.
	post := func(user, client, body string) int {
		rq, _ := http.NewRequest("POST", "http://"+addr+"/realtime/send?client="+client, strings.NewReader(body))
		rq.Header.Set("Content-Type", "application/json")
		rq.Header.Set("Authorization", "Bearer "+user)
		rs, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		rs.Body.Close()
		return rs.StatusCode
	}
	if code := post("mallory", r.Client, `{"op":"publish","topic":"feed"}`); code != 404 {
		t.Fatalf("another user driving the stream: %d", code)
	}
	if code := post("ada", r.Client, `{"op":"publish","topic":"feed","event":"tick","data":{"n":1}}`); code != 200 {
		t.Fatalf("send: %d", code)
	}
	e := wait()
	if e[0] != "message" || !strings.Contains(e[1], `"event":"tick"`) {
		t.Fatalf("event %v", e)
	}
}
