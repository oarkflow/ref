// Package rtc connects a REF application to the call service (rtcd).
//
// The call service is independent of REF: it owns calls, signaling, the SFU and
// recording, and any backend can use it. This package is REF's client of it.
//
//	resource "calls" {
//	  kind "rtc.service"
//	  config {
//	    url     "https://calls.example.com"
//	    api_key env.required("RTCD_API_KEY")
//	  }
//	}
//
// Nodes: call.start, call.answer, call.decline, call.end, call.get,
// call.signal, call.e2ee, call.ice_servers and call.token. They act as the
// signed-in user. call.token hands that user a short-lived token, with which the
// user's browser talks to the call service directly (events, SFU signaling,
// media), so none of that traffic passes through this application.
//
// Register installs the driver; the generic ref command does so before it runs
// an application.
package rtc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/rtcd/client"
)

var once sync.Once

// Register installs the rtc.service resource and the call.* actions. Safe to
// call more than once.
func Register() { once.Do(register) }

func register() {
	platform.RegisterResourceDriver("rtc.service", platform.ResourceFactoryFunc(openService), platform.ResourceKindInfo{
		Family:  "rtc",
		Summary: "Connection to a call service (rtcd): audio and video calls, p2p or through its SFU, end-to-end encrypted.",
		Config: []platform.ConfigField{
			{Name: "url", Type: "string", Required: true, Summary: "Where this application reaches the call service"},
			{Name: "public_url", Type: "string", Summary: "Where browsers reach it, if different from url"},
			{Name: "api_key", Type: "string", Required: true, Summary: "This application's service key; keep it in the environment"},
			{Name: "timeout", Type: "duration", Default: "10s"},
			{Name: "token_ttl", Type: "duration", Default: "15m", Summary: "Life of the token call.token gives a user's browser"},
		},
	})
	for _, op := range callOps {
		registerOp(op)
	}
}

// Service is the opened rtc.service resource.
type Service struct {
	Client    *client.Client
	PublicURL string
	TokenTTL  time.Duration
}

func openService(_ context.Context, spec platform.ResourceSpec) (platform.Resource, io.Closer, error) {
	if err := platform.RejectUnknownConfig("rtc.service", spec.Config, "url", "public_url", "api_key", "timeout", "token_ttl"); err != nil {
		return nil, nil, err
	}
	url := strings.TrimRight(platform.ConfigString(spec.Config, "url", ""), "/")
	key := platform.ConfigString(spec.Config, "api_key", "")
	if url == "" || key == "" {
		return nil, nil, fmt.Errorf("rtc.service %q: url and api_key are required", spec.Name)
	}
	timeout, err := platform.ConfigDuration(spec.Config, "timeout", 10*time.Second)
	if err != nil {
		return nil, nil, err
	}
	ttl, err := platform.ConfigDuration(spec.Config, "token_ttl", 15*time.Minute)
	if err != nil {
		return nil, nil, err
	}
	c := client.New(url, key)
	c.HTTP.Timeout = timeout
	pub := strings.TrimRight(platform.ConfigString(spec.Config, "public_url", url), "/")
	return &Service{Client: c, PublicURL: pub, TokenTTL: ttl}, nil, nil
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
	run                           func(ctx context.Context, s *Service, user string, a callArgs) (any, error)
}

