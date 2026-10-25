package realtime

import (
	"context"
	"encoding/json"
	"errors"
)

// Request is a client-to-server frame. The same JSON is used over WebSocket and
// over the SSE companion POST endpoint.
//
//	{"id":"1","op":"subscribe","topic":"room:7","track":true,"meta":{"name":"Ada"}}
//	{"id":"2","op":"publish","topic":"room:7","event":"chat","data":{"text":"hi"}}
//	{"op":"presence","topic":"room:7"}
//	{"op":"call","name":"chat.send","data":{...}}
type Request struct {
	ID    string          `json:"id,omitempty"`
	Op    string          `json:"op"`
	Topic string          `json:"topic,omitempty"`
	Event string          `json:"event,omitempty"`
	Name  string          `json:"name,omitempty"`
	Track bool            `json:"track,omitempty"`
	After string          `json:"after,omitempty"`
	Meta  map[string]any  `json:"meta,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// reply is the server's answer to one request.
type reply struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Code    string          `json:"code,omitempty"`
	Result  any             `json:"result,omitempty"`
	Topic   string          `json:"topic,omitempty"`
	Present []PresenceEntry `json:"present,omitempty"`
}

// HandleFrame executes one client frame and returns the reply to send. A reply
// is always returned; a malformed frame produces an error reply, never a
// panic or a dropped connection.
func (h *Hub) HandleFrame(ctx context.Context, c *Client, raw []byte) []byte {
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return encodeReply(reply{Type: "reply", Code: "bad_request", Error: "frame is not valid JSON"})
	}
	r := reply{Type: "reply", ID: req.ID, OK: true, Topic: req.Topic}
	var err error
	switch req.Op {
	case "ping":
		r.Type = "pong"
	case "subscribe":
		if err = h.Subscribe(c, req.Topic, req.Track, req.Meta); err == nil && req.After != "" {
			if !h.Resume(c, req.Topic, req.After) {
				r.Result = map[string]any{"resumed": false}
			} else {
				r.Result = map[string]any{"resumed": true}
			}
		}
		if err == nil && req.Track {
			r.Present = h.Presence(req.Topic)
		}
	case "unsubscribe":
		h.Unsubscribe(c, req.Topic)
	case "publish":
		var msg Message
		if msg, err = h.Publish(ctx, c.Subject, req.Topic, req.Event, req.Data); err == nil || msg.ID != "" {
			r.Result = map[string]any{"id": msg.ID}
		}
	case "presence":
		if err = h.Authorize(c.Subject, req.Topic, ActionSubscribe); err == nil {
			r.Present = h.Presence(req.Topic)
		}
	case "call":
		if h.opts.Call == nil {
			err = errors.New("calls are not enabled")
			r.Code = "unsupported"
		} else {
			r.Result, err = h.opts.Call(ctx, c, req.Name, req.Data)
		}
	default:
		err = errors.New("unknown op " + req.Op)
		r.Code = "bad_request"
	}
	if err != nil {
		r.OK = false
		r.Error = err.Error()
		if r.Code == "" {
			switch {
			case errors.Is(err, ErrForbidden):
				r.Code = "forbidden"
			case errors.Is(err, ErrBadTopic):
				r.Code = "bad_topic"
			default:
				r.Code = "error"
			}
		}
	}
	return encodeReply(r)
}

// Authorize exposes the hub's authorization decision to transports.
func (h *Hub) Authorize(s Subject, topic string, a Action) error {
	if err := validTopic(topic); err != nil {
		return err
	}
	if topic == "user:"+s.ID && s.ID != "" {
		return nil
	}
	return h.authorize(s, topic, a)
}

func encodeReply(r reply) []byte {
	b, err := json.Marshal(r)
	if err != nil {
		b = []byte(`{"type":"reply","ok":false,"code":"error","error":"unencodable result"}`)
	}
	return b
}
