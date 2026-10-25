package rtc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/contrib/rtc"
	"github.com/oarkflow/ref/platform"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"golang.org/x/net/websocket"
	_ "modernc.org/sqlite"
)

func app(dbPath string) string {
	return fmt.Sprintf(`
name "rtc"
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
resource "db" { kind "database.sql" config { driver "sqlite" dsn "file:%s?_pragma=busy_timeout(5000)" max_open_connections 1 } }
resource "live" { kind "realtime.hub" config { authenticator "keys" } }
resource "calls" {
  kind "rtc.calls"
  config {
    database "db" hub "live"
    stun_urls ["stun:stun.example.com:3478"]
    turn_urls ["turn:turn.example.com:3478"]
    turn_secret "turn-shared-secret-0123456789"
  }
}
resource "sfu" { kind "rtc.sfu" config { hub "live" calls "calls" } }

intent "call.start" {
  response "r"
  node "n" { uses "call.start" resource "calls" kind effect requires [input] provides [r]
             config { to_fact "input.to" kind_fact "input.kind" mode_fact "input.mode" } }
}
intent "call.answer" {
  response "r"
  node "n" { uses "call.answer" resource "calls" kind effect requires [input] provides [r] config { call_fact "input.call" } }
}
intent "call.signal" {
  response "r"
  node "n" { uses "call.signal" resource "calls" kind effect requires [input] provides [r]
             config { call_fact "input.call" to_fact "input.to" type_fact "input.type" data_fact "input.data" } }
}
intent "call.end" {
  response "r"
  node "n" { uses "call.end" resource "calls" kind effect requires [input] provides [r] config { call_fact "input.call" } }
}
intent "call.ice" {
  response "r"
  node "n" { uses "call.ice_servers" resource "calls" kind read provides [r] }
}
route "call.start"  { method POST path "/call/start"  intent "call.start"  auth "keys" }
route "call.answer" { method POST path "/call/answer" intent "call.answer" auth "keys" }
route "call.signal" { method POST path "/call/signal" intent "call.signal" auth "keys" }
route "call.end"    { method POST path "/call/end"    intent "call.end"    auth "keys" }
route "call.ice"    { method GET  path "/call/ice"    intent "call.ice"    auth "keys" }
`, filepath.ToSlash(dbPath))
}

type env struct {
	t    *testing.T
	addr string
}

func start(t *testing.T) *env {
	t.Helper()
	rtc.Register()
	p, err := platform.Compile(context.Background(), []byte(app(filepath.Join(t.TempDir(), "r.db"))), t.TempDir(), platform.DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	srv := fh.NewFast()
	if err := p.Mount(srv); err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.ShutdownWithTimeout(time.Second); _ = ln.Close() })
	return &env{t: t, addr: ln.Addr().String()}
}

func (e *env) do(user, method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://"+e.addr+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", user+"-secret-key-0123456789")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// socket is a user's hub connection; events() yields the server events it gets.
type socket struct {
	ws     *websocket.Conn
	events chan map[string]any
}

func (e *env) connect(user string) *socket {
	e.t.Helper()
	cfg, _ := websocket.NewConfig("ws://"+e.addr+"/realtime/ws", "http://"+e.addr)
	cfg.Header.Set("X-API-Key", user+"-secret-key-0123456789")
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { ws.Close() })
	s := &socket{ws: ws, events: make(chan map[string]any, 64)}
	go func() {
		for {
			var m map[string]any
			if websocket.JSON.Receive(ws, &m) != nil {
				return
			}
			if m["type"] == "message" {
				s.events <- m
			}
		}
	}()
	return s
}

func (s *socket) next(t *testing.T, event string) map[string]any {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case m := <-s.events:
			if m["event"] == event {
				return m["data"].(map[string]any)
			}
		case <-deadline:
			t.Fatalf("no %q event within 10s", event)
		}
	}
}

func TestP2PCallOverTheHub(t *testing.T) {
	e := start(t)
	bob := e.connect("bob")
	eve := e.connect("eve")

	status, res := e.do("ada", "POST", "/call/start", map[string]any{"to": []string{"bob"}, "kind": "video", "mode": "p2p"})
	if status != 200 {
		t.Fatalf("start: %d %v", status, res)
	}
	call := res["call"].(map[string]any)["id"].(string)
	ice := res["ice"].([]any)
	if len(ice) != 2 || ice[1].(map[string]any)["credential"] == "" || strings.Contains(fmt.Sprint(res), "turn-shared-secret") {
		t.Fatalf("ice servers: %v", ice)
	}

	in := bob.next(t, "call.incoming")
	if in["call"].(map[string]any)["initiator"] != "ada" {
		t.Fatalf("incoming: %v", in)
	}
	if status, _ := e.do("ada", "POST", "/call/signal", map[string]any{"call": call, "to": "bob", "type": "offer", "data": map[string]any{"sdp": "v=0 offer"}}); status != 200 {
		t.Fatalf("offer: %d", status)
	}
	if status, _ := e.do("bob", "POST", "/call/answer", map[string]any{"call": call}); status != 200 {
		t.Fatalf("answer: %d", status)
	}
	sig := bob.next(t, "call.signal")
	if sig["from"] != "ada" || sig["type"] != "offer" {
		t.Fatalf("relayed signal: %v", sig)
	}
	// A stranger can neither join the call nor listen to it.
	if status, _ := e.do("eve", "POST", "/call/signal", map[string]any{"call": call, "to": "bob", "type": "offer", "data": map[string]any{}}); status != 403 {
		t.Fatalf("stranger signal: %d", status)
	}
	select {
	case m := <-eve.events:
		t.Fatalf("eve overheard %v", m)
	case <-time.After(200 * time.Millisecond):
	}
	if status, _ := e.do("bob", "POST", "/call/end", map[string]any{"call": call}); status != 200 {
		t.Fatalf("end: %d", status)
	}
	if ended := e.connectNext(t, "ada", call); ended != "" {
		t.Fatal(ended)
	}
	if status, res := e.do("eve", "GET", "/call/ice", nil); status != 200 || len(res["ice"].([]any)) != 2 {
		t.Fatalf("ice: %d %v", status, res)
	}
}

