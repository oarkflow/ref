package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/fh/mw/session"
	"github.com/oarkflow/ref/invocation"
	"github.com/oarkflow/ref/realtime"
	"github.com/oarkflow/ref/runtime"
	"github.com/redis/go-redis/v9"
)

// realtime.hub: WebSocket and SSE endpoints backed by a realtime.Hub.
//
// The hub authenticates every connection with the named authenticator and
// refuses it when there is none, so a hub cannot be declared open by omission.
// Topic access is decided by `rules`, longest matching prefix first; a topic no
// rule covers falls back to realtime.DefaultAuthorize (any signed-in user,
// never another user's private topic).
//
//	resource "live" {
//	  kind "realtime.hub"
//	  config {
//	    authenticator "session_auth"
//	    session "sessions"
//	    backplane "redis"  redis_addr env.required("REDIS_ADDR")
//	    callable ["chat.send"]
//	    rules { "admin:" { subscribe ["admin"] publish ["admin"] } }
//	  }
//	}

// RealtimeHub is the opened resource.
type RealtimeHub struct {
	Hub *realtime.Hub

	name     string
	mount    realtime.MountConfig
	auth     Authenticator
	sessions *session.SessionManager
	callable map[string]bool
	platform *Platform
}

type realtimeRule struct {
	prefix    string
	subscribe []string
	publish   []string
}

var realtimeConfigKeys = []string{
	"authenticator", "session", "ws_path", "sse_path", "send_path", "allowed_origins",
	"max_message", "heartbeat", "send_buffer", "history", "presence_ttl", "node_id",
	"backplane", "redis_url", "redis_addr", "redis_password", "redis_db", "redis_channel",
	"callable", "rules",
}

func registerRealtimeResources(r *Registry) {
	mustResource(r, "realtime.hub", ResourceFactoryFunc(openRealtimeHub), ResourceKindInfo{
		Family:  "realtime",
		Summary: "WebSocket and SSE hub with topics, presence and optional multi-node fan-out over Redis.",
		Config: []ConfigField{
			{Name: "authenticator", Type: "resource", Required: true, Summary: "Authenticates each connection; there is no anonymous mode"},
			{Name: "session", Type: "resource", Summary: "Session manager, so cookie-authenticated browsers can connect"},
			{Name: "ws_path", Type: "string", Default: "/realtime/ws"},
			{Name: "sse_path", Type: "string", Default: "/realtime/events"},
			{Name: "send_path", Type: "string", Default: "/realtime/send", Summary: "Where SSE clients post frames"},
			{Name: "allowed_origins", Type: "list", Summary: "Browser origins besides the server's own"},
			{Name: "max_message", Type: "int", Default: "65536"},
			{Name: "heartbeat", Type: "duration", Default: "25s"},
			{Name: "send_buffer", Type: "int", Default: "256", Summary: "A client further behind than this is dropped"},
			{Name: "history", Type: "int", Default: "100", Summary: "Messages per topic kept for resume"},
			{Name: "backplane", Type: "string", Default: "none", Summary: "none (single node) or redis (several nodes)"},
			{Name: "redis_url", Type: "string"},
			{Name: "callable", Type: "list", Summary: "Intents a client may invoke with the call op"},
			{Name: "rules", Type: "object", Summary: "prefix -> {subscribe [roles], publish [roles]}"},
		},
	})
}

