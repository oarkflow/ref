package sms

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/smppflow/pkg/routing"

	"github.com/oarkflow/ref/examples/smsgateway/internal/gateway"
)

// Queue is the queue the hub publishes to and consumes from. queue.broker
// satisfies it; so would any REF queue given broadcast support.
type Queue interface {
	Enqueue(jobType string, payload any, headers ...map[string]string) (string, error)
	EnqueueAfter(jobType string, payload any, delay time.Duration, headers ...map[string]string) (string, error)
	Register(jobType string, h fh.QueueHandler)
	RegisterBroadcast(jobType string, h fh.QueueHandler)
	Broadcast(jobType string, payload any, headers ...map[string]string) error
	Start() error
}

// Tunable is implemented by queues that can size the consumers of one job type.
type Tunable interface {
	Tune(jobType string, concurrency, maxAttempts int)
}

// IntentRunner runs a BCL intent with a payload. The hub drives its pipelines
// through it, so the pipeline stays whatever the BCL says it is.
type IntentRunner func(ctx context.Context, intent string, payload []byte, headers map[string]string) error

// Job types.
const (
	JobDispatchPrefix = "sms.dispatch."
	JobDLR            = "sms.dlr"
	JobConfigUser     = "sms.config.user"
	JobConfigProvider = "sms.config.provider"
	JobConfigAssign   = "sms.config.assignment"
	JobConfigRate     = "sms.config.rate"
)

// DispatchJob is the payload of a dispatch job. It carries identifiers only:
// the message in the database is the truth, and the sequence number identifies
// the one live job.
type DispatchJob struct {
	MessageID string `json:"message_id"`
	Provider  string `json:"provider"`
	Seq       int64  `json:"seq"`
}

// Provider is a running provider: its configuration, its gateway and its
// health.
type Provider struct {
	Name    string
	Kind    string
	Cfg     ProviderConfig
	GW      gateway.Gateway
	Dynamic bool
	// fingerprint identifies the stored record the provider was built from, so a
	// repeated configuration event does not rebuild an unchanged gateway.
	fingerprint string

	limiter *tokenBucket

	mu       sync.Mutex
	failures int
	lastErr  string
	lastOK   time.Time
	lastFail time.Time

	Sent, Failed, Throttled atomic.Int64
}

func (p *Provider) note(ok bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ok {
		p.failures, p.lastOK = 0, time.Now()
		return
	}
	p.failures++
	p.lastFail = time.Now()
	if err != nil {
		p.lastErr = err.Error()
	}
}

// Health is a provider's status for operators.
type Health struct {
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	Owner        string    `json:"owner,omitempty"`
	Dynamic      bool      `json:"dynamic"`
	Disabled     bool      `json:"disabled"`
	Countries    []string  `json:"countries"`
	Sent         int64     `json:"sent"`
	Failed       int64     `json:"failed"`
	Throttled    int64     `json:"throttled"`
	Consecutive  int       `json:"consecutive_failures"`
	LastError    string    `json:"last_error,omitempty"`
	LastSuccess  time.Time `json:"last_success,omitzero"`
	Quality      float64   `json:"quality"`
	DeliveryRate float64   `json:"delivery_rate"`
	QueueDLQ     int       `json:"dlq_depth"`
}

// Hub is the application's core: state, routing, the ledger, provider
// gateways and the queue consumers. The REF actions in actions.go are thin
// adapters over its methods.
type Hub struct {
	Name   string
	Cfg    HubConfig
	Store  *Store
	Dir    *Directory
	Router *Router
	Log    *slog.Logger

	queue Queue

	mu         sync.RWMutex
	providers  map[string]*Provider
	registered map[string]bool
	started    bool
	runner     IntentRunner

	limMu    sync.Mutex
	limiters map[string]*tokenBucket

	stop chan struct{}
	wg   sync.WaitGroup

	Counters Counters
	now      func() time.Time
}

