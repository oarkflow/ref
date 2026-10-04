package sms

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/oarkflow/ref/platform"

	"github.com/oarkflow/ref/examples/smsgateway/internal/cfgdec"
	"github.com/oarkflow/ref/examples/smsgateway/internal/gateway"
)

// Resource kinds this package adds to REF.
const (
	KindHub     = "sms.hub"
	KindAuth    = "sms.auth"
	KindGateway = "sms.gateway." // + plugin kind
)

var registerOnce sync.Once

// Register installs the application's resource kinds and actions into REF. It
// must run after every gateway plugin has registered itself (their init
// functions), because each plugin becomes a BCL resource kind,
// sms.gateway.<plugin>. It is safe to call more than once.
func Register() {
	registerOnce.Do(func() {
		platform.RegisterResourceDriver(KindHub, platform.ResourceFactoryFunc(openHub), platform.ResourceKindInfo{
			Family:  "sms",
			Summary: "SMS platform core: users, providers, routing, ledger and queue consumers.",
			Config: []platform.ConfigField{
				{Name: "database", Type: "resource", Required: true, Summary: "A database.sql resource (SQLite or PostgreSQL)"},
				{Name: "queue", Type: "resource", Required: true, Summary: "A queue.broker resource"},
				{Name: "currency", Type: "string", Default: "USD"},
				{Name: "default_country", Type: "string", Summary: "Reads national-format numbers"},
				{Name: "capture_on", Type: "string", Default: "submit", Summary: "submit or delivered"},
				{Name: "routing", Type: "block"},
				{Name: "pricing", Type: "block"},
				{Name: "recovery", Type: "block"},
				{Name: "seed", Type: "block"},
			},
		})
		platform.RegisterResourceDriver(KindAuth, platform.ResourceFactoryFunc(openAuth), platform.ResourceKindInfo{
			Family:   "auth",
			Summary:  "Authenticates API keys against the sms.hub user directory.",
			Provides: []string{"Authenticator"},
			Config:   []platform.ConfigField{{Name: "hub", Type: "resource", Required: true}},
		})
		for _, kind := range gateway.Kinds() {
			platform.RegisterResourceDriver(KindGateway+kind, platform.ResourceFactoryFunc(openGateway(kind)), platform.ResourceKindInfo{
				Family:  "sms",
				Summary: "SMS provider using the " + kind + " gateway plugin.",
				Config: []platform.ConfigField{
					{Name: "hub", Type: "resource", Required: true},
					{Name: "countries", Type: "[]string"},
					{Name: "cost_per_segment", Type: "number"},
					{Name: "quality", Type: "number"},
					{Name: "retry", Type: "block"},
					{Name: "capabilities", Type: "block"},
					{Name: "plugin", Type: "block", Summary: "Settings of the " + kind + " plugin"},
				},
			})
		}
		registerActions()
	})
}

func openHub(ctx context.Context, spec platform.ResourceSpec) (platform.Resource, io.Closer, error) {
	cfg, err := decodeHubConfig(spec.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("sms.hub %q: %w", spec.Name, err)
	}
	dbRes, ok := spec.Dependency("database")
	if !ok {
		return nil, nil, fmt.Errorf("sms.hub %q: config.database must name a database.sql resource", spec.Name)
	}
	db, ok := dbRes.(*platform.Database)
	if !ok {
		return nil, nil, fmt.Errorf("sms.hub %q: config.database must name a database.sql resource", spec.Name)
	}
	qRes, ok := spec.Dependency("queue")
	if !ok {
		return nil, nil, fmt.Errorf("sms.hub %q: config.queue must name a queue.broker resource", spec.Name)
	}
	q, ok := qRes.(Queue)
	if !ok {
		return nil, nil, fmt.Errorf("sms.hub %q: config.queue must name a queue.broker resource", spec.Name)
	}
	h, err := NewHub(spec.Name, cfg, db.DB, db.Dialect, q, slog.Default())
	if err != nil {
		return nil, nil, fmt.Errorf("sms.hub %q: %w", spec.Name, err)
	}
	if err := h.Migrate(ctx); err != nil {
		return nil, nil, fmt.Errorf("sms.hub %q: %w", spec.Name, err)
	}
	return h, h, nil
}

// hubDependency resolves config.hub to the hub resource.
func hubDependency(spec platform.ResourceSpec) (*Hub, error) {
	res, ok := spec.Dependency("hub")
	if !ok {
		return nil, fmt.Errorf("%s %q: config.hub must name an sms.hub resource (and the resource must declare depends_on [that hub])", spec.Kind, spec.Name)
	}
	h, ok := res.(*Hub)
	if !ok {
		return nil, fmt.Errorf("%s %q: config.hub must name an sms.hub resource", spec.Kind, spec.Name)
	}
	return h, nil
}

func openGateway(kind string) func(context.Context, platform.ResourceSpec) (platform.Resource, io.Closer, error) {
	return func(ctx context.Context, spec platform.ResourceSpec) (platform.Resource, io.Closer, error) {
		h, err := hubDependency(spec)
		if err != nil {
			return nil, nil, err
		}
		cfg, err := decodeProviderConfig(spec.Config)
		if err != nil {
			return nil, nil, fmt.Errorf("%s %q: %w", spec.Kind, spec.Name, err)
		}
		if err := cfg.normalize(spec.Name); err != nil {
			return nil, nil, err
		}
		gw, err := gateway.Open(ctx, kind, spec.Name, cfg.Plugin)
		if err != nil {
			return nil, nil, fmt.Errorf("%s %q: %w", spec.Kind, spec.Name, err)
		}
		if err := h.AddProvider(spec.Name, kind, cfg, gw, false); err != nil {
			_ = gw.Close()
			return nil, nil, err
		}
		// The hub owns the gateway's lifetime: it closes every gateway when it
		// closes, and replaces one when its configuration changes.
		return gw, nil, nil
	}
}

// ---------------------------------------------------------------------------
// Authentication
// ---------------------------------------------------------------------------

// authenticator resolves API keys through the hub's directory, so a key issued
// through the admin API works the moment its change event is applied.
type authenticator struct{ hub *Hub }

func openAuth(_ context.Context, spec platform.ResourceSpec) (platform.Resource, io.Closer, error) {
	var cfg struct {
		Hub string `json:"hub"`
	}
	if err := cfgdec.Decode(spec.Config, &cfg); err != nil {
		return nil, nil, fmt.Errorf("sms.auth %q: %w", spec.Name, err)
	}
	h, err := hubDependency(spec)
	if err != nil {
		return nil, nil, err
	}
	return &authenticator{hub: h}, nil, nil
}

// ErrBadCredentials is the single answer to every authentication failure.
var ErrBadCredentials = errors.New("invalid credentials")

// Authenticate implements the REF authenticator contract.
func (a *authenticator) Authenticate(_ context.Context, creds platform.Credentials) (platform.Principal, error) {
	key := strings.TrimSpace(creds.BearerToken)
	if key == "" {
		key = strings.TrimSpace(creds.APIKey)
	}
	if key == "" {
		return platform.Principal{}, ErrBadCredentials
	}
	u, ok := a.hub.Dir.UserByKey(key)
	if !ok {
		return platform.Principal{}, ErrBadCredentials
	}
	return platform.Principal{ID: u.ID, Username: u.Name, Roles: []string{"sender"}, Claims: map[string]any{"tenant": u.Tenant}}, nil
}