func openRealtimeHub(ctx context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("realtime.hub", spec.Config, realtimeConfigKeys...); err != nil {
		return nil, nil, err
	}
	authName, err := requiredString(spec.Config, "authenticator")
	if err != nil {
		return nil, nil, fmt.Errorf("realtime.hub %q: %w", spec.Name, err)
	}
	auth, ok := asAuthenticator(spec.resolved[authName])
	if !ok {
		return nil, nil, fmt.Errorf("realtime.hub %q: %q is not an authenticator", spec.Name, authName)
	}
	res := &RealtimeHub{name: spec.Name, auth: auth, callable: map[string]bool{}}
	if name := configString(spec.Config, "session", ""); name != "" {
		manager, ok := spec.resolved[name].(*session.SessionManager)
		if !ok {
			return nil, nil, fmt.Errorf("realtime.hub %q: %q is not a session manager", spec.Name, name)
		}
		res.sessions = manager
	}
	for _, name := range configStrings(spec.Config, "callable") {
		res.callable[name] = true
	}

	maxMessage, err := configInt64(spec.Config, "max_message", 0)
	if err != nil {
		return nil, nil, err
	}
	heartbeat, err := configDuration(spec.Config, "heartbeat", 0)
	if err != nil {
		return nil, nil, err
	}
	sendBuffer, err := configInt(spec.Config, "send_buffer", 0)
	if err != nil {
		return nil, nil, err
	}
	history, err := configInt(spec.Config, "history", 0)
	if err != nil {
		return nil, nil, err
	}
	ttl, err := configDuration(spec.Config, "presence_ttl", 0)
	if err != nil {
		return nil, nil, err
	}
	res.mount = realtime.MountConfig{
		WSPath:         configString(spec.Config, "ws_path", ""),
		SSEPath:        configString(spec.Config, "sse_path", ""),
		SendPath:       configString(spec.Config, "send_path", ""),
		AllowedOrigins: configStrings(spec.Config, "allowed_origins"),
		MaxMessage:     maxMessage,
		Heartbeat:      heartbeat,
		Identify:       res.identify,
	}

	rules, err := parseRealtimeRules(spec.Config["rules"])
	if err != nil {
		return nil, nil, fmt.Errorf("realtime.hub %q: %w", spec.Name, err)
	}

	opts := realtime.Options{
		NodeID: configString(spec.Config, "node_id", ""), SendBuffer: sendBuffer, History: history,
		PresenceTTL: ttl,
		Authorize:   authorizeByRules(rules),
		Call:        res.call,
	}
	var closers []io.Closer
	switch mode := configString(spec.Config, "backplane", "none"); mode {
	case "none", "":
	case "redis":
		client, err := redisClientFromConfig(spec.Config)
		if err != nil {
			return nil, nil, fmt.Errorf("realtime.hub %q: %w", spec.Name, err)
		}
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = client.Ping(pingCtx).Err()
		cancel()
		if err != nil {
			_ = client.Close()
			return nil, nil, fmt.Errorf("realtime.hub %q: redis backplane unreachable: %w", spec.Name, err)
		}
		opts.Backplane = realtime.NewRedisPlane(client, configString(spec.Config, "redis_channel", "ref:realtime:"+spec.Name))
		closers = append(closers, client)
	default:
		return nil, nil, fmt.Errorf("realtime.hub %q: backplane %q is not one of none, redis", spec.Name, mode)
	}
	hub, err := realtime.New(opts)
	if err != nil {
		for _, c := range closers {
			_ = c.Close()
		}
		return nil, nil, fmt.Errorf("realtime.hub %q: %w", spec.Name, err)
	}
	res.Hub = hub
	return res, closerFunc(func() error {
		err := hub.Close()
		for _, c := range closers {
			_ = c.Close()
		}
		return err
	}), nil
}

func redisClientFromConfig(config map[string]any) (*redis.Client, error) {
	if url := configString(config, "redis_url", ""); url != "" {
		opts, err := redis.ParseURL(url)
		if err != nil {
			return nil, fmt.Errorf("redis_url: %w", err)
		}
		return redis.NewClient(opts), nil
	}
	addr, err := requiredString(config, "redis_addr")
	if err != nil {
		return nil, fmt.Errorf("the redis backplane needs redis_url or redis_addr")
	}
	db, err := configInt(config, "redis_db", 0)
	if err != nil {
		return nil, err
	}
	return redis.NewClient(&redis.Options{Addr: addr, Password: configString(config, "redis_password", ""), DB: db}), nil
}