// connectNext checks, through the API, that the call is over for ada.
func (e *env) connectNext(t *testing.T, user, call string) string {
	status, _ := e.do(user, "POST", "/call/signal", map[string]any{"call": call, "to": "bob", "type": "candidate", "data": map[string]any{}})
	if status != 409 {
		return fmt.Sprintf("signal after the call ended returned %d, want 409", status)
	}
	return ""
}

func TestSFUGroupCallEndToEnd(t *testing.T) {
	e := start(t)
	adaWS, bobWS := e.connect("ada"), e.connect("bob")

	status, res := e.do("ada", "POST", "/call/start", map[string]any{"to": []string{"bob"}, "kind": "audio", "mode": "sfu"})
	if status != 200 {
		t.Fatalf("start: %d %v", status, res)
	}
	call := res["call"].(map[string]any)["id"].(string)
	bobWS.next(t, "call.incoming")

	// Joining before answering is refused for the invitee; a stranger is refused outright.
	sfuPost := func(user, action string, body any) (int, map[string]any) {
		return e.do(user, "POST", "/rtc/sfu/"+call+"/"+action, body)
	}
	if status, _ := sfuPost("bob", "join", map[string]any{"sdp": "x"}); status != 409 {
		t.Fatalf("join before answering: %d", status)
	}
	if status, _ := sfuPost("eve", "join", map[string]any{"sdp": "x"}); status != 403 {
		t.Fatalf("stranger join: %d", status)
	}
	if status, _ := e.do("bob", "POST", "/call/answer", map[string]any{"call": call}); status != 200 {
		t.Fatalf("answer: %d", status)
	}

	// Ada publishes; Bob listens.
	track, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "mic", "s")
	newPeerWith(t, e, call, "ada", adaWS, track)
	_, bobTracks := newPeerWith(t, e, call, "bob", bobWS, nil)

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		var seq uint16
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				seq++
				_ = track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: seq, Timestamp: uint32(seq) * 960, SSRC: 7}, Payload: []byte("opus")})
			}
		}
	}()
	select {
	case tr := <-bobTracks:
		if tr.StreamID() != "ada" {
			t.Fatalf("stream id %q", tr.StreamID())
		}
		for i := 0; i < 3; i++ {
			if _, _, err := tr.ReadRTP(); err != nil {
				t.Fatal(err)
			}
		}
	case <-time.After(20 * time.Second):
		t.Fatal("bob never received ada's audio through the SFU")
	}

	// Ending the call tears the room down.
	if status, _ := e.do("ada", "POST", "/call/end", map[string]any{"call": call}); status != 200 {
		t.Fatalf("end: %d", status)
	}
	if status, _ := sfuPost("ada", "join", map[string]any{"sdp": "x"}); status != 410 {
		t.Fatalf("join after end: %d", status)
	}
}

func newPeerWith(t *testing.T, e *env, call, user string, ws *socket, local *webrtc.TrackLocalStaticRTP) (*webrtc.PeerConnection, chan *webrtc.TrackRemote) {
	t.Helper()
	post := func(action string, body any) (int, map[string]any) {
		return e.do(user, "POST", "/rtc/sfu/"+call+"/"+action, body)
	}
	me := &webrtc.MediaEngine{}
	_ = me.RegisterDefaultCodecs()
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	tracks := make(chan *webrtc.TrackRemote, 4)
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { tracks <- tr })
	if local != nil {
		if _, err := pc.AddTrack(local); err != nil {
			t.Fatal(err)
		}
	} else if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	joined, queued := false, []webrtc.ICECandidateInit{}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if !joined {
			queued = append(queued, c.ToJSON())
			return
		}
		post("candidate", c.ToJSON())
	})
	go func() { // server events arrive over the hub
		for {
			select {
			case m := <-ws.events:
				data, _ := m["data"].(map[string]any)
				switch m["event"] {
				case "sfu.offer":
					_ = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: data["sdp"].(string)})
					ans, _ := pc.CreateAnswer(nil)
					_ = pc.SetLocalDescription(ans)
					post("answer", map[string]any{"sdp": ans.SDP})
				case "sfu.candidate":
					raw, _ := json.Marshal(data["candidate"])
					var ci webrtc.ICECandidateInit
					_ = json.Unmarshal(raw, &ci)
					_ = pc.AddICECandidate(ci)
				}
			case <-time.After(25 * time.Second):
				return
			}
		}
	}()
	offer, _ := pc.CreateOffer(nil)
	_ = pc.SetLocalDescription(offer)
	status, out := post("join", map[string]any{"sdp": offer.SDP})
	if status != 200 {
		t.Fatalf("%s join: %d %v", user, status, out)
	}
	_ = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: out["sdp"].(string)})
	mu.Lock()
	joined = true
	for _, c := range queued {
		post("candidate", c)
	}
	mu.Unlock()
	return pc, tracks
}
