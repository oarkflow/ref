package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/oarkflow/ref/chat"
	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/realtime"
)

// chat.store: channels, DMs, history and receipts in a SQL database, announced
// over a realtime.hub.
//
//	resource "chat" {
//	  kind "chat.store"
//	  config { database "db" hub "live" }
//	}
//
// The resource also governs the hub's "chat:<channel>" topics: a user may
// subscribe only as a member (in the channel's tenant), and nobody may publish
// to one directly, so a message can only come from chat.send and carry the
// sender's real identity.

// ChatStore is the opened resource.
type ChatStore struct {
	Store *chat.Store
}

func registerChatResources(r *Registry) {
	mustResource(r, "chat.store", ResourceFactoryFunc(openChatStore), ResourceKindInfo{
		Family:  "messaging",
		Summary: "Channels, direct messages, history, read receipts and typing, stored in SQL and delivered over a realtime.hub.",
		Config: []ConfigField{
			{Name: "database", Type: "resource", Required: true, Summary: "database.sql resource that holds the chat tables"},
			{Name: "hub", Type: "resource", Summary: "realtime.hub that delivers events; without one messages are stored but not pushed"},
		},
	})
}

func openChatStore(ctx context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("chat.store", spec.Config, "database", "hub"); err != nil {
		return nil, nil, err
	}
	dbName, err := requiredString(spec.Config, "database")
	if err != nil {
		return nil, nil, fmt.Errorf("chat.store %q: %w", spec.Name, err)
	}
	db, ok := spec.resolved[dbName].(*Database)
	if !ok {
		return nil, nil, fmt.Errorf("chat.store %q: %q is not a database.sql resource", spec.Name, dbName)
	}
	opts := chat.Options{Dialect: db.Dialect, Rebind: func(q string) string { return rebind(db.Dialect, q) }}

	var hub *realtime.Hub
	if name := configString(spec.Config, "hub", ""); name != "" {
		h, ok := spec.resolved[name].(*RealtimeHub)
		if !ok {
			return nil, nil, fmt.Errorf("chat.store %q: %q is not a realtime.hub resource", spec.Name, name)
		}
		hub = h.Hub
		opts.Notifier = chat.NotifierFunc(func(ctx context.Context, topic, event string, data json.RawMessage, from string) error {
			_, err := hub.Emit(ctx, topic, event, data, from)
			return err
		})
	}
	store, err := chat.Open(ctx, db.DB, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("chat.store %q: %w", spec.Name, err)
	}
	if hub != nil {
		hub.AddAuthorizer(func(s realtime.Subject, topic string, a realtime.Action) (bool, error) {
			channel, ok := chat.ChannelOfTopic(topic)
			if !ok {
				return false, nil
			}
			if a == realtime.ActionPublish {
				return true, realtime.ErrForbidden
			}
			lookup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if store.IsMember(lookup, chat.Actor{ID: s.ID, Tenant: s.Tenant}, channel) {
				return true, nil
			}
			return true, realtime.ErrForbidden
		})
	}
	return &ChatStore{Store: store}, nil, nil
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

// chatArgs reads an action's arguments: each is either a literal in the node's
// config ("channel") or the name of a fact holding it ("channel_fact").
type chatArgs struct {
	ctx    *ActionContext
	config map[string]any
}

func (a chatArgs) str(key string) (string, error) {
	if fact := configString(a.config, key+"_fact", ""); fact != "" {
		return factString(a.ctx.Inputs, fact)
	}
	if v := configString(a.config, key, ""); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%s is required (set %s or %s_fact)", key, key, key)
}

func (a chatArgs) optStr(key string) string {
	s, _ := a.str(key)
	return s
}

func (a chatArgs) int64(key string) (int64, error) {
	s := a.optStr(key)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number", key)
	}
	return n, nil
}

func (a chatArgs) list(key string) []string {
	if fact := configString(a.config, key+"_fact", ""); fact != "" {
		v, _ := resolvePath(a.ctx.Inputs, fact)
		return stringSlice(v)
	}
	return configStrings(a.config, key)
}

func (a chatArgs) raw(key string) json.RawMessage {
	fact := configString(a.config, key+"_fact", "")
	if fact == "" {
		return nil
	}
	v, ok := resolvePath(a.ctx.Inputs, fact)
	if !ok || v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return b
}

type chatOp struct {
	name, summary, kind, provides string
	fields                        []string
	run                           func(ctx context.Context, st *chat.Store, by chat.Actor, a chatArgs) (any, error)
}

func chatFailure(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, chat.ErrForbidden):
		return permissionDenied("you are not a member of that conversation")
	case errors.Is(err, chat.ErrNotFound):
		return notFoundOrMessage("that conversation does not exist")
	case errors.Is(err, chat.ErrInvalid):
		return invalidInput("%v", err)
	default:
		return unavailable("chat is unavailable: %v", err)
	}
}

