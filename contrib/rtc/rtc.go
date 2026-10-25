// Package rtc adds audio and video calls to a REF application.
//
//	resource "calls" { kind "rtc.calls"  config { database "db" hub "live"
//	                     stun_urls ["stun:stun.l.google.com:19302"]
//	                     turn_urls ["turn:turn.example.com:3478"] turn_secret env.required("TURN_SECRET") } }
//	resource "sfu"   { kind "rtc.sfu"    config { hub "live" calls "calls" udp_port_min 50000 udp_port_max 50100 } }
//
// Nodes: call.start, call.answer, call.decline, call.end, call.get,
// call.signal and call.ice_servers. A p2p call relays SDP/ICE between the two
// peers through the hub; an sfu call has each participant talk to the server
// (rtc.sfu) over HTTP, and the server talks back over the hub.
//
// Register installs the drivers; the generic ref command does so before it runs
// an application.
package rtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/contrib/rtc/calls"
	"github.com/oarkflow/ref/contrib/rtc/sfu"
	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/platform"
	"github.com/pion/webrtc/v4"
)

var once sync.Once

// Register installs the rtc.* resources and call.* actions. Safe to call more
// than once.
func Register() { once.Do(register) }

func register() {
	platform.RegisterResourceDriver("rtc.calls", platform.ResourceFactoryFunc(openCalls), platform.ResourceKindInfo{
		Family:  "rtc",
		Summary: "Call state, signaling relay and STUN/TURN credentials for audio and video calls, stored in SQL.",
		Config: []platform.ConfigField{
			{Name: "database", Type: "resource", Required: true},
			{Name: "hub", Type: "resource", Required: true, Summary: "realtime.hub that delivers call events to users"},
			{Name: "stun_urls", Type: "list"}, {Name: "turn_urls", Type: "list"},
			{Name: "turn_secret", Type: "string", Summary: "Shared secret of the TURN server (coturn use-auth-secret)"},
			{Name: "turn_ttl", Type: "duration", Default: "1h"},
			{Name: "ring_timeout", Type: "duration", Default: "45s"},
			{Name: "max_invitees", Type: "int", Default: "20"},
		},
	})
	platform.RegisterResourceDriver("rtc.sfu", platform.ResourceFactoryFunc(openSFU), platform.ResourceKindInfo{
		Family:  "rtc",
		Summary: "Pion selective forwarding unit for group calls. One room lives on one node.",
		Config: []platform.ConfigField{
			{Name: "hub", Type: "resource", Required: true}, {Name: "calls", Type: "resource", Required: true},
			{Name: "path", Type: "string", Default: "/rtc/sfu"},
			{Name: "udp_port_min", Type: "int"}, {Name: "udp_port_max", Type: "int"},
			{Name: "public_ips", Type: "list", Summary: "Advertised host addresses for a server behind static NAT"},
			{Name: "max_peers", Type: "int", Default: "50"}, {Name: "max_rooms", Type: "int", Default: "1000"},
		},
	})
	for _, op := range callOps {
		registerOp(op)
	}
}

// ---------------------------------------------------------------------------
// rtc.calls
// ---------------------------------------------------------------------------

// Calls is the opened rtc.calls resource.
type Calls struct {
	Store *calls.Store
	hub   *platform.RealtimeHub

	mu      sync.RWMutex
	onLeave []func(callID, user string, ended bool)
}

// OnLeave registers a function told when a user leaves a call or a call ends
// (user is empty and ended true when the whole call is over).
func (c *Calls) OnLeave(f func(callID, user string, ended bool)) {
	c.mu.Lock()
	c.onLeave = append(c.onLeave, f)
	c.mu.Unlock()
}

func (c *Calls) left(call calls.Call, user string) {
	c.mu.RLock()
	fs := c.onLeave
	c.mu.RUnlock()
	for _, f := range fs {
		if call.Status == calls.Ended {
			f(call.ID, "", true)
		} else {
			f(call.ID, user, false)
		}
	}
}