// Counters are the hub's lifetime counts.
type Counters struct {
	Accepted, Duplicates, Submitted, Delivered, Failed atomic.Int64
	Retries, Failovers, StaleJobs, Recovered           atomic.Int64
}

// NewHub opens the hub on db. The caller owns db.
func NewHub(name string, cfg HubConfig, db *sql.DB, dialect string, q Queue, log *slog.Logger) (*Hub, error) {
	store, err := NewStore(db, dialect)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	h := &Hub{
		Name: name, Cfg: cfg, Store: store, Dir: NewDirectory(), Router: NewRouter(cfg.Routing), Log: log.With("hub", name),
		queue: q, providers: map[string]*Provider{}, registered: map[string]bool{}, limiters: map[string]*tokenBucket{},
		stop: make(chan struct{}), now: time.Now,
	}
	h.Dir.SetBaseRates(cfg.baseRates())
	return h, nil
}

// Migrate prepares the schema.
func (h *Hub) Migrate(ctx context.Context) error { return h.Store.Migrate(ctx) }

func newID(prefix string) string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------------------
// Providers
// ---------------------------------------------------------------------------

// AddProvider registers a provider with its gateway, replacing any provider of
// the same name. It is called when a BCL provider resource opens and when a
// provider is created at runtime.
func (h *Hub) AddProvider(name, kind string, cfg ProviderConfig, gw gateway.Gateway, dynamic bool) error {
	if err := cfg.normalize(name); err != nil {
		return err
	}
	p := &Provider{Name: name, Kind: kind, Cfg: cfg, GW: gw, Dynamic: dynamic, limiter: newTokenBucket(cfg.TPS)}
	if src, ok := gw.(gateway.DLRSource); ok {
		src.SetDLRSink(func(ctx context.Context, d gateway.DLR) {
			d.Provider = name
			if err := h.EnqueueDLR(d); err != nil {
				h.Log.Error("cannot queue delivery receipt", "provider", name, "error", err)
			}
		})
	}
	h.mu.Lock()
	old := h.providers[name]
	h.providers[name] = p
	started := h.started
	h.mu.Unlock()
	h.Dir.PutProvider(name, cfg, dynamic)
	if old != nil && old.GW != nil && old.GW != gw {
		_ = old.GW.Close()
	}
	if started {
		if err := h.rebuildRoutes(); err != nil {
			return err
		}
		h.consume(name, cfg)
	}
	return nil
}

// RemoveProvider forgets a provider and closes its gateway.
func (h *Hub) RemoveProvider(name string) {
	h.mu.Lock()
	old := h.providers[name]
	delete(h.providers, name)
	started := h.started
	h.mu.Unlock()
	h.Dir.RemoveProvider(name)
	if old != nil && old.GW != nil {
		_ = old.GW.Close()
	}
	if started {
		_ = h.rebuildRoutes()
	}
}

func (h *Hub) provider(name string) (*Provider, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	p, ok := h.providers[name]
	return p, ok
}

// Gateway returns the gateway of a provider, for tests and tooling.
func (h *Hub) Gateway(name string) (gateway.Gateway, bool) {
	p, ok := h.provider(name)
	if !ok {
		return nil, false
	}
	return p.GW, true
}

// SetProviderState sets a provider's operational state for routing: active,
// down, maintenance, draining or suspended. It is runtime state: it is not
// stored, and a restart returns providers to active.
func (h *Hub) SetProviderState(name, state string) error {
	st := routing.ProviderState(state)
	switch st {
	case routing.ProviderActive, routing.ProviderDown, routing.ProviderMaintenance, routing.ProviderDraining, routing.ProviderSuspended:
	default:
		return fmt.Errorf("state must be active, down, maintenance, draining or suspended")
	}
	h.Router.SetState(name, st)
	return nil
}

func (h *Hub) rebuildRoutes() error { return h.Router.Rebuild(h.Dir.Snapshot()) }