func parseRealtimeRules(raw any) ([]realtimeRule, error) {
	if raw == nil {
		return nil, nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("rules must map a topic prefix to {subscribe, publish}")
	}
	var rules []realtimeRule
	for prefix, v := range table {
		body, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("rules.%s must be an object", prefix)
		}
		for k := range body {
			if k != "subscribe" && k != "publish" {
				return nil, fmt.Errorf("rules.%s: unknown key %q (want subscribe, publish)", prefix, k)
			}
		}
		rules = append(rules, realtimeRule{prefix: prefix, subscribe: stringSlice(body["subscribe"]), publish: stringSlice(body["publish"])})
	}
	// Longest prefix wins, so "room:vip:" can be stricter than "room:".
	slices.SortFunc(rules, func(a, b realtimeRule) int { return len(b.prefix) - len(a.prefix) })
	return rules, nil
}

func authorizeByRules(rules []realtimeRule) func(realtime.Subject, string, realtime.Action) error {
	return func(s realtime.Subject, topic string, action realtime.Action) error {
		if err := realtime.DefaultAuthorize(s, topic, action); err != nil {
			return err
		}
		for _, r := range rules {
			if !strings.HasPrefix(topic, r.prefix) {
				continue
			}
			roles := r.subscribe
			if action == realtime.ActionPublish {
				roles = r.publish
			}
			if len(roles) == 0 {
				return nil // the rule names no roles for this action: any signed-in user
			}
			for _, have := range s.Roles {
				if slices.Contains(roles, have) {
					return nil
				}
			}
			return realtime.ErrForbidden
		}
		return nil
	}
}

// Identify authenticates a request exactly as the hub's own endpoints do. Other
// resources that serve endpoints for signed-in users (the SFU) call it so there
// is one definition of "who is this".
func (r *RealtimeHub) Identify(c fh.Ctx) (realtime.Subject, error) { return r.identify(c) }

// identify resolves the caller exactly as a route with auth and session would.
func (r *RealtimeHub) identify(c fh.Ctx) (realtime.Subject, error) {
	ctx := c.Context()
	sessionID := ""
	if r.sessions != nil {
		sess, complete, err := r.sessions.Begin(c)
		if err != nil {
			return realtime.Subject{}, err
		}
		c.OnBeforeResponse(complete)
		ctx = context.WithValue(ctx, sessionContextKey{}, sess)
		sessionID = sess.ID
		c.SetContext(ctx)
	}
	creds := Credentials{
		BearerToken: extractBearer(c.Get("Authorization")),
		APIKey:      c.Get("X-API-Key"),
		Headers:     flattenHeaders(c.GetReqHeaders()),
		SessionID:   sessionID,
		RemoteIP:    c.IP(),
		TLS:         c.Protocol() == "https",
	}
	principal, err := r.auth.Authenticate(ctx, creds)
	if err != nil {
		return realtime.Subject{}, err
	}
	if principal.ID == "" {
		return realtime.Subject{}, errUnauthenticated
	}
	return realtime.Subject{
		ID: principal.ID, Tenant: principal.TenantID, Roles: principal.Roles,
		Claims: map[string]any{"principal": principal},
	}, nil
}

// bind and install are called by Platform.Mount.
func (r *RealtimeHub) bind(p *Platform) { r.platform = p }

func (r *RealtimeHub) install(app *fh.App) error { return r.Hub.Mount(app, r.mount) }