func failure(err error) error {
	f := intent.Failure{Message: err.Error()}
	var ce *client.Error
	switch {
	case errors.As(err, &ce) && ce.Status == 403:
		f.Code, f.Category, f.Message = "PERMISSION_DENIED", intent.CategoryPermission, "you are not allowed to do that in this call"
	case errors.As(err, &ce) && ce.Status == 400:
		f.Code, f.Category = "INVALID_INPUT", intent.CategoryInvalidInput
	case errors.As(err, &ce) && (ce.Status == 409 || ce.Status == 410):
		f.Code, f.Category, f.Message = "CONFLICT", intent.CategoryConflict, "the call is over"
	default:
		// Includes a refused service key: that is this application's mistake,
		// not the user's, so it is not described to the user.
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
		run: func(ctx context.Context, s *Service, user string, a callArgs) (any, error) {
			req := client.StartRequest{To: a.list("to"), Kind: "video", Mode: "p2p"}
			if v, err := a.str("kind"); err == nil {
				req.Kind = v
			}
			if v, err := a.str("mode"); err == nil {
				req.Mode = v
			}
			return s.Client.As(user).Start(ctx, req)
		}},
	{name: "call.answer", summary: "Accept a call", kind: "effect", provides: "{call, ice}", fields: []string{"call, call_fact"},
		run: func(ctx context.Context, s *Service, user string, a callArgs) (any, error) {
			id, err := a.str("call")
			if err != nil {
				return nil, invalid(err.Error())
			}
			return s.Client.As(user).Answer(ctx, id)
		}},
	{name: "call.decline", summary: "Refuse a call", kind: "effect", provides: "the call", fields: []string{"call, call_fact"},
		run: func(ctx context.Context, s *Service, user string, a callArgs) (any, error) {
			id, err := a.str("call")
			if err != nil {
				return nil, invalid(err.Error())
			}
			return s.Client.As(user).Decline(ctx, id)
		}},
	{name: "call.end", summary: "Hang up (cancels a ringing call; leaves or ends an active one)", kind: "effect", provides: "the call", fields: []string{"call, call_fact"},
		run: func(ctx context.Context, s *Service, user string, a callArgs) (any, error) {
			id, err := a.str("call")
			if err != nil {
				return nil, invalid(err.Error())
			}
			return s.Client.As(user).End(ctx, id)
		}},
	{name: "call.get", summary: "A call the caller takes part in", kind: "read", provides: "the call", fields: []string{"call, call_fact"},
		run: func(ctx context.Context, s *Service, user string, a callArgs) (any, error) {
			id, err := a.str("call")
			if err != nil {
				return nil, invalid(err.Error())
			}
			return s.Client.As(user).Get(ctx, id)
		}},
	{name: "call.signal", summary: "Relay an SDP offer/answer or ICE candidate to the other peer of a p2p call", kind: "effect", provides: "true",
		fields: []string{"call, call_fact", "to, to_fact", "type (offer|answer|candidate|renegotiate|mute|unmute)", "data_fact"},
		run: func(ctx context.Context, s *Service, user string, a callArgs) (any, error) {
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
			var data any
			if fact := platform.ConfigString(a.config, "data_fact", ""); fact != "" {
				data, _ = platform.ResolvePath(a.ctx.Inputs, fact)
			}
			return true, s.Client.As(user).Signal(ctx, id, to, typ, data)
		}},
	{name: "call.e2ee", summary: "Relay an end-to-end encryption message (a key exchange) to another participant. The service only carries it: it cannot read, change or forge it.", kind: "effect", provides: "true",
		fields: []string{"call, call_fact", "to, to_fact", "data_fact (a string)"},
		run: func(ctx context.Context, s *Service, user string, a callArgs) (any, error) {
			id, err := a.str("call")
			if err != nil {
				return nil, invalid(err.Error())
			}
			to, err := a.str("to")
			if err != nil {
				return nil, invalid(err.Error())
			}
			data, err := a.str("data")
			if err != nil {
				return nil, invalid(err.Error())
			}
			return true, s.Client.As(user).E2EE(ctx, id, to, data)
		}},
	{name: "call.ice_servers", summary: "STUN/TURN servers for the caller, with a short-lived TURN credential", kind: "read", provides: "{ice}",
		run: func(ctx context.Context, s *Service, user string, a callArgs) (any, error) {
			ice, err := s.Client.As(user).ICE(ctx)
			return map[string]any{"ice": ice}, err
		}},
	{name: "call.token", summary: "A short-lived token for the caller's browser to use the call service directly (events, group-call signaling, media)", kind: "effect", provides: "{token, expires_at, url}",
		run: func(ctx context.Context, s *Service, user string, a callArgs) (any, error) {
			t, err := s.Client.Token(ctx, user, s.TokenTTL)
			if err != nil {
				return nil, err
			}
			return map[string]any{"token": t.Token, "expires_at": t.ExpiresAt, "url": s.PublicURL}, nil
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
		svc, ok := res.(*Service)
		if !ok {
			return nil, fmt.Errorf("node %q: resource %q is not an rtc.service", spec.Name, spec.Resource)
		}
		return platform.ActionFunc(func(ctx *platform.ActionContext) (platform.ActionResult, error) {
			if ctx.Principal.ID == "" {
				return platform.ActionResult{}, intent.Failure{Code: "PERMISSION_DENIED", Category: intent.CategoryPermission, Message: "calls need a signed-in user"}
			}
			out, err := op.run(ctx.Context, svc, ctx.Principal.ID, callArgs{ctx: ctx, config: spec.Config})
			if err != nil {
				var f intent.Failure
				if errors.As(err, &f) {
					return platform.ActionResult{}, err
				}
				return platform.ActionResult{}, failure(err)
			}
			return platform.Acknowledge(spec, out), nil
		}), nil
	}), platform.ActionInfo{Family: "call", Summary: op.summary, ResourceKind: "rtc.service", Kind: op.kind, Provides: op.provides, Config: cfg})
}
