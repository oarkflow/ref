package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/oarkflow/fh"
	ws "github.com/oarkflow/fh/pkg/websocket"
)

// MountConfig describes where and how a hub is exposed over HTTP.
type MountConfig struct {
	// WSPath is the WebSocket endpoint. Default /realtime/ws.
	WSPath string
	// SSEPath is the server-sent events endpoint. Default /realtime/events.
	SSEPath string
	// SendPath receives client frames for SSE clients. Default /realtime/send.
	SendPath string
	// Identify authenticates the request. It must return ErrForbidden-wrapped
	// or any error to refuse; the error text is not sent to the client.
	Identify func(fh.Ctx) (Subject, error)
	// AllowedOrigins lists browser origins permitted to open a WebSocket or
	// send frames, besides the server's own. "*" allows any (not advisable
	// with cookie authentication).
	AllowedOrigins []string
	// MaxMessage caps one client frame. Default 64 KiB.
	MaxMessage int64
	// Manager, when set, caps and tracks WebSocket connections (see
	// websocket.NewManager). Optional.
	Manager *ws.Manager
	// Heartbeat is the ping / keep-alive interval. Default 25s.
	Heartbeat time.Duration
}

func (m *MountConfig) defaults() {
	if m.WSPath == "" {
		m.WSPath = "/realtime/ws"
	}
	if m.SSEPath == "" {
		m.SSEPath = "/realtime/events"
	}
	if m.SendPath == "" {
		m.SendPath = "/realtime/send"
	}
	if m.MaxMessage <= 0 {
		m.MaxMessage = 64 << 10
	}
	if m.Heartbeat <= 0 {
		m.Heartbeat = 25 * time.Second
	}
}

// Client returns a connected client by id.
func (h *Hub) Client(id string) *Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.clients[id]
}

// Mount registers the WebSocket, SSE and send endpoints on app.
func (h *Hub) Mount(app *fh.App, cfg MountConfig) error {
	if cfg.Identify == nil {
		return errors.New("realtime: Mount needs an Identify function")
	}
	cfg.defaults()
	app.Get(cfg.WSPath, func(c fh.Ctx) error { return h.serveWS(c, cfg) })
	app.Get(cfg.SSEPath, func(c fh.Ctx) error { return h.serveSSE(c, cfg) })
	app.Post(cfg.SendPath, func(c fh.Ctx) error { return h.serveSend(c, cfg) })
	return nil
}

func fail(c fh.Ctx, status int, code string) error {
	return c.Status(status).JSON(map[string]any{"error": map[string]any{"code": code}})
}

func originAllowed(c fh.Ctx, cfg MountConfig) bool {
	origin := c.Get("Origin")
	if origin == "" {
		return true // not a browser; it has no ambient credentials to abuse
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, c.Hostname()) || strings.EqualFold(u.Host, c.Get("Host")) {
		return true
	}
	for _, a := range cfg.AllowedOrigins {
		if a == "*" || strings.EqualFold(a, origin) {
			return true
		}
	}
	return false
}