// call runs a whitelisted intent on behalf of a connected client.
func (r *RealtimeHub) call(ctx context.Context, c *realtime.Client, name string, payload json.RawMessage) (any, error) {
	if !r.callable[name] {
		return nil, fmt.Errorf("%q cannot be called over this connection", name)
	}
	if r.platform == nil || r.platform.Engine == nil {
		return nil, fmt.Errorf("the platform is not ready")
	}
	principal, _ := c.Subject.Claims["principal"].(Principal)
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	inv := &invocation.Invocation{
		ID:        invocation.ID("ws-" + c.ID + "-" + fmt.Sprint(time.Now().UnixNano())),
		Intent:    invocation.IntentID(name),
		Identity:  verifiedFromPrincipal(principal, principal.TenantID),
		Input:     invocation.NewInput(payload, "application/json"),
		Principal: invocation.NewPrincipalHint("", "", nil, ""),
		Transport: invocation.Transport{Protocol: "ws"},
		Received:  time.Now(),
	}
	result, err := r.platform.Engine.Dispatch(ctx, inv)
	if err != nil {
		_, body := failureView(err)
		if e, ok := body["message"].(string); ok {
			return nil, fmt.Errorf("%s", e)
		}
		return nil, fmt.Errorf("the call failed")
	}
	defer runtime.ReleaseDispatchResult(result)
	return result.Value, nil
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

func requireRealtimeHub(build BuildContext, spec NodeSpec) (*RealtimeHub, error) {
	if spec.Resource == "" {
		return nil, fmt.Errorf("node %q: a realtime.hub resource is required", spec.Name)
	}
	found, ok := build.Resource(spec.Resource)
	if !ok {
		return nil, fmt.Errorf("node %q: resource %q is not declared", spec.Name, spec.Resource)
	}
	hub, ok := found.(*RealtimeHub)
	if !ok {
		return nil, fmt.Errorf("node %q: resource %q is not a realtime.hub", spec.Name, spec.Resource)
	}
	return hub, nil
}

func registerRealtimeActions(r *Registry) {
	mustAction(r, "realtime.publish", realtimePublishAction, ActionInfo{
		Family: "realtime", Summary: "Publish an event to a topic, or to one user's devices. The caller must be allowed to publish to the topic unless as_server is set.",
		ResourceKind: "realtime.hub", Kind: "effect", Provides: "{id}",
		Config: []ConfigField{
			{Name: "topic", Type: "string", Summary: "Fixed topic"},
			{Name: "topic_fact", Type: "fact", Summary: "Topic read from a fact"},
			{Name: "user_fact", Type: "fact", Summary: "Send to this user's private topic instead of a topic"},
			{Name: "event", Type: "string", Required: true},
			{Name: "data_fact", Type: "fact", Summary: "Payload read from a fact"},
			{Name: "as_server", Type: "bool", Default: "false", Summary: "Skip the caller's topic permissions. Without it, a topic is published to only if the signed-in caller may publish there."},
		},
	})
	mustAction(r, "realtime.presence", realtimePresenceAction, ActionInfo{
		Family: "realtime", Summary: "Who is present in a topic across all nodes",
		ResourceKind: "realtime.hub", Kind: "read", Provides: "{users, count}",
		Config: []ConfigField{
			{Name: "topic", Type: "string"}, {Name: "topic_fact", Type: "fact"},
			{Name: "as_server", Type: "bool", Default: "false", Summary: "Skip the caller's topic permissions. Without it, the caller must be allowed to subscribe to the topic."},
		},
	})
	mustAction(r, "realtime.stats", realtimeStatsAction, ActionInfo{
		Family: "realtime", Summary: "Connection and delivery counters of this node",
		ResourceKind: "realtime.hub", Kind: "read", Provides: "{clients, topics, published, delivered, dropped, remote}",
	})
}

// realtimeTarget resolves the topic an action works on.
func realtimeTarget(spec NodeSpec, allowUser bool) (fixed, topicFact, userFact string, err error) {
	fixed = configString(spec.Config, "topic", "")
	topicFact = configString(spec.Config, "topic_fact", "")
	if allowUser {
		userFact = configString(spec.Config, "user_fact", "")
	}
	n := 0
	for _, v := range []string{fixed, topicFact, userFact} {
		if v != "" {
			n++
		}
	}
	if n != 1 {
		want := "topic or topic_fact"
		if allowUser {
			want += " or user_fact"
		}
		return "", "", "", fmt.Errorf("node %q: set exactly one of %s", spec.Name, want)
	}
	return
}

// callerMayUse checks that the signed-in caller could themselves perform action
// on topic. An intent that takes a topic from user input would otherwise let
// any user publish to, or list the members of, a topic they cannot join.
func callerMayUse(hub *RealtimeHub, ctx *ActionContext, topic string, action realtime.Action) error {
	subject := realtime.Subject{ID: ctx.Principal.ID, Tenant: ctx.TenantID, Roles: ctx.Principal.Roles}
	if err := hub.Hub.Authorize(subject, topic, action); err != nil {
		return permissionDenied("you may not use that topic")
	}
	return nil
}

var realtimePublishAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	hub, err := requireRealtimeHub(build, spec)
	if err != nil {
		return nil, err
	}
	fixed, topicFact, userFact, err := realtimeTarget(spec, true)
	if err != nil {
		return nil, err
	}
	event, err := requiredString(spec.Config, "event")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	dataFact := configString(spec.Config, "data_fact", "")
	asServer := configBool(spec.Config, "as_server", false)
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		var data json.RawMessage
		if dataFact != "" {
			value, found := resolvePath(ctx.Inputs, dataFact)
			if !found {
				return ActionResult{}, invalidInput("required value %q is missing", dataFact)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return ActionResult{}, invalidInput("the value at %q cannot be sent: %v", dataFact, err)
			}
			data = encoded
		}
		var msg realtime.Message
		var err error
		switch {
		case userFact != "":
			user, ferr := factString(ctx.Inputs, userFact)
			if ferr != nil {
				return ActionResult{}, invalidInput("%v", ferr)
			}
			msg, err = hub.Hub.SendToUser(ctx.Context, user, event, data, ctx.Principal.ID)
		default:
			topic := fixed
			if topicFact != "" {
				var ferr error
				if topic, ferr = factString(ctx.Inputs, topicFact); ferr != nil {
					return ActionResult{}, invalidInput("%v", ferr)
				}
			}
			if !asServer {
				if err := callerMayUse(hub, ctx, topic, realtime.ActionPublish); err != nil {
					return ActionResult{}, err
				}
			}
			msg, err = hub.Hub.Emit(ctx.Context, topic, event, data, ctx.Principal.ID)
		}
		if err != nil && msg.ID == "" {
			return ActionResult{}, invalidInput("realtime publish failed: %v", err)
		}
		// With a message id the local delivery happened; a backplane error only
		// means other nodes may have missed it, which the caller can see in logs.
		return acknowledgement(spec, map[string]any{"id": msg.ID}), nil
	}), nil
})

var realtimePresenceAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	hub, err := requireRealtimeHub(build, spec)
	if err != nil {
		return nil, err
	}
	fixed, topicFact, _, err := realtimeTarget(spec, false)
	if err != nil {
		return nil, err
	}
	asServer := configBool(spec.Config, "as_server", false)
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		topic := fixed
		if topicFact != "" {
			var ferr error
			if topic, ferr = factString(ctx.Inputs, topicFact); ferr != nil {
				return ActionResult{}, invalidInput("%v", ferr)
			}
		}
		if !asServer {
			if err := callerMayUse(hub, ctx, topic, realtime.ActionSubscribe); err != nil {
				return ActionResult{}, err
			}
		}
		present := hub.Hub.Presence(topic)
		users := make([]map[string]any, 0, len(present))
		for _, e := range present {
			users = append(users, map[string]any{"user": e.User, "meta": e.Meta})
		}
		return singleOutput(spec, map[string]any{"users": users, "count": len(users)}), nil
	}), nil
})

var realtimeStatsAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	hub, err := requireRealtimeHub(build, spec)
	if err != nil {
		return nil, err
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		s := hub.Hub.Stats()
		return singleOutput(spec, map[string]any{
			"clients": s.Clients, "topics": s.Topics, "published": s.Published,
			"delivered": s.Delivered, "dropped": s.Dropped, "remote": s.Remote,
		}), nil
	}), nil
})