// Health reports every provider.
func (h *Hub) Health(ctx context.Context) []Health {
	var out []Health
	for _, v := range h.Dir.Providers() {
		hl := Health{Name: v.Name, Owner: v.Owner, Disabled: v.Disabled, Countries: v.Config.Countries}
		if p, ok := h.provider(v.Name); ok {
			p.mu.Lock()
			hl.Kind, hl.Dynamic = p.Kind, p.Dynamic
			hl.Consecutive, hl.LastError, hl.LastSuccess = p.failures, p.lastErr, p.lastOK
			p.mu.Unlock()
			hl.Sent, hl.Failed, hl.Throttled = p.Sent.Load(), p.Failed.Load(), p.Throttled.Load()
		}
		if st, ok := h.Router.Stats(v.Name); ok {
			hl.Quality, hl.DeliveryRate = st.QualityScore, st.DeliveryRate
		}
		if q, ok := h.queue.(interface{ DLQDepth(string) int }); ok {
			hl.QueueDLQ = q.DLQDepth(JobDispatchPrefix + v.Name)
		}
		out = append(out, hl)
	}
	return out
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start loads state, builds the routes, registers every consumer and starts the
// recovery loop. runner executes the BCL pipelines the consumers trigger.
func (h *Hub) Start(ctx context.Context, runner IntentRunner) error {
	h.mu.Lock()
	if h.started {
		h.mu.Unlock()
		return errors.New("sms: hub already started")
	}
	h.runner = runner
	h.mu.Unlock()

	if err := h.loadState(ctx); err != nil {
		return err
	}
	if err := h.applySeeds(ctx); err != nil {
		return err
	}
	if err := h.rebuildRoutes(); err != nil {
		return err
	}

	h.mu.Lock()
	h.started = true
	names := make([]string, 0, len(h.providers))
	cfgs := map[string]ProviderConfig{}
	for n, p := range h.providers {
		names = append(names, n)
		cfgs[n] = p.Cfg
	}
	h.mu.Unlock()

	h.queue.Register(JobDLR, h.handle(h.Cfg.Pipeline.DLR))
	for kind, job := range map[string]string{"user": JobConfigUser, "provider": JobConfigProvider, "assignment": JobConfigAssign, "rate": JobConfigRate} {
		h.queue.RegisterBroadcast(job, h.configHandler(kind))
	}
	for _, n := range names {
		h.consume(n, cfgs[n])
	}
	if err := h.queue.Start(); err != nil {
		return err
	}
	h.wg.Add(1)
	go h.recoveryLoop()
	h.Log.Info("sms hub started", "providers", len(names), "node", h.Cfg.NodeID)
	return nil
}

// consume registers the dispatch consumers of one provider.
func (h *Hub) consume(name string, cfg ProviderConfig) {
	job := JobDispatchPrefix + name
	h.mu.Lock()
	if h.registered[job] {
		h.mu.Unlock()
		return
	}
	h.registered[job] = true
	h.mu.Unlock()
	if t, ok := h.queue.(Tunable); ok {
		// The queue's own attempts are a safety net for crashes and infrastructure
		// errors; the provider's retry policy is enforced by the pipeline.
		t.Tune(job, cfg.Concurrency, 10)
	}
	h.queue.Register(job, h.handle(h.Cfg.Pipeline.Deliver))
}

// handle adapts a queue job to a BCL intent run.
func (h *Hub) handle(intent string) fh.QueueHandler {
	return func(ctx context.Context, job *fh.QueueJob) error {
		h.mu.RLock()
		run := h.runner
		h.mu.RUnlock()
		if run == nil {
			return errors.New("sms: no pipeline runner attached")
		}
		return run(ctx, intent, job.Payload, job.Headers)
	}
}

func (h *Hub) loadState(ctx context.Context) error {
	users, err := h.Store.ListUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		h.Dir.PutUser(u)
	}
	recs, err := h.Store.ListProviders(ctx)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if err := h.instantiate(ctx, r); err != nil {
			// One broken stored provider must not stop the platform; it is
			// reported and skipped.
			h.Log.Error("cannot start stored provider", "provider", r.Name, "error", err)
		}
	}
	assigns, err := h.Store.ListAssignments(ctx)
	if err != nil {
		return err
	}
	for _, a := range assigns {
		h.Dir.PutAssignment(a)
	}
	rates, err := h.Store.ListRates(ctx)
	if err != nil {
		return err
	}
	for _, r := range rates {
		h.Dir.PutRate(r)
	}
	return nil
}