func openCalls(ctx context.Context, spec platform.ResourceSpec) (platform.Resource, io.Closer, error) {
	if err := platform.RejectUnknownConfig("rtc.calls", spec.Config, "database", "hub", "stun_urls", "turn_urls",
		"turn_secret", "turn_ttl", "ring_timeout", "max_invitees"); err != nil {
		return nil, nil, err
	}
	dbRes, ok := spec.Dependency("database")
	db, isDB := dbRes.(*platform.Database)
	if !ok || !isDB {
		return nil, nil, fmt.Errorf("rtc.calls %q: database must name a database.sql resource", spec.Name)
	}
	hubRes, ok := spec.Dependency("hub")
	hub, isHub := hubRes.(*platform.RealtimeHub)
	if !ok || !isHub {
		return nil, nil, fmt.Errorf("rtc.calls %q: hub must name a realtime.hub resource", spec.Name)
	}
	ttl, err := platform.ConfigDuration(spec.Config, "turn_ttl", time.Hour)
	if err != nil {
		return nil, nil, err
	}
	ring, err := platform.ConfigDuration(spec.Config, "ring_timeout", 45*time.Second)
	if err != nil {
		return nil, nil, err
	}
	maxInv, err := platform.ConfigInt(spec.Config, "max_invitees", 20)
	if err != nil {
		return nil, nil, err
	}
	turn, secret := platform.ConfigStrings(spec.Config, "turn_urls"), platform.ConfigString(spec.Config, "turn_secret", "")
	if len(turn) > 0 && secret == "" {
		return nil, nil, fmt.Errorf("rtc.calls %q: turn_urls needs a turn_secret; TURN without credentials is an open relay", spec.Name)
	}
	store, err := calls.Open(ctx, db.DB, calls.Options{
		Dialect: db.Dialect,
		Rebind:  func(q string) string { return platform.Rebind(db.Dialect, q) },
		Notifier: calls.NotifierFunc(func(ctx context.Context, topic, event string, data json.RawMessage, from string) error {
			_, err := hub.Hub.Emit(ctx, topic, event, data, from)
			return err
		}),
		ICE:         calls.ICEConfig{STUN: platform.ConfigStrings(spec.Config, "stun_urls"), TURN: turn, Secret: secret, TTL: ttl},
		RingTimeout: ring, MaxInvitees: maxInv,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("rtc.calls %q: %w", spec.Name, err)
	}
	return &Calls{Store: store, hub: hub}, nil, nil
}

// ---------------------------------------------------------------------------
// rtc.sfu
// ---------------------------------------------------------------------------

// SFU is the opened rtc.sfu resource. It serves, under path:
//
//	POST {path}/{call}/join       {"sdp": "..."}            -> {"type":"answer","sdp":"..."}
//	POST {path}/{call}/answer     {"sdp": "..."}            answer to a server offer
//	POST {path}/{call}/candidate  {"candidate": "...", ...} a client ICE candidate
//	POST {path}/{call}/leave
//
// and sends the client, as events on its private topic, "sfu.offer"
// ({call_id, type, sdp}) and "sfu.candidate" ({call_id, candidate}).
type SFU struct {
	Server *sfu.Server
	path   string
	calls  *Calls
	hub    *platform.RealtimeHub
}

func openSFU(_ context.Context, spec platform.ResourceSpec) (platform.Resource, io.Closer, error) {
	if err := platform.RejectUnknownConfig("rtc.sfu", spec.Config, "hub", "calls", "path", "udp_port_min",
		"udp_port_max", "public_ips", "max_peers", "max_rooms"); err != nil {
		return nil, nil, err
	}
	hubRes, ok := spec.Dependency("hub")
	hub, isHub := hubRes.(*platform.RealtimeHub)
	if !ok || !isHub {
		return nil, nil, fmt.Errorf("rtc.sfu %q: hub must name a realtime.hub resource", spec.Name)
	}
	callsRes, ok := spec.Dependency("calls")
	cs, isCalls := callsRes.(*Calls)
	if !ok || !isCalls {
		return nil, nil, fmt.Errorf("rtc.sfu %q: calls must name an rtc.calls resource", spec.Name)
	}
	ints := map[string]int{}
	for _, k := range []string{"udp_port_min", "udp_port_max", "max_peers", "max_rooms"} {
		n, err := platform.ConfigInt(spec.Config, k, 0)
		if err != nil {
			return nil, nil, err
		}
		ints[k] = n
	}
	if (ints["udp_port_min"] == 0) != (ints["udp_port_max"] == 0) || ints["udp_port_min"] > ints["udp_port_max"] || ints["udp_port_max"] > 65535 {
		return nil, nil, fmt.Errorf("rtc.sfu %q: udp_port_min and udp_port_max must both be set, min <= max <= 65535", spec.Name)
	}
	s := &SFU{path: strings.TrimRight(platform.ConfigString(spec.Config, "path", "/rtc/sfu"), "/"), calls: cs, hub: hub}
	var ice []webrtc.ICEServer
	for _, srv := range cs.Store.ICE("") {
		if srv.Username == "" {
			ice = append(ice, webrtc.ICEServer{URLs: srv.URLs})
		}
	}
	server, err := sfu.New(sfu.Config{
		ICEServers: ice, UDPPortMin: uint16(ints["udp_port_min"]), UDPPortMax: uint16(ints["udp_port_max"]),
		PublicIPs: platform.ConfigStrings(spec.Config, "public_ips"), MaxPeers: ints["max_peers"], MaxRooms: ints["max_rooms"],
	}, s)
	if err != nil {
		return nil, nil, fmt.Errorf("rtc.sfu %q: %w", spec.Name, err)
	}
	s.Server = server
	cs.OnLeave(func(callID, user string, ended bool) {
		if ended {
			for _, u := range server.Peers(callID) {
				server.Leave(callID, u)
			}
		} else {
			server.Leave(callID, user)
		}
	})
	return s, closerFunc(server.Close), nil
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// Offer implements sfu.Signaler over the hub.
func (s *SFU) Offer(user, call string, offer webrtc.SessionDescription) error {
	raw, _ := json.Marshal(map[string]any{"call_id": call, "type": "offer", "sdp": offer.SDP})
	_, err := s.hub.Hub.Emit(context.Background(), "user:"+user, "sfu.offer", raw, "")
	return err
}

// Candidate implements sfu.Signaler over the hub.
func (s *SFU) Candidate(user, call string, c webrtc.ICECandidateInit) error {
	raw, _ := json.Marshal(map[string]any{"call_id": call, "candidate": c})
	_, err := s.hub.Hub.Emit(context.Background(), "user:"+user, "sfu.candidate", raw, "")
	return err
}

// MountApp implements platform.AppMounter.
func (s *SFU) MountApp(app *fh.App) error {
	app.Post(s.path+"/:call/join", s.handle(s.join))
	app.Post(s.path+"/:call/answer", s.handle(s.answer))
	app.Post(s.path+"/:call/candidate", s.handle(s.candidate))
	app.Post(s.path+"/:call/leave", s.handle(s.leave))
	return nil
}

type sfuRequest struct {
	call string
	user string
	body []byte
}

func (s *SFU) handle(f func(ctx context.Context, r sfuRequest) (any, error)) fh.HandlerFunc {
	return func(c fh.Ctx) error {
		fail := func(status int, code string) error {
			return c.Status(status).JSON(map[string]any{"error": map[string]any{"code": code}})
		}
		// A JSON content type cannot be sent cross-site without a preflight, so
		// this keeps a page on another origin from driving a signed-in user's
		// cookie into joining a call.
		if !strings.HasPrefix(strings.ToLower(c.Get("Content-Type")), "application/json") {
			return fail(415, "json_required")
		}
		subject, err := s.hub.Identify(c)
		if err != nil {
			return fail(401, "unauthenticated")
		}
		// c.Body() is fh's read buffer, valid only until the handler returns, and
		// Pion keeps substrings of the SDP it is given; own the bytes.
		body := bytes.Clone(c.Body())
		if len(body) > 64<<10 {
			return fail(413, "too_large")
		}
		id := strings.Clone(c.Params("call")) // a view of fh's buffer otherwise; the room outlives the request
		subject.ID, subject.Tenant = strings.Clone(subject.ID), strings.Clone(subject.Tenant)
		call, err := s.calls.Store.Get(c.Context(), calls.Actor{ID: subject.ID, Tenant: subject.Tenant}, id)
		switch {
		case errors.Is(err, calls.ErrForbidden):
			return fail(403, "forbidden")
		case err != nil:
			return fail(503, "unavailable")
		case call.Mode != calls.SFU:
			return fail(400, "not_an_sfu_call")
		case call.Status == calls.Ended:
			return fail(410, "call_ended")
		}
		if state := participantState(call, subject.ID); state != "joined" {
			return fail(409, "answer_the_call_first")
		}
		out, err := f(c.Context(), sfuRequest{call: id, user: subject.ID, body: body})
		switch {
		case errors.Is(err, sfu.ErrRoomFull):
			return fail(429, "room_full")
		case errors.Is(err, sfu.ErrNoPeer):
			return fail(409, "not_in_room")
		case err != nil:
			return fail(400, "bad_request")
		}
		return c.Status(200).JSON(out)
	}
}

func participantState(c calls.Call, user string) string {
	for _, p := range c.Participants {
		if p.User == user {
			return p.State
		}
	}
	return ""
}

func (s *SFU) join(ctx context.Context, r sfuRequest) (any, error) {
	var in struct{ SDP string }
	if err := json.Unmarshal(r.body, &in); err != nil || in.SDP == "" {
		return nil, errors.New("sdp required")
	}
	ans, err := s.Server.Join(ctx, r.call, r.user, webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: strings.Clone(in.SDP)})
	if err != nil {
		return nil, err
	}
	return map[string]any{"type": "answer", "sdp": ans.SDP}, nil
}

func (s *SFU) answer(_ context.Context, r sfuRequest) (any, error) {
	var in struct{ SDP string }
	if err := json.Unmarshal(r.body, &in); err != nil || in.SDP == "" {
		return nil, errors.New("sdp required")
	}
	return map[string]any{"ok": true}, s.Server.Answer(r.call, r.user, webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: strings.Clone(in.SDP)})
}