var chatOps = []chatOp{
	{name: "chat.channel_create", summary: "Create a group channel; the caller owns it", kind: "effect", provides: "the channel",
		fields: []string{"name, name_fact", "members_fact (list of user ids)"},
		run: func(ctx context.Context, st *chat.Store, by chat.Actor, a chatArgs) (any, error) {
			name, err := a.str("name")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			return st.CreateChannel(ctx, by, name, a.list("members"))
		}},
	{name: "chat.dm", summary: "Open (or create) the direct conversation with a user", kind: "effect", provides: "the channel",
		fields: []string{"user, user_fact"},
		run: func(ctx context.Context, st *chat.Store, by chat.Actor, a chatArgs) (any, error) {
			user, err := a.str("user")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			return st.OpenDM(ctx, by, user)
		}},
	{name: "chat.send", summary: "Store a message and push it to the channel", kind: "effect", provides: "the message",
		fields: []string{"channel, channel_fact", "body, body_fact", "meta_fact"},
		run: func(ctx context.Context, st *chat.Store, by chat.Actor, a chatArgs) (any, error) {
			channel, err := a.str("channel")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			body, err := a.str("body")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			return st.Send(ctx, by, channel, body, a.raw("meta"))
		}},
	{name: "chat.history", summary: "Messages of a channel, oldest first, paged backwards with before_seq", kind: "read", provides: "{messages}",
		fields: []string{"channel, channel_fact", "before_seq_fact", "limit_fact"},
		run: func(ctx context.Context, st *chat.Store, by chat.Actor, a chatArgs) (any, error) {
			channel, err := a.str("channel")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			before, err := a.int64("before_seq")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			limit, err := a.int64("limit")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			msgs, err := st.History(ctx, by, channel, before, int(limit))
			return map[string]any{"messages": nilToEmpty(msgs)}, err
		}},
	{name: "chat.since", summary: "Messages after a sequence number: what a reconnecting client missed", kind: "read", provides: "{messages}",
		fields: []string{"channel, channel_fact", "after_seq_fact"},
		run: func(ctx context.Context, st *chat.Store, by chat.Actor, a chatArgs) (any, error) {
			channel, err := a.str("channel")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			after, err := a.int64("after_seq")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			msgs, err := st.Since(ctx, by, channel, after, 0)
			return map[string]any{"messages": nilToEmpty(msgs)}, err
		}},
	{name: "chat.channels", summary: "The caller's channels with unread counts", kind: "read", provides: "{channels}",
		run: func(ctx context.Context, st *chat.Store, by chat.Actor, a chatArgs) (any, error) {
			list, err := st.Channels(ctx, by)
			return map[string]any{"channels": nilToEmpty(list)}, err
		}},
	{name: "chat.members", summary: "Members of a channel", kind: "read", provides: "{members}",
		fields: []string{"channel, channel_fact"},
		run: func(ctx context.Context, st *chat.Store, by chat.Actor, a chatArgs) (any, error) {
			channel, err := a.str("channel")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			m, err := st.Members(ctx, by, channel)
			return map[string]any{"members": nilToEmpty(m)}, err
		}},
	{name: "chat.add_member", summary: "Add a user to a channel (owner only)", kind: "effect",
		fields: []string{"channel, channel_fact", "user, user_fact"},
		run: func(ctx context.Context, st *chat.Store, by chat.Actor, a chatArgs) (any, error) {
			channel, err := a.str("channel")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			user, err := a.str("user")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			return true, st.AddMember(ctx, by, channel, user)
		}},
	{name: "chat.remove_member", summary: "Remove a user from a channel (owner, or yourself)", kind: "effect",
		fields: []string{"channel, channel_fact", "user, user_fact"},
		run: func(ctx context.Context, st *chat.Store, by chat.Actor, a chatArgs) (any, error) {
			channel, err := a.str("channel")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			user, err := a.str("user")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			return true, st.RemoveMember(ctx, by, channel, user)
		}},
	{name: "chat.mark_read", summary: "Record a read receipt up to a sequence number (all when omitted)", kind: "effect",
		fields: []string{"channel, channel_fact", "seq_fact"},
		run: func(ctx context.Context, st *chat.Store, by chat.Actor, a chatArgs) (any, error) {
			channel, err := a.str("channel")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			seq, err := a.int64("seq")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			return true, st.MarkRead(ctx, by, channel, seq)
		}},
	{name: "chat.receipts", summary: "Each member's read position in a channel", kind: "read", provides: "{receipts}",
		fields: []string{"channel, channel_fact"},
		run: func(ctx context.Context, st *chat.Store, by chat.Actor, a chatArgs) (any, error) {
			channel, err := a.str("channel")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			r, err := st.Receipts(ctx, by, channel)
			return map[string]any{"receipts": r}, err
		}},
	{name: "chat.typing", summary: "Tell a channel the caller is typing (not stored)", kind: "effect",
		fields: []string{"channel, channel_fact"},
		run: func(ctx context.Context, st *chat.Store, by chat.Actor, a chatArgs) (any, error) {
			channel, err := a.str("channel")
			if err != nil {
				return nil, invalidInput("%v", err)
			}
			return true, st.Typing(ctx, by, channel)
		}},
}

func nilToEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func registerChatActions(r *Registry) {
	for _, op := range chatOps {
		op := op
		cfg := make([]ConfigField, 0, len(op.fields))
		for _, f := range op.fields {
			cfg = append(cfg, ConfigField{Name: f, Type: "string"})
		}
		mustAction(r, op.name, ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
			res, ok := build.Resource(spec.Resource)
			if !ok {
				return nil, fmt.Errorf("node %q: resource %q is not declared", spec.Name, spec.Resource)
			}
			cs, ok := res.(*ChatStore)
			if !ok {
				return nil, fmt.Errorf("node %q: resource %q is not a chat.store", spec.Name, spec.Resource)
			}
			return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
				if ctx.Principal.ID == "" {
					return ActionResult{}, permissionDenied("chat needs a signed-in user")
				}
				by := chat.Actor{ID: ctx.Principal.ID, Tenant: ctx.TenantID}
				out, err := op.run(ctx.Context, cs.Store, by, chatArgs{ctx: ctx, config: spec.Config})
				if err != nil {
					var failure intent.Failure
					if errors.As(err, &failure) {
						return ActionResult{}, err
					}
					return ActionResult{}, chatFailure(err)
				}
				return acknowledgement(spec, out), nil
			}), nil
		}), ActionInfo{Family: "chat", Summary: op.summary, ResourceKind: "chat.store", Kind: op.kind, Provides: op.provides, Config: cfg})
	}
}