func (h *Hub) applySeeds(ctx context.Context) error {
	for _, su := range h.Cfg.Seed.Users {
		u := su.User
		if u.ID == "" {
			return errors.New("sms: a seed user needs an id")
		}
		if _, err := h.Store.GetUser(ctx, u.ID); err == nil {
			continue // never overwrite: admin changes survive restarts
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if su.APIKey != "" {
			u.APIKeyHash = HashKey(su.APIKey)
		}
		if err := h.putUserLocal(ctx, &u); err != nil {
			return fmt.Errorf("sms: seed user %s: %w", u.ID, err)
		}
		if su.Balance > 0 {
			if _, err := h.Store.TopUp(ctx, u.ID, h.Cfg.Currency, FromUnits(su.Balance), "seed:"+u.ID); err != nil {
				return err
			}
		}
	}
	for _, a := range h.Cfg.Seed.Assignments {
		if _, err := h.Store.GetAssignment(ctx, a.ID); err == nil {
			continue
		}
		if err := h.Store.PutAssignment(ctx, a); err != nil {
			return err
		}
		h.Dir.PutAssignment(a)
	}
	for _, r := range h.Cfg.Seed.Rates {
		if _, err := h.Store.GetRate(ctx, r.ID); err == nil {
			continue
		}
		if err := h.Store.PutRate(ctx, r); err != nil {
			return err
		}
		h.Dir.PutRate(r)
	}
	return nil
}

// Close stops the hub: consumers drain with the queue, the recovery loop ends
// and gateways close.
func (h *Hub) Close() error {
	h.mu.Lock()
	select {
	case <-h.stop:
		h.mu.Unlock()
		return nil
	default:
		close(h.stop)
	}
	provs := make([]*Provider, 0, len(h.providers))
	for _, p := range h.providers {
		provs = append(provs, p)
	}
	h.mu.Unlock()
	h.wg.Wait()
	for _, p := range provs {
		if p.GW != nil {
			_ = p.GW.Close()
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Configuration: persist, apply here, broadcast to the other nodes
// ---------------------------------------------------------------------------

func (h *Hub) putUserLocal(ctx context.Context, u *User) error {
	if err := h.normalizeUser(u); err != nil {
		return err
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = h.now().UTC()
	}
	if err := h.Store.PutUser(ctx, *u); err != nil {
		return err
	}
	if err := h.Store.EnsureAccount(ctx, u.ID, u.Currency); err != nil {
		return err
	}
	h.Dir.PutUser(*u)
	return nil
}

func (h *Hub) normalizeUser(u *User) error {
	u.ID = strings.TrimSpace(u.ID)
	if u.ID == "" || strings.ContainsAny(u.ID, " \t\n/\\|") {
		return errors.New("a user id is required and may not contain spaces or separators")
	}
	if u.Tenant == "" {
		u.Tenant = u.ID
	}
	if u.Status == "" {
		u.Status = UserActive
	}
	if u.Status != UserActive && u.Status != UserSuspended {
		return fmt.Errorf("user status must be %s or %s", UserActive, UserSuspended)
	}
	if u.Currency == "" {
		u.Currency = h.Cfg.Currency
	}
	if !strings.EqualFold(u.Currency, h.Cfg.Currency) {
		return fmt.Errorf("the platform bills in %s; user currency %s is not supported", h.Cfg.Currency, u.Currency)
	}
	u.Currency = h.Cfg.Currency
	for i, c := range u.Countries {
		c = strings.ToUpper(c)
		if !KnownCountry(c) {
			return fmt.Errorf("unknown country %q", c)
		}
		u.Countries[i] = c
	}
	switch u.Objective {
	case "", ObjectiveBalanced, ObjectiveCost, ObjectiveDelivery, ObjectiveLatency:
	default:
		return fmt.Errorf("unknown objective %q", u.Objective)
	}
	return nil
}

func (h *Hub) broadcast(job string, id string) {
	if err := h.queue.Broadcast(job, map[string]string{"id": id}); err != nil {
		h.Log.Error("cannot broadcast configuration change", "job", job, "id", id, "error", err)
	}
}

// PutUser creates or updates a user.
func (h *Hub) PutUser(ctx context.Context, u User) (User, error) {
	if err := h.putUserLocal(ctx, &u); err != nil {
		return u, err
	}
	if err := h.rebuildRoutes(); err != nil {
		return u, err
	}
	h.broadcast(JobConfigUser, u.ID)
	return u, nil
}

// DeleteUser removes a user.
func (h *Hub) DeleteUser(ctx context.Context, id string) error {
	if err := h.Store.DeleteUser(ctx, id); err != nil {
		return err
	}
	h.Dir.RemoveUser(id)
	_ = h.rebuildRoutes()
	h.broadcast(JobConfigUser, id)
	return nil
}

// IssueKey creates a new API key for a user, replacing the old one. The key is
// returned once; only its hash is kept.
func (h *Hub) IssueKey(ctx context.Context, userID string) (string, error) {
	u, err := h.Store.GetUser(ctx, userID)
	if err != nil {
		return "", err
	}
	var b [24]byte
	_, _ = rand.Read(b[:])
	key := "smsk_" + hex.EncodeToString(b[:])
	u.APIKeyHash = HashKey(key)
	if _, err := h.PutUser(ctx, u); err != nil {
		return "", err
	}
	return key, nil
}

// TopUp credits a user's balance idempotently by reference.
func (h *Hub) TopUp(ctx context.Context, userID string, amount float64, ref string) (Balance, bool, error) {
	if _, ok := h.Dir.User(userID); !ok {
		return Balance{}, false, ErrUnknownUser
	}
	applied, err := h.Store.TopUp(ctx, userID, h.Cfg.Currency, FromUnits(amount), ref)
	if err != nil {
		return Balance{}, false, err
	}
	b, err := h.Store.Balance(ctx, userID)
	return b, applied, err
}

// PutProvider creates or replaces a provider at runtime. The gateway is built
// first, so a configuration that cannot connect is refused before anything is
// stored.
func (h *Hub) PutProvider(ctx context.Context, rec ProviderRecord) error {
	if rec.Name == "" || strings.ContainsAny(rec.Name, " \t\n/\\|.") {
		return errors.New("a provider name is required and may not contain spaces, dots or separators")
	}
	if _, ok := h.Dir.Provider(rec.Name); ok {
		if p, _ := h.provider(rec.Name); p != nil && !p.Dynamic {
			return fmt.Errorf("provider %q is declared in BCL and cannot be changed at runtime", rec.Name)
		}
	}
	if rec.Owner != "" {
		if _, ok := h.Dir.User(rec.Owner); !ok {
			return fmt.Errorf("owner %q is not a known user", rec.Owner)
		}
	}
	// Build first, so a configuration that cannot work is refused before anything
	// is stored; store second, so a provider that is running is always one that
	// will come back after a restart; activate last.
	built, err := h.build(ctx, rec)
	if err != nil {
		return err
	}
	if err := h.Store.PutProvider(ctx, rec); err != nil {
		_ = built.gw.Close()
		return err
	}
	if err := h.activate(rec, built); err != nil {
		_ = built.gw.Close()
		_ = h.Store.DeleteProvider(ctx, rec.Name)
		return err
	}
	h.broadcast(JobConfigProvider, rec.Name)
	return nil
}

// DeleteProvider removes a runtime-created provider.
func (h *Hub) DeleteProvider(ctx context.Context, name string) error {
	if p, ok := h.provider(name); ok && !p.Dynamic {
		return fmt.Errorf("provider %q is declared in BCL and cannot be removed at runtime", name)
	}
	if err := h.Store.DeleteProvider(ctx, name); err != nil {
		return err
	}
	h.RemoveProvider(name)
	h.broadcast(JobConfigProvider, name)
	return nil
}

type builtProvider struct {
	cfg         ProviderConfig
	gw          gateway.Gateway
	fingerprint string
}

// build validates a provider record and opens its gateway.
func (h *Hub) build(ctx context.Context, rec ProviderRecord) (builtProvider, error) {
	cfg, err := decodeProviderConfig(rec.Config)
	if err != nil {
		return builtProvider{}, fmt.Errorf("provider %q: %w", rec.Name, err)
	}
	cfg.Owner = rec.Owner
	cfg.Disabled = !rec.Enabled
	if err := cfg.normalize(rec.Name); err != nil {
		return builtProvider{}, err
	}
	raw, _ := json.Marshal(rec)
	sum := sha256.Sum256(raw)
	gw, err := gateway.Open(ctx, rec.Kind, rec.Name, cfg.Plugin)
	if err != nil {
		return builtProvider{}, err
	}
	return builtProvider{cfg: cfg, gw: gw, fingerprint: hex.EncodeToString(sum[:])}, nil
}

func (h *Hub) activate(rec ProviderRecord, b builtProvider) error {
	if err := h.AddProvider(rec.Name, rec.Kind, b.cfg, b.gw, true); err != nil {
		return err
	}
	h.mu.Lock()
	if p := h.providers[rec.Name]; p != nil && p.GW == b.gw {
		p.fingerprint = b.fingerprint
	}
	h.mu.Unlock()
	return nil
}

// instantiate builds a stored provider record into a running provider, unless
// the running one was already built from this very record.
func (h *Hub) instantiate(ctx context.Context, rec ProviderRecord) error {
	raw, _ := json.Marshal(rec)
	sum := sha256.Sum256(raw)
	if p, ok := h.provider(rec.Name); ok && p.Dynamic && p.fingerprint == hex.EncodeToString(sum[:]) {
		return nil
	}
	b, err := h.build(ctx, rec)
	if err != nil {
		return err
	}
	if err := h.activate(rec, b); err != nil {
		_ = b.gw.Close()
		return err
	}
	return nil
}

// PutAssignment creates or replaces an assignment.
func (h *Hub) PutAssignment(ctx context.Context, a Assignment) (Assignment, error) {
	if a.ID == "" {
		a.ID = newID("asg_")
	}
	if a.Provider == "" {
		return a, errors.New("an assignment needs a provider")
	}
	if _, ok := h.Dir.Provider(a.Provider); !ok {
		return a, fmt.Errorf("provider %q does not exist", a.Provider)
	}
	if a.UserID == "" && a.Tenant == "" {
		return a, errors.New("an assignment needs a user or a tenant")
	}
	if a.UserID != "" {
		if _, ok := h.Dir.User(a.UserID); !ok {
			return a, fmt.Errorf("user %q does not exist", a.UserID)
		}
	}
	for i, c := range a.Countries {
		c = strings.ToUpper(c)
		if !KnownCountry(c) {
			return a, fmt.Errorf("unknown country %q", c)
		}
		a.Countries[i] = c
	}
	for _, pfx := range a.Prefixes {
		if !validPrefix(pfx) {
			return a, fmt.Errorf("prefix %q must be 1-15 digits", pfx)
		}
	}
	if err := h.Store.PutAssignment(ctx, a); err != nil {
		return a, err
	}
	h.Dir.PutAssignment(a)
	if err := h.rebuildRoutes(); err != nil {
		return a, err
	}
	h.broadcast(JobConfigAssign, a.ID)
	return a, nil
}

// DeleteAssignment removes an assignment.
func (h *Hub) DeleteAssignment(ctx context.Context, id string) error {
	if err := h.Store.DeleteAssignment(ctx, id); err != nil {
		return err
	}
	h.Dir.RemoveAssignment(id)
	_ = h.rebuildRoutes()
	h.broadcast(JobConfigAssign, id)
	return nil
}

// PutRate creates or replaces a rate.
func (h *Hub) PutRate(ctx context.Context, r Rate) (Rate, error) {
	if r.ID == "" {
		r.ID = newID("rate_")
	}
	if r.SellPerSegmentMicros < 0 {
		return r, errors.New("a rate cannot be negative")
	}
	r.Country = strings.ToUpper(r.Country)
	if r.Country != "" && !KnownCountry(r.Country) {
		return r, fmt.Errorf("unknown country %q", r.Country)
	}
	if r.UserID != "" {
		if _, ok := h.Dir.User(r.UserID); !ok {
			return r, fmt.Errorf("user %q does not exist", r.UserID)
		}
	}
	if err := h.Store.PutRate(ctx, r); err != nil {
		return r, err
	}
	h.Dir.PutRate(r)
	h.broadcast(JobConfigRate, r.ID)
	return r, nil
}

// DeleteRate removes a rate.
func (h *Hub) DeleteRate(ctx context.Context, id string) error {
	if err := h.Store.DeleteRate(ctx, id); err != nil {
		return err
	}
	h.Dir.RemoveRate(id)
	h.broadcast(JobConfigRate, id)
	return nil
}

// configHandler is the consumer of one configuration job type. It reloads the
// named entity from the store, so a duplicated or reordered event is harmless
// and every node converges on what the database says.
func (h *Hub) configHandler(kind string) fh.QueueHandler {
	return func(ctx context.Context, job *fh.QueueJob) error {
		var ev struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(job.Payload, &ev); err != nil || ev.ID == "" {
			return nil // a malformed event can never succeed; drop it
		}
		var err error
		switch kind {
		case "user":
			var u User
			if u, err = h.Store.GetUser(ctx, ev.ID); errors.Is(err, ErrNotFound) {
				h.Dir.RemoveUser(ev.ID)
				err = nil
			} else if err == nil {
				h.Dir.PutUser(u)
			}
		case "provider":
			var rec ProviderRecord
			if rec, err = h.Store.GetProvider(ctx, ev.ID); errors.Is(err, ErrNotFound) {
				if p, ok := h.provider(ev.ID); ok && p.Dynamic {
					h.RemoveProvider(ev.ID)
				}
				err = nil
			} else if err == nil {
				err = h.instantiate(ctx, rec)
			}
		case "assignment":
			var a Assignment
			if a, err = h.Store.GetAssignment(ctx, ev.ID); errors.Is(err, ErrNotFound) {
				h.Dir.RemoveAssignment(ev.ID)
				err = nil
			} else if err == nil {
				h.Dir.PutAssignment(a)
			}
		case "rate":
			var r Rate
			if r, err = h.Store.GetRate(ctx, ev.ID); errors.Is(err, ErrNotFound) {
				h.Dir.RemoveRate(ev.ID)
				err = nil
			} else if err == nil {
				h.Dir.PutRate(r)
			}
		}
		if err != nil {
			return err
		}
		return h.rebuildRoutes()
	}
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

type tokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(perSecond int) *tokenBucket {
	if perSecond <= 0 {
		return nil
	}
	return &tokenBucket{rate: float64(perSecond), burst: float64(max(perSecond, 1)), tokens: float64(max(perSecond, 1)), last: time.Now()}
}

// reserve takes a token and returns how long to wait before using it (zero
// when one is free). A nil bucket is unlimited.
func (b *tokenBucket) reserve() time.Duration {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	b.tokens--
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(-b.tokens / b.rate * float64(time.Second))
}

// allow takes a token if one is free.
func (b *tokenBucket) allow() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (h *Hub) userAllowed(u User) bool {
	if u.RatePerSecond <= 0 {
		return true
	}
	h.limMu.Lock()
	b := h.limiters[u.ID]
	if b == nil || int(b.rate) != u.RatePerSecond {
		b = newTokenBucket(u.RatePerSecond)
		h.limiters[u.ID] = b
	}
	h.limMu.Unlock()
	return b.allow()
}