func (h *Hub) serveWS(c fh.Ctx, cfg MountConfig) error {
	// Authenticate before the upgrade, so a refused caller gets a plain 401.
	subject, err := cfg.Identify(c)
	if err != nil {
		return fail(c, 401, "unauthenticated")
	}
	client, err := h.Connect(subject)
	if err != nil {
		return fail(c, 503, "unavailable")
	}
	opened := false
	topics := splitTopics(c.Query("topics"))
	wcfg := ws.DefaultConfig()
	wcfg.MaxMessageSize = int(cfg.MaxMessage)
	wcfg.PingInterval = cfg.Heartbeat
	wcfg.PongTimeout = cfg.Heartbeat * 3
	wcfg.AllowedOrigins = cfg.AllowedOrigins
	wcfg.AllowAllOrigins = slices.Contains(cfg.AllowedOrigins, "*")
	wcfg.CheckOrigin = func(c fh.Ctx) bool { return originAllowed(c, cfg) }
	wcfg.Manager = cfg.Manager

	err = ws.NewWithConfig(wcfg, func(conn *ws.Conn) error {
		opened = true
		defer client.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		_ = conn.WriteText(string(mustJSON(map[string]any{"type": "ready", "client": client.ID, "node": h.node, "user": subject.ID})))
		for _, t := range topics {
			if err := h.Subscribe(client, t, false, nil); err != nil {
				_ = conn.WriteText(string(encodeReply(reply{Type: "reply", Code: "forbidden", Error: "cannot subscribe to " + t})))
			}
		}

		go func() { // outbound: the hub's queue to the wire
			for {
				select {
				case frame, ok := <-client.Out():
					if !ok {
						_ = conn.CloseWithStatus(ws.CloseGoingAway, "")
						return
					}
					if conn.WriteText(string(frame)) != nil {
						client.Close()
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()

		for { // inbound: client frames
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return nil
			}
			if conn.WriteText(string(h.HandleFrame(ctx, client, msg))) != nil {
				return nil
			}
		}
	})(c)
	if !opened {
		client.Close() // handshake refused (origin, headers): nothing took ownership
	}
	return err
}

func (h *Hub) serveSSE(c fh.Ctx, cfg MountConfig) error {
	subject, err := cfg.Identify(c)
	if err != nil {
		return fail(c, 401, "unauthenticated")
	}
	client, err := h.Connect(subject)
	if err != nil {
		return fail(c, 503, "unavailable")
	}
	topics := splitTopics(c.Query("topics"))
	after := c.LastEventID()
	return c.SSE(func(s *fh.SSE) error {
		defer client.Close()
		if err := s.WriteEvent(fh.SSEEvent{Event: "ready", Data: mustJSON(map[string]any{"client": client.ID, "node": h.node, "user": subject.ID})}); err != nil {
			return nil
		}
		for _, t := range topics {
			if err := h.Subscribe(client, t, false, nil); err != nil {
				_ = s.WriteEvent(fh.SSEEvent{Event: "error", Data: mustJSON(map[string]any{"code": "forbidden", "topic": t})})
				continue
			}
			if after != "" {
				h.Resume(client, t, after)
			}
		}
		tick := time.NewTicker(cfg.Heartbeat)
		defer tick.Stop()
		for {
			select {
			case frame, ok := <-client.Out():
				if !ok {
					return nil
				}
				var head struct {
					Type string `json:"type"`
					ID   string `json:"id"`
				}
				_ = json.Unmarshal(frame, &head)
				ev := fh.SSEEvent{Event: head.Type, Data: string(frame)}
				if head.Type == "message" {
					ev.ID = head.ID
				}
				if err := s.WriteEvent(ev); err != nil {
					return nil
				}
			case <-tick.C:
				if err := s.Ping(); err != nil {
					return nil
				}
			case <-s.Done():
				return nil
			}
		}
	})
}

// serveSend carries client frames for SSE clients, which cannot send on their
// stream. The caller must authenticate as the user who owns the stream.
func (h *Hub) serveSend(c fh.Ctx, cfg MountConfig) error {
	if !originAllowed(c, cfg) {
		return fail(c, 403, "origin_not_allowed")
	}
	if !strings.HasPrefix(strings.ToLower(c.Get("Content-Type")), "application/json") {
		return fail(c, 415, "json_required")
	}
	subject, err := cfg.Identify(c)
	if err != nil {
		return fail(c, 401, "unauthenticated")
	}
	client := h.Client(c.Query("client"))
	if client == nil || client.Subject.ID != subject.ID || subject.ID == "" {
		return fail(c, 404, "unknown_client")
	}
	body := bytes.Clone(c.Body()) // fh reuses its read buffer after the handler returns
	if int64(len(body)) > cfg.MaxMessage {
		return fail(c, 413, "too_large")
	}
	c.Set("Content-Type", "application/json")
	return c.Status(200).Send(h.HandleFrame(c.Context(), client, body))
}

func splitTopics(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			// fh's query strings are views of its read buffer, valid only during
			// the handler; a topic outlives it as a map key.
			out = append(out, strings.Clone(t))
		}
	}
	return out
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