func (s *SFU) candidate(_ context.Context, r sfuRequest) (any, error) {
	var in webrtc.ICECandidateInit
	if err := json.Unmarshal(r.body, &in); err != nil || in.Candidate == "" {
		return nil, errors.New("candidate required")
	}
	in.Candidate = strings.Clone(in.Candidate)
	return map[string]any{"ok": true}, s.Server.Candidate(r.call, r.user, in)
}

func (s *SFU) leave(_ context.Context, r sfuRequest) (any, error) {
	s.Server.Leave(r.call, r.user)
	return map[string]any{"ok": true}, nil
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

type callArgs struct {
	ctx    *platform.ActionContext
	config map[string]any
}

func (a callArgs) str(key string) (string, error) {
	if fact := platform.ConfigString(a.config, key+"_fact", ""); fact != "" {
		return platform.FactString(a.ctx.Inputs, fact)
	}
	if v := platform.ConfigString(a.config, key, ""); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%s is required (set %s or %s_fact)", key, key, key)
}

func (a callArgs) list(key string) []string {
	if fact := platform.ConfigString(a.config, key+"_fact", ""); fact != "" {
		v, _ := platform.ResolvePath(a.ctx.Inputs, fact)
		switch x := v.(type) {
		case []string:
			return x
		case []any:
			out := make([]string, 0, len(x))
			for _, e := range x {
				out = append(out, fmt.Sprint(e))
			}
			return out
		case string:
			return []string{x}
		}
		return nil
	}
	return platform.ConfigStrings(a.config, key)
}

type callOp struct {
	name, summary, kind, provides string
	fields                        []string
	run                           func(ctx context.Context, r *Calls, by calls.Actor, a callArgs) (any, error)
}

func failure(err error) error {
	f := intent.Failure{Message: err.Error()}
	switch {
	case errors.Is(err, calls.ErrForbidden):
		f.Code, f.Category, f.Message = "PERMISSION_DENIED", intent.CategoryPermission, "you are not allowed to do that in this call"
	case errors.Is(err, calls.ErrInvalid):
		f.Code, f.Category = "INVALID_INPUT", intent.CategoryInvalidInput
	case errors.Is(err, calls.ErrState):
		f.Code, f.Category, f.Message = "CONFLICT", intent.CategoryConflict, "the call is over"
	default:
		f.Code, f.Category, f.Message = "UNAVAILABLE", intent.CategoryUnavailable, "calls are unavailable"
	}
	return f
}

func invalid(msg string) error {
	return intent.Failure{Code: "INVALID_INPUT", Category: intent.CategoryInvalidInput, Message: msg}
}

var callOps = []callOp{
	{name: "call.start", summary: "Place a call. mode p2p is for two people; sfu for a group. Rings every invitee.", kind: "effect", provides: "{call, ice}",
		fields: []string{"to, to_fact (user ids)", "kind (audio|video, default video)", "mode (p2p|sfu, default p2p)"},
		run: func(ctx context.Context, r *Calls, by calls.Actor, a callArgs) (any, error) {
			kind, mode := "video", calls.P2P
			if v, err := a.str("kind"); err == nil {
				kind = v
			}
			if v, err := a.str("mode"); err == nil {
				mode = v
			}
			c, err := r.Store.Start(ctx, by, a.list("to"), kind, mode)
			if err != nil {
				return nil, err
			}
			return map[string]any{"call": c, "ice": r.Store.ICE(by.ID)}, nil
		}},
	{name: "call.answer", summary: "Accept a call", kind: "effect", provides: "{call, ice}", fields: []string{"call, call_fact"},
		run: func(ctx context.Context, r *Calls, by calls.Actor, a callArgs) (any, error) {
			id, err := a.str("call")
			if err != nil {
				return nil, invalid(err.Error())
			}
			c, err := r.Store.Answer(ctx, by, id)
			if err != nil {
				return nil, err
			}
			return map[string]any{"call": c, "ice": r.Store.ICE(by.ID)}, nil
		}},
	{name: "call.decline", summary: "Refuse a call", kind: "effect", provides: "the call", fields: []string{"call, call_fact"},
		run: func(ctx context.Context, r *Calls, by calls.Actor, a callArgs) (any, error) {
			id, err := a.str("call")
			if err != nil {
				return nil, invalid(err.Error())
			}
			c, err := r.Store.Decline(ctx, by, id)
			if err == nil {
				r.left(c, by.ID)
			}
			return c, err
		}},
	{name: "call.end", summary: "Hang up (cancels a ringing call; leaves or ends an active one)", kind: "effect", provides: "the call", fields: []string{"call, call_fact"},
		run: func(ctx context.Context, r *Calls, by calls.Actor, a callArgs) (any, error) {
			id, err := a.str("call")
			if err != nil {
				return nil, invalid(err.Error())
			}
			c, err := r.Store.End(ctx, by, id)
			if err == nil {
				r.left(c, by.ID)
			}
			return c, err
		}},
	{name: "call.get", summary: "A call the caller takes part in", kind: "read", provides: "the call", fields: []string{"call, call_fact"},
		run: func(ctx context.Context, r *Calls, by calls.Actor, a callArgs) (any, error) {
			id, err := a.str("call")
			if err != nil {
				return nil, invalid(err.Error())
			}
			return r.Store.Get(ctx, by, id)
		}},
	{name: "call.signal", summary: "Relay an SDP offer/answer or ICE candidate to the other peer of a p2p call", kind: "effect", provides: "true",
		fields: []string{"call, call_fact", "to, to_fact", "type (offer|answer|candidate|renegotiate|mute|unmute)", "data_fact"},
		run: func(ctx context.Context, r *Calls, by calls.Actor, a callArgs) (any, error) {
			id, err := a.str("call")
			if err != nil {
				return nil, invalid(err.Error())
			}
			to, err := a.str("to")
			if err != nil {
				return nil, invalid(err.Error())
			}
			typ, err := a.str("type")
			if err != nil {
				return nil, invalid(err.Error())
			}
			var data json.RawMessage
			if fact := platform.ConfigString(a.config, "data_fact", ""); fact != "" {
				v, _ := platform.ResolvePath(a.ctx.Inputs, fact)
				data, _ = json.Marshal(v)
			}
			return true, r.Store.Signal(ctx, by, id, to, typ, data)
		}},
	{name: "call.ice_servers", summary: "STUN/TURN servers for the caller, with a short-lived TURN credential", kind: "read", provides: "{ice}",
		run: func(ctx context.Context, r *Calls, by calls.Actor, a callArgs) (any, error) {
			return map[string]any{"ice": r.Store.ICE(by.ID)}, nil
		}},
}

func registerOp(op callOp) {
	cfg := make([]platform.ConfigField, 0, len(op.fields))
	for _, f := range op.fields {
		cfg = append(cfg, platform.ConfigField{Name: f, Type: "string"})
	}
	platform.RegisterActionDriver(op.name, platform.ActionFactoryFunc(func(build platform.BuildContext, spec platform.NodeSpec) (platform.Action, error) {
		res, ok := build.Resource(spec.Resource)
		if !ok {
			return nil, fmt.Errorf("node %q: resource %q is not declared", spec.Name, spec.Resource)
		}
		cs, ok := res.(*Calls)
		if !ok {
			return nil, fmt.Errorf("node %q: resource %q is not an rtc.calls", spec.Name, spec.Resource)
		}
		return platform.ActionFunc(func(ctx *platform.ActionContext) (platform.ActionResult, error) {
			if ctx.Principal.ID == "" {
				return platform.ActionResult{}, intent.Failure{Code: "PERMISSION_DENIED", Category: intent.CategoryPermission, Message: "calls need a signed-in user"}
			}
			out, err := op.run(ctx.Context, cs, calls.Actor{ID: ctx.Principal.ID, Tenant: ctx.TenantID}, callArgs{ctx: ctx, config: spec.Config})
			if err != nil {
				var f intent.Failure
				if errors.As(err, &f) {
					return platform.ActionResult{}, err
				}
				return platform.ActionResult{}, failure(err)
			}
			return platform.Acknowledge(spec, out), nil
		}), nil
	}), platform.ActionInfo{Family: "call", Summary: op.summary, ResourceKind: "rtc.calls", Kind: op.kind, Provides: op.provides, Config: cfg})
}
