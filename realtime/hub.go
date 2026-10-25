// Package realtime is REF's connection hub: topics, presence and fan-out for
// WebSocket and SSE clients, on one node or several.
//
// The hub knows nothing about HTTP. A transport (see ws.go and the platform's
// realtime.hub resource) creates a Client per connection, feeds it inbound
// frames through Hub.HandleFrame and drains Client.Out to the wire. Across
// nodes the hub speaks through a Backplane; with none configured it is a plain
// single-process hub with identical behaviour.
package realtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Errors returned by hub operations.
var (
	ErrForbidden = errors.New("realtime: not permitted")
	ErrClosed    = errors.New("realtime: hub closed")
	ErrBadTopic  = errors.New("realtime: invalid topic")
)

// Action is what a subject asks to do with a topic.
type Action string

const (
	ActionSubscribe Action = "subscribe"
	ActionPublish   Action = "publish"
)

// Subject is the authenticated identity behind a connection.
type Subject struct {
	ID     string
	Tenant string
	Roles  []string
	Claims map[string]any
}

// Message is one published event.
type Message struct {
	ID    string          `json:"id"`
	Topic string          `json:"topic"`
	Event string          `json:"event,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	From  string          `json:"from,omitempty"`
	At    time.Time       `json:"at"`
}

// PresenceEntry is one user present in a topic.
type PresenceEntry struct {
	Topic string         `json:"topic"`
	User  string         `json:"user"`
	Meta  map[string]any `json:"meta,omitempty"`
}

// PresenceEvent describes a presence change, or (Action "sync") a node's whole
// presence table.
type PresenceEvent struct {
	Action  string          `json:"action"` // join, leave, sync
	Topic   string          `json:"topic,omitempty"`
	User    string          `json:"user,omitempty"`
	Meta    map[string]any  `json:"meta,omitempty"`
	Entries []PresenceEntry `json:"entries,omitempty"`
}

// Options configure a Hub. The zero value is a usable single-node hub.
type Options struct {
	// NodeID identifies this node on the backplane. Random when empty.
	NodeID string
	// Backplane links this hub to other nodes. Nil means single node.
	Backplane Backplane
	// SendBuffer is the per-connection outbound queue. A connection that falls
	// this far behind is dropped rather than allowed to stall the topic.
	SendBuffer int
	// History is how many recent messages each topic keeps for resume.
	History int
	// PresenceTTL is how long a silent node's presence is believed.
	PresenceTTL time.Duration
	// Authorize decides whether a subject may use a topic. Nil selects
	// DefaultAuthorize.
	Authorize func(Subject, string, Action) error
	// Call serves the "call" op, which lets a client invoke a named server-side
	// operation (the platform wires it to intent dispatch). Nil rejects it.
	Call func(ctx context.Context, c *Client, name string, payload json.RawMessage) (any, error)
	// OnDrop is told about every connection dropped for being too slow.
	OnDrop func(*Client)
}

// Authorizer is consulted before Options.Authorize. It returns handled=false to
// leave the decision to the next authorizer, so a package that owns a topic
// namespace (chat owns "chat:") can govern it without the hub knowing why.
type Authorizer func(s Subject, topic string, a Action) (handled bool, err error)

// DefaultAuthorize admits any identified subject to any topic except another
// user's private "user:<id>" topic. Anonymous subjects get nothing.
func DefaultAuthorize(s Subject, topic string, _ Action) error {
	if s.ID == "" {
		return ErrForbidden
	}
	if rest, ok := strings.CutPrefix(topic, "user:"); ok && rest != s.ID {
		return ErrForbidden
	}
	return nil
}

// Stats is a snapshot of hub counters.
type Stats struct {
	Clients   int    `json:"clients"`
	Topics    int    `json:"topics"`
	Published uint64 `json:"published"`
	Delivered uint64 `json:"delivered"`
	Dropped   uint64 `json:"dropped"`
	Remote    uint64 `json:"remote"`
}

// Hub routes messages between clients.
type Hub struct {
	opts Options
	node string

	amu         sync.RWMutex
	authorizers []Authorizer

	mu      sync.RWMutex
	closed  bool
	clients map[string]*Client
	topics  map[string]map[*Client]struct{}
	history map[string]*ring

	pmu      sync.Mutex
	local    map[string]map[string]map[string]map[string]any // topic -> user -> conn -> meta
	remote   map[string]*remoteNode
	seq      atomic.Uint64
	pub, del atomic.Uint64
	drop, rx atomic.Uint64
	stop     chan struct{}
	wg       sync.WaitGroup
}

type remoteNode struct {
	seen    time.Time
	entries map[string]map[string]map[string]any // topic -> user -> meta
}

// New starts a hub. Call Close to stop it.
func New(opts Options) (*Hub, error) {
	if opts.NodeID == "" {
		opts.NodeID = randomID(6)
	}
	if opts.SendBuffer <= 0 {
		opts.SendBuffer = 256
	}
	if opts.History < 0 {
		opts.History = 0
	} else if opts.History == 0 {
		opts.History = 100
	}
	if opts.PresenceTTL <= 0 {
		opts.PresenceTTL = 30 * time.Second
	}
	if opts.Authorize == nil {
		opts.Authorize = DefaultAuthorize
	}
	h := &Hub{
		opts:    opts,
		node:    opts.NodeID,
		clients: map[string]*Client{},
		topics:  map[string]map[*Client]struct{}{},
		history: map[string]*ring{},
		local:   map[string]map[string]map[string]map[string]any{},
		remote:  map[string]*remoteNode{},
		stop:    make(chan struct{}),
	}
	if opts.Backplane != nil {
		if err := opts.Backplane.Subscribe(h.fromRemote); err != nil {
			return nil, fmt.Errorf("realtime: backplane: %w", err)
		}
		h.wg.Add(1)
		go h.presenceLoop()
	}
	return h, nil
}

// AddAuthorizer registers a namespace authorizer. Authorizers run in the order
// added, ahead of Options.Authorize.
func (h *Hub) AddAuthorizer(a Authorizer) {
	h.amu.Lock()
	h.authorizers = append(h.authorizers, a)
	h.amu.Unlock()
}

func (h *Hub) authorize(s Subject, topic string, a Action) error {
	h.amu.RLock()
	list := h.authorizers
	h.amu.RUnlock()
	for _, f := range list {
		if handled, err := f(s, topic, a); handled {
			return err
		}
	}
	return h.opts.Authorize(s, topic, a)
}

// Node returns this hub's node id.
func (h *Hub) Node() string { return h.node }

// Close disconnects every client and detaches from the backplane.
func (h *Hub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	clients := make([]*Client, 0, len(h.clients))
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	close(h.stop)
	for _, c := range clients {
		c.Close()
	}
	h.wg.Wait()
	if h.opts.Backplane != nil {
		return h.opts.Backplane.Close()
	}
	return nil
}

// Stats returns a snapshot of the counters.
func (h *Hub) Stats() Stats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return Stats{
		Clients: len(h.clients), Topics: len(h.topics),
		Published: h.pub.Load(), Delivered: h.del.Load(), Dropped: h.drop.Load(), Remote: h.rx.Load(),
	}
}

// Client is one connection. The transport reads Out and writes each frame to
// the wire; it calls Close when the wire goes away.
type Client struct {
	ID      string
	Subject Subject
	hub     *Hub
	out     chan []byte
	once    sync.Once
	done    chan struct{}
	omu     sync.RWMutex // guards closing out against a concurrent send
	shut    bool
	tmu     sync.Mutex
	topics  map[string]bool // topic -> tracked for presence
}

// Out is the outbound frame queue; it is closed when the client is closed.
func (c *Client) Out() <-chan []byte { return c.out }

// Done is closed when the client is closed.
func (c *Client) Done() <-chan struct{} { return c.done }

// Connect registers a connection. Every identified subject is subscribed to
// its private "user:<id>" topic, which is how a server sends to one user.
func (h *Hub) Connect(s Subject) (*Client, error) {
	// The subject may be built from request data that is only valid for the
	// request; the client outlives it.
	s.ID, s.Tenant = strings.Clone(s.ID), strings.Clone(s.Tenant)
	s.Roles = cloneStrings(s.Roles)
	c := &Client{
		ID: randomID(8), Subject: s, hub: h,
		out: make(chan []byte, h.opts.SendBuffer), done: make(chan struct{}),
		topics: map[string]bool{},
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	h.clients[c.ID] = c
	h.mu.Unlock()
	if s.ID != "" {
		_ = h.Subscribe(c, "user:"+s.ID, false, nil)
	}
	return c, nil
}

// Close removes the client from every topic and ends its outbound stream.
func (c *Client) Close() {
	c.once.Do(func() {
		h := c.hub
		c.tmu.Lock()
		topics := make(map[string]bool, len(c.topics))
		for t, tr := range c.topics {
			topics[t] = tr
		}
		c.topics = map[string]bool{}
		c.tmu.Unlock()

		h.mu.Lock()
		delete(h.clients, c.ID)
		for t := range topics {
			if set := h.topics[t]; set != nil {
				delete(set, c)
				if len(set) == 0 {
					delete(h.topics, t)
				}
			}
		}
		h.mu.Unlock()
		for t, tracked := range topics {
			if tracked {
				h.untrack(t, c)
			}
		}
		close(c.done)
		c.omu.Lock()
		c.shut = true
		close(c.out)
		c.omu.Unlock()
	})
}

// enqueue offers a frame to the client, dropping the client if it is full.
func (c *Client) enqueue(frame []byte) bool {
	c.omu.RLock()
	defer c.omu.RUnlock()
	if c.shut {
		return false
	}
	select {
	case c.out <- frame:
		return true
	default:
		c.hub.drop.Add(1)
		if c.hub.opts.OnDrop != nil {
			c.hub.opts.OnDrop(c)
		}
		go c.Close()
		return false
	}
}

// Subscribe adds c to topic. With track set the user also appears in the
// topic's presence list, carrying meta.
func (h *Hub) Subscribe(c *Client, topic string, track bool, meta map[string]any) error {
	if err := validTopic(topic); err != nil {
		return err
	}
	if !strings.HasPrefix(topic, "user:") || topic != "user:"+c.Subject.ID {
		if err := h.authorize(c.Subject, topic, ActionSubscribe); err != nil {
			return err
		}
	}
	c.tmu.Lock()
	prev, had := c.topics[topic]
	if had && prev == track {
		c.tmu.Unlock()
		return nil
	}
	c.topics[topic] = track
	c.tmu.Unlock()

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ErrClosed
	}
	set := h.topics[topic]
	if set == nil {
		set = map[*Client]struct{}{}
		h.topics[topic] = set
	}
	set[c] = struct{}{}
	h.mu.Unlock()
	if track {
		h.track(topic, c, meta)
	} else if had && prev {
		h.untrack(topic, c)
	}
	return nil
}

// Unsubscribe removes c from topic.
func (h *Hub) Unsubscribe(c *Client, topic string) {
	c.tmu.Lock()
	tracked, had := c.topics[topic]
	delete(c.topics, topic)
	c.tmu.Unlock()
	if !had {
		return
	}
	h.mu.Lock()
	if set := h.topics[topic]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(h.topics, topic)
		}
	}
	h.mu.Unlock()
	if tracked {
		h.untrack(topic, c)
	}
}

// Publish sends a message as subject. The subject must be allowed to publish
// to the topic.
func (h *Hub) Publish(ctx context.Context, from Subject, topic, event string, data json.RawMessage) (Message, error) {
	if err := validTopic(topic); err != nil {
		return Message{}, err
	}
	if !(topic == "user:"+from.ID && from.ID != "") {
		if err := h.authorize(from, topic, ActionPublish); err != nil {
			return Message{}, err
		}
	}
	return h.Emit(ctx, topic, event, data, from.ID)
}

// Emit publishes on the server's own authority: no authorization check. Use it
// from trusted code such as a node in an intent.
func (h *Hub) Emit(ctx context.Context, topic, event string, data json.RawMessage, from string) (Message, error) {
	if err := validTopic(topic); err != nil {
		return Message{}, err
	}
	h.mu.RLock()
	closed := h.closed
	h.mu.RUnlock()
	if closed {
		return Message{}, ErrClosed
	}
	msg := Message{
		ID:    fmt.Sprintf("%s-%d", h.node, h.seq.Add(1)),
		Topic: topic, Event: event, Data: data, From: from, At: time.Now().UTC(),
	}
	h.pub.Add(1)
	h.deliver(msg)
	if bp := h.opts.Backplane; bp != nil {
		if err := bp.Publish(ctx, Envelope{Node: h.node, Kind: "message", Message: &msg}); err != nil {
			return msg, fmt.Errorf("realtime: delivered locally, backplane failed: %w", err)
		}
	}
	return msg, nil
}

// SendToUser publishes to a user's private topic, reaching every device of
// that user on every node.
func (h *Hub) SendToUser(ctx context.Context, user, event string, data json.RawMessage, from string) (Message, error) {
	return h.Emit(ctx, "user:"+user, event, data, from)
}

type messageFrame struct {
	Type string `json:"type"`
	Message
}

func (h *Hub) deliver(msg Message) {
	frame, err := json.Marshal(messageFrame{Type: "message", Message: msg})
	if err != nil {
		return
	}
	h.mu.Lock()
	if h.opts.History > 0 {
		r := h.history[msg.Topic]
		if r == nil {
			r = newRing(h.opts.History)
			h.history[msg.Topic] = r
		}
		r.push(msg)
	}
	set := h.topics[msg.Topic]
	targets := make([]*Client, 0, len(set))
	for c := range set {
		targets = append(targets, c)
	}
	h.mu.Unlock()
	for _, c := range targets {
		if c.enqueue(frame) {
			h.del.Add(1)
		}
	}
}

// History returns up to n recent messages of topic, oldest first. When after is
// the id of a message still held, only newer messages are returned; ok reports
// whether after was found (false means the client missed more than is kept).
func (h *Hub) History(topic string, n int, after string) (msgs []Message, ok bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	r := h.history[topic]
	if r == nil {
		return nil, after == ""
	}
	return r.since(after, n)
}

// Resume replays to c the messages of its topics that it missed after the given
// message id. It reports false when the id is no longer in history.
func (h *Hub) Resume(c *Client, topic, after string) bool {
	msgs, ok := h.History(topic, 0, after)
	for _, m := range msgs {
		frame, err := json.Marshal(messageFrame{Type: "message", Message: m})
		if err == nil {
			c.enqueue(frame)
		}
	}
	return ok
}

// Topics returns the sorted topics that currently have a subscriber on this node.
func (h *Hub) Topics() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.topics))
	for t := range h.topics {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Presence
// ---------------------------------------------------------------------------

func (h *Hub) track(topic string, c *Client, meta map[string]any) {
	user := c.Subject.ID
	if user == "" {
		return
	}
	h.pmu.Lock()
	users := h.local[topic]
	if users == nil {
		users = map[string]map[string]map[string]any{}
		h.local[topic] = users
	}
	conns := users[user]
	first := len(conns) == 0
	if conns == nil {
		conns = map[string]map[string]any{}
		users[user] = conns
	}
	conns[c.ID] = meta
	h.pmu.Unlock()
	if first {
		h.announce(PresenceEvent{Action: "join", Topic: topic, User: user, Meta: meta})
	}
}

func (h *Hub) untrack(topic string, c *Client) {
	user := c.Subject.ID
	h.pmu.Lock()
	users := h.local[topic]
	conns := users[user]
	if _, ok := conns[c.ID]; !ok {
		h.pmu.Unlock()
		return
	}
	delete(conns, c.ID)
	last := len(conns) == 0
	if last {
		delete(users, user)
		if len(users) == 0 {
			delete(h.local, topic)
		}
	}
	h.pmu.Unlock()
	if last {
		h.announce(PresenceEvent{Action: "leave", Topic: topic, User: user})
	}
}

// announce shows a presence change to local subscribers and other nodes.
func (h *Hub) announce(ev PresenceEvent) {
	h.deliverPresence(ev)
	if bp := h.opts.Backplane; bp != nil {
		_ = bp.Publish(context.Background(), Envelope{Node: h.node, Kind: "presence", Presence: &ev})
	}
}

type presenceFrame struct {
	Type   string         `json:"type"`
	Action string         `json:"action"`
	Topic  string         `json:"topic"`
	User   string         `json:"user"`
	Meta   map[string]any `json:"meta,omitempty"`
}

func (h *Hub) deliverPresence(ev PresenceEvent) {
	frame, err := json.Marshal(presenceFrame{Type: "presence", Action: ev.Action, Topic: ev.Topic, User: ev.User, Meta: ev.Meta})
	if err != nil {
		return
	}
	h.mu.RLock()
	set := h.topics[ev.Topic]
	targets := make([]*Client, 0, len(set))
	for c := range set {
		targets = append(targets, c)
	}
	h.mu.RUnlock()
	for _, c := range targets {
		c.enqueue(frame)
	}
}

// Presence lists who is present in topic across all nodes, sorted by user.
func (h *Hub) Presence(topic string) []PresenceEntry {
	h.pmu.Lock()
	defer h.pmu.Unlock()
	merged := map[string]map[string]any{}
	for user, conns := range h.local[topic] {
		for _, meta := range conns {
			merged[user] = meta
			break
		}
	}
	for _, n := range h.remote {
		for user, meta := range n.entries[topic] {
			if _, ok := merged[user]; !ok {
				merged[user] = meta
			}
		}
	}
	out := make([]PresenceEntry, 0, len(merged))
	for user, meta := range merged {
		out = append(out, PresenceEntry{Topic: topic, User: user, Meta: meta})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User < out[j].User })
	return out
}

// Online reports whether user is present in topic on any node.
func (h *Hub) Online(topic, user string) bool {
	for _, e := range h.Presence(topic) {
		if e.User == user {
			return true
		}
	}
	return false
}

func (h *Hub) snapshot() []PresenceEntry {
	h.pmu.Lock()
	defer h.pmu.Unlock()
	var out []PresenceEntry
	for topic, users := range h.local {
		for user, conns := range users {
			for _, meta := range conns {
				out = append(out, PresenceEntry{Topic: topic, User: user, Meta: meta})
				break
			}
		}
	}
	return out
}

// presenceLoop re-announces this node's presence so that a node that joined
// late, or missed an event, converges, and expires silent nodes.
func (h *Hub) presenceLoop() {
	defer h.wg.Done()
	t := time.NewTicker(h.opts.PresenceTTL / 3)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			h.sendSync()
			h.expireNodes()
		}
	}
}

func (h *Hub) sendSync() {
	if bp := h.opts.Backplane; bp != nil {
		_ = bp.Publish(context.Background(), Envelope{Node: h.node, Kind: "presence",
			Presence: &PresenceEvent{Action: "sync", Entries: h.snapshot()}})
	}
}

func (h *Hub) expireNodes() {
	now := time.Now()
	var gone []PresenceEvent
	h.pmu.Lock()
	for id, n := range h.remote {
		if now.Sub(n.seen) > h.opts.PresenceTTL {
			delete(h.remote, id)
			for topic, users := range n.entries {
				for user := range users {
					gone = append(gone, PresenceEvent{Action: "leave", Topic: topic, User: user})
				}
			}
		}
	}
	h.pmu.Unlock()
	for _, ev := range gone {
		if !h.Online(ev.Topic, ev.User) {
			h.deliverPresence(ev)
		}
	}
}

// fromRemote handles an envelope from another node.
func (h *Hub) fromRemote(env Envelope) {
	if env.Node == h.node {
		return
	}
	h.rx.Add(1)
	switch env.Kind {
	case "message":
		if env.Message != nil {
			h.deliver(*env.Message)
		}
	case "presence":
		if env.Presence != nil {
			h.remotePresence(env.Node, *env.Presence)
		}
	}
}

func (h *Hub) remotePresence(node string, ev PresenceEvent) {
	var changes []PresenceEvent
	h.pmu.Lock()
	n := h.remote[node]
	if n == nil {
		n = &remoteNode{entries: map[string]map[string]map[string]any{}}
		h.remote[node] = n
		// A node we have not heard from has just appeared: tell it what we hold.
		defer h.sendSync()
	}
	n.seen = time.Now()
	set := func(topic, user string, meta map[string]any) {
		if n.entries[topic] == nil {
			n.entries[topic] = map[string]map[string]any{}
		}
		if _, had := n.entries[topic][user]; !had {
			changes = append(changes, PresenceEvent{Action: "join", Topic: topic, User: user, Meta: meta})
		}
		n.entries[topic][user] = meta
	}
	switch ev.Action {
	case "join":
		set(ev.Topic, ev.User, ev.Meta)
	case "leave":
		if _, had := n.entries[ev.Topic][ev.User]; had {
			delete(n.entries[ev.Topic], ev.User)
			changes = append(changes, PresenceEvent{Action: "leave", Topic: ev.Topic, User: ev.User})
		}
	case "sync":
		seen := map[[2]string]bool{}
		for _, e := range ev.Entries {
			seen[[2]string{e.Topic, e.User}] = true
			set(e.Topic, e.User, e.Meta)
		}
		for topic, users := range n.entries {
			for user := range users {
				if !seen[[2]string{topic, user}] {
					delete(users, user)
					changes = append(changes, PresenceEvent{Action: "leave", Topic: topic, User: user})
				}
			}
		}
	}
	h.pmu.Unlock()
	for _, c := range changes {
		// Only announce a change that alters who is present overall: a user
		// who is also connected here has not joined or left.
		if !h.localHas(c.Topic, c.User) {
			h.deliverPresence(c)
		}
	}
}

func (h *Hub) localHas(topic, user string) bool {
	h.pmu.Lock()
	defer h.pmu.Unlock()
	return len(h.local[topic][user]) > 0
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = strings.Clone(v)
	}
	return out
}

func validTopic(t string) error {
	if t == "" || len(t) > 200 {
		return ErrBadTopic
	}
	for _, r := range t {
		if r < 0x21 || r == 0x7f {
			return ErrBadTopic
		}
	}
	return nil
}

func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ring keeps the last n messages of a topic.
type ring struct {
	buf  []Message
	next int
	full bool
}

func newRing(n int) *ring { return &ring{buf: make([]Message, n)} }

func (r *ring) push(m Message) {
	r.buf[r.next] = m
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

func (r *ring) all() []Message {
	if !r.full {
		return append([]Message(nil), r.buf[:r.next]...)
	}
	return append(append([]Message(nil), r.buf[r.next:]...), r.buf[:r.next]...)
}

func (r *ring) since(after string, n int) ([]Message, bool) {
	all := r.all()
	ok := true
	if after != "" {
		ok = false
		for i, m := range all {
			if m.ID == after {
				all, ok = all[i+1:], true
				break
			}
		}
		if !ok {
			// The client's marker has aged out: everything held is newer.
			return trim(all, n), false
		}
	}
	return trim(all, n), ok
}

func trim(m []Message, n int) []Message {
	if n > 0 && len(m) > n {
		return m[len(m)-n:]
	}
	return m
}
