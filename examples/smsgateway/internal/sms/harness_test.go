package sms

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/oarkflow/ref/examples/smsgateway/internal/brokerq"
	"github.com/oarkflow/ref/examples/smsgateway/internal/cfgdec"
	"github.com/oarkflow/ref/examples/smsgateway/internal/gateway"
)

// rig is a hub on a real SQLite file and a real broker queue, with the BCL
// pipeline replaced by the equivalent Go calls. The BCL pipeline has its own
// end-to-end tests; these exercise the engine.
type rig struct {
	t    *testing.T
	hub  *Hub
	q    *brokerq.Queue
	db   *sql.DB
	mock map[string]*gateway.Mock
	// close shuts the rig down; it is idempotent.
	close func()
}

func fastCfg() HubConfig {
	cfg, err := decodeHubConfig(map[string]any{})
	if err != nil {
		panic(err)
	}
	cfg.Pricing.Default = 0.02
	cfg.Recovery.Interval = Duration(50 * time.Millisecond)
	cfg.Recovery.Grace = Duration(100 * time.Millisecond)
	cfg.DefaultCountry = "NP"
	return cfg
}

func newRig(t *testing.T, mutate ...func(*HubConfig)) *rig {
	t.Helper()
	return newRigIn(t, t.TempDir(), mutate...)
}

// newRigIn opens a rig on the storage in dir, so a test can restart one.
func newRigIn(t *testing.T, dir string, mutate ...func(*HubConfig)) *rig {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "sms.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	q, err := brokerq.Open(brokerq.Config{
		Dir: filepath.Join(dir, "broker"), Concurrency: 4, PollInterval: cfgdec.Duration(5 * time.Millisecond),
		Retry: brokerq.Retry{MaxAttempts: 4, Initial: cfgdec.Duration(10 * time.Millisecond), Max: cfgdec.Duration(50 * time.Millisecond), Factor: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := fastCfg()
	for _, m := range mutate {
		m(&cfg)
	}
	h, err := NewHub("sms", cfg, db, "sqlite", q, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, hub: h, q: q, db: db, mock: map[string]*gateway.Mock{}}
	var once sync.Once
	r.close = func() {
		once.Do(func() {
			_ = h.Close()
			_ = q.Close()
			_ = db.Close()
		})
	}
	t.Cleanup(r.close)
	return r
}

// provider adds a mock provider.
func (r *rig) provider(name string, cfg ProviderConfig, plugin map[string]any) *gateway.Mock {
	r.t.Helper()
	gw, err := gateway.Open(context.Background(), "mock", name, plugin)
	if err != nil {
		r.t.Fatal(err)
	}
	if cfg.Retry.Initial == 0 {
		cfg.Retry = RetryConfig{MaxAttempts: 3, Initial: Duration(10 * time.Millisecond), Max: Duration(40 * time.Millisecond), Factor: 2}
	}
	if err := r.hub.AddProvider(name, "mock", cfg, gw, false); err != nil {
		r.t.Fatal(err)
	}
	m := gw.(*gateway.Mock)
	r.mock[name] = m
	return m
}

func (r *rig) user(id string, balance float64, mutate ...func(*User)) User {
	r.t.Helper()
	u := User{ID: id, Status: UserActive}
	for _, m := range mutate {
		m(&u)
	}
	u, err := r.hub.PutUser(context.Background(), u)
	if err != nil {
		r.t.Fatal(err)
	}
	if balance > 0 {
		if _, _, err := r.hub.TopUp(context.Background(), id, balance, "init:"+id); err != nil {
			r.t.Fatal(err)
		}
	}
	return u
}

// start starts the hub with the Go pipeline.
func (r *rig) start() {
	r.t.Helper()
	if err := r.hub.Start(context.Background(), r.runner()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) runner() IntentRunner {
	return func(ctx context.Context, intent string, payload []byte, _ map[string]string) error {
		switch intent {
		case r.hub.Cfg.Pipeline.Deliver:
			js, err := r.hub.LoadJob(ctx, payload)
			if err != nil {
				return err
			}
			out, err := r.hub.ProviderSend(ctx, js)
			if err != nil {
				return err
			}
			_, err = r.hub.Settle(ctx, js, out)
			return err
		case r.hub.Cfg.Pipeline.DLR:
			_, err := r.hub.ApplyDLR(ctx, payload)
			return err
		}
		return fmt.Errorf("unexpected intent %s", intent)
	}
}

// send runs the submit pipeline in Go: validate, check, route, pay, enqueue.
func (r *rig) send(userID string, req SendRequest) (AcceptResult, error) {
	r.t.Helper()
	ctx := context.Background()
	u, err := r.hub.ValidateUser(ctx, userID)
	if err != nil {
		return AcceptResult{}, err
	}
	d, err := r.hub.CheckData(ctx, u, req)
	if err != nil {
		return AcceptResult{}, err
	}
	ex, err := r.hub.PlanRoute(ctx, u, d)
	if err != nil {
		return AcceptResult{}, err
	}
	res, err := r.hub.Accept(ctx, u, d, ex)
	if err != nil {
		return res, err
	}
	return res, r.hub.Enqueue(ctx, res)
}

func (r *rig) message(id string) *Message {
	r.t.Helper()
	m, err := r.hub.Store.Get(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

func (r *rig) balance(user string) Balance {
	r.t.Helper()
	b, err := r.hub.Store.Balance(context.Background(), user)
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}

func (r *rig) waitState(id string, want ...MessageState) *Message {
	r.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var m *Message
	for time.Now().Before(deadline) {
		m = r.message(id)
		for _, w := range want {
			if m.State == w {
				return m
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("message %s is %s (provider %s, attempts %d, error %q); wanted %v", id, m.State, m.Provider, m.TotalAttempts, m.Error, want)
	return nil
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func np(cfg ProviderConfig) ProviderConfig {
	if len(cfg.Countries) == 0 {
		cfg.Countries = []string{"NP"}
	}
	return cfg
}

var npNumber = "9841234567"
