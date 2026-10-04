// Package smpp adds SMPP to REF: a service.smpp resource (a bind, kept alive
// and re-established as needed) and an smpp.submit action that sends one
// message through it. It is built on github.com/oarkflow/smppflow's session and
// message layers.
//
//	resource "carrier" {
//	  kind "service.smpp"
//	  config {
//	    addr "smsc.example.com:2775"
//	    system_id "gw"
//	    password env.required("SMPP_PASSWORD")
//	    queue "bus"              # delivery receipts are published here …
//	    receipt_job "sms.dlr"    # … as jobs of this type
//	  }
//	}
//
//	node "send" {
//	  uses "smpp.submit"
//	  resource "carrier"
//	  requires [message]
//	  provides [sent]
//	  config { to "{{ message.to }}" from "{{ message.from }}" text "{{ message.text }}" dlr "{{ message.dlr }}" }
//	}
//
// smpp.submit never fails the intent because the carrier refused: it publishes
// { ok, provider_message_id, latency_ms, error { kind, protocol, status, code,
// message } } and leaves the meaning of a refusal to the rules that read it.
// The error's code is the SMPP status smppflow reports (INVALID_PASSWORD, or a
// hex code it has no name for).
package smpp

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/smppflow/pkg/events"
	"github.com/oarkflow/smppflow/pkg/lifecycle"
	"github.com/oarkflow/smppflow/pkg/message"
	"github.com/oarkflow/smppflow/pkg/protocol"
	"github.com/oarkflow/smppflow/pkg/session"

	cfgdec "github.com/oarkflow/ref/contrib/messaging/internal/cfg"
	"github.com/oarkflow/ref/platform"
)

// Config is the service.smpp resource's BCL config.
type Config struct {
	Addr            string          `json:"addr"`
	SystemID        string          `json:"system_id"`
	Password        string          `json:"password"`
	SystemType      string          `json:"system_type"`
	BindMode        string          `json:"bind_mode"` // transceiver (default) or transmitter
	Version         string          `json:"version"`   // 3.3, 3.4 (default) or 5.0
	WindowSize      int             `json:"window_size"`
	EnquireInterval cfgdec.Duration `json:"enquire_interval"`
	ResponseTimeout cfgdec.Duration `json:"response_timeout"`
	ConnectTimeout  cfgdec.Duration `json:"connect_timeout"`
	SourceTON       int             `json:"source_ton"`
	SourceNPI       int             `json:"source_npi"`
	DestTON         int             `json:"dest_ton"`
	DestNPI         int             `json:"dest_npi"`
	UseSAR          bool            `json:"use_sar"`
	// Queue names the queue resource receipts are published to, as jobs of
	// ReceiptJob. Without it receipts are dropped.
	Queue      string `json:"queue"`
	ReceiptJob string `json:"receipt_job"`
}

// Register installs service.smpp and smpp.submit.
func Register() {
	registerOnce.Do(func() {
		registerAnalyze()
		platform.RegisterResourceDriver("service.smpp", platform.ResourceFactoryFunc(open), platform.ResourceKindInfo{
			Family:   "service",
			Summary:  "An SMPP bind (smppflow): lazy connect, re-bind after a fault, delivery receipts published to a queue.",
			Provides: []string{"SMPP"},
			Config: []platform.ConfigField{
				{Name: "addr", Type: "string", Required: true},
				{Name: "system_id", Type: "string", Required: true},
				{Name: "password", Type: "string"},
				{Name: "bind_mode", Type: "string", Default: "transceiver"},
				{Name: "version", Type: "string", Default: "3.4"},
				{Name: "window_size", Type: "int", Default: "16"},
				{Name: "queue", Type: "resource", Summary: "Queue that receives delivery receipts"},
				{Name: "receipt_job", Type: "string", Summary: "Job type of a receipt"},
			},
		})
		platform.RegisterActionDriver("smpp.submit", platform.ActionFactoryFunc(buildSubmit), platform.ActionInfo{
			Family:       "service",
			Summary:      "Submit one message through an SMPP bind; the outcome, success or not, is published as data",
			ResourceKind: "service.smpp",
			Provides:     "{ ok, provider_message_id, latency_ms, error { kind, protocol, status, code, message } }",
			Kind:         "effect",
			Config: []platform.ConfigField{
				{Name: "to", Type: "template", Required: true},
				{Name: "from", Type: "template", Required: true},
				{Name: "text", Type: "template", Required: true},
				{Name: "dlr", Type: "template", Default: "true", Summary: "Request a delivery receipt"},
				{Name: "reference", Type: "template", Summary: "The platform's own message id, for the logs"},
				{Name: "system_id", Type: "template", Summary: "Bind as this account instead of the resource's own; empty uses the resource's. Binds are kept per account"},
				{Name: "password", Type: "template", Summary: "Password for system_id"},
			},
		})
	})
}

var registerOnce sync.Once

// Client is a service.smpp resource.
type Client struct {
	name string
	cfg  Config

	mu    sync.Mutex
	conns map[string]*conn

	publish func(job string, payload any) error
}

// conn is one bind. A Client keeps one per set of credentials in use, so the
// accounts of a carrier can be used side by side.
type conn struct {
	client  *session.Client
	manager *message.Manager
	stop    context.CancelFunc
}

type discardStore struct{}

func (discardStore) SaveMessage(context.Context, *message.Message) error { return nil }
func (discardStore) GetMessage(context.Context, message.ID) (*message.Message, error) {
	return nil, fmt.Errorf("not stored")
}
func (discardStore) ListMessages(context.Context) ([]*message.Message, error) { return nil, nil }
func (discardStore) UpdateMessageState(context.Context, message.ID, lifecycle.MessageState) error {
	return nil
}
func (discardStore) SaveDLR(context.Context, *message.DeliveryReceipt) error { return nil }
func (discardStore) ListDLR(context.Context) ([]*message.DeliveryReceipt, error) {
	return nil, nil
}

func open(_ context.Context, spec platform.ResourceSpec) (platform.Resource, io.Closer, error) {
	var cfg Config
	if err := cfgdec.Decode(spec.Config, &cfg); err != nil {
		return nil, nil, fmt.Errorf("service.smpp %q: %w", spec.Name, err)
	}
	if cfg.Addr == "" || cfg.SystemID == "" {
		return nil, nil, fmt.Errorf("service.smpp %q: addr and system_id are required", spec.Name)
	}
	switch cfg.BindMode {
	case "", "transceiver", "transmitter":
	default:
		return nil, nil, fmt.Errorf("service.smpp %q: bind_mode must be transceiver or transmitter", spec.Name)
	}
	if cfg.WindowSize <= 0 {
		cfg.WindowSize = 16
	}
	if cfg.EnquireInterval == 0 {
		cfg.EnquireInterval = cfgdec.Duration(30 * time.Second)
	}
	if cfg.ResponseTimeout == 0 {
		cfg.ResponseTimeout = cfgdec.Duration(10 * time.Second)
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = cfgdec.Duration(10 * time.Second)
	}
	c := &Client{name: spec.Name, cfg: cfg}
	if cfg.Queue != "" {
		if cfg.ReceiptJob == "" {
			return nil, nil, fmt.Errorf("service.smpp %q: receipt_job is required with queue", spec.Name)
		}
		res, ok := spec.Dependency("queue")
		if !ok {
			return nil, nil, fmt.Errorf("service.smpp %q: queue %q is not a declared resource", spec.Name, cfg.Queue)
		}
		q, ok := res.(interface {
			Enqueue(string, any, ...map[string]string) (string, error)
		})
		if !ok {
			return nil, nil, fmt.Errorf("service.smpp %q: resource %q is not a job queue", spec.Name, cfg.Queue)
		}
		c.publish = func(job string, payload any) error { _, err := q.Enqueue(job, payload); return err }
	}
	return c, c, nil
}

func (c *Client) version() protocol.Version {
	switch c.cfg.Version {
	case "3.3":
		return protocol.Version33
	case "5.0":
		return protocol.Version50
	default:
		return protocol.Version34
	}
}

// Failure is a submit failure with the carrier's own facts.
type Failure struct {
	Kind    string
	Status  int
	Code    string
	Message string
}

var statusToken = regexp.MustCompile(`(?:failed|bind failed):\s*(\S+)`)

func fail(kind string, err error) *Failure {
	f := &Failure{Kind: kind, Message: err.Error()}
	if m := statusToken.FindStringSubmatch(err.Error()); m != nil {
		f.Code = m[1]
		if strings.HasPrefix(strings.ToLower(m[1]), "0x") {
			if n, perr := strconv.ParseUint(m[1][2:], 16, 32); perr == nil {
				f.Status = int(n)
			}
		}
	}
	return f
}

// credKey names a bind: the credentials it was made with.
func credKey(systemID, password string) string { return systemID + "\x00" + password }

func (c *Client) connect(ctx context.Context, systemID, password string) (*message.Manager, *Failure) {
	if systemID == "" {
		systemID, password = c.cfg.SystemID, c.cfg.Password
	}
	key := credKey(systemID, password)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.conns[key]; existing != nil {
		return existing.manager, nil
	}
	mode := protocol.BindModeTransceiver
	if c.cfg.BindMode == "transmitter" {
		mode = protocol.BindModeTransmitter
	}
	bus := events.NewMemoryBus(true)
	client := session.NewClient(session.Config{
		Addr: c.cfg.Addr,
		Bind: protocol.BindRequest{Mode: mode, SystemID: systemID, Password: password,
			SystemType: c.cfg.SystemType, InterfaceVersion: c.version()},
		WindowSize: c.cfg.WindowSize, EnquireInterval: c.cfg.EnquireInterval.D(),
		ResponseTimeout: c.cfg.ResponseTimeout.D(), Bus: bus,
	})
	cctx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout.D())
	defer cancel()
	if err := client.Connect(cctx); err != nil {
		return nil, fail("connect", err)
	}
	if err := client.Bind(cctx); err != nil {
		_ = client.Close(context.Background())
		return nil, fail("bind", err)
	}
	mgr := message.NewManager(message.ManagerConfig{
		Client: client, Store: discardStore{}, Bus: bus, ProviderID: c.name, UseSAR: c.cfg.UseSAR,
		DefaultSourceTON: protocol.TON(c.cfg.SourceTON), DefaultSourceNPI: protocol.NPI(c.cfg.SourceNPI),
		DefaultDestTON: protocol.TON(c.cfg.DestTON), DefaultDestNPI: protocol.NPI(c.cfg.DestNPI),
	})
	rctx, stop := context.WithCancel(context.Background())
	mgr.StartInbound(rctx)
	bus.Subscribe(events.DLRReceived, func(_ context.Context, e events.Event) error {
		if d, ok := e.Data.(message.DeliveryReceipt); ok && c.publish != nil {
			at := d.DoneAt
			if at.IsZero() {
				at = d.ReceivedAt
			}
			return c.publish(c.cfg.ReceiptJob, map[string]any{
				"provider": c.name, "provider_message_id": d.ProviderMessageID,
				"state": string(d.State), "code": d.ErrorCode, "raw": d.Raw, "at": at.UTC().Format(time.RFC3339Nano),
			})
		}
		return nil
	})
	if c.conns == nil {
		c.conns = map[string]*conn{}
	}
	c.conns[key] = &conn{client: client, manager: mgr, stop: stop}
	return mgr, nil
}

// drop ends one bind (the one made with these credentials; none means the
// configured ones) so the next submit binds again.
func (c *Client) drop(systemID, password string) {
	if systemID == "" {
		systemID, password = c.cfg.SystemID, c.cfg.Password
	}
	key := credKey(systemID, password)
	c.mu.Lock()
	cn := c.conns[key]
	delete(c.conns, key)
	c.mu.Unlock()
	cn.end()
}

func (cn *conn) end() {
	if cn == nil {
		return
	}
	if cn.stop != nil {
		cn.stop()
	}
	if cn.client != nil {
		_ = cn.client.Close(context.Background())
	}
}

// Message is what to send.
type Message struct {
	From, To, Text string
	WantDLR        bool
	Reference      string
	// SystemID and Password, when set, are the account to bind as instead of
	// the resource's own.
	SystemID, Password string
}

// Submit sends one message. It returns the carrier's message id.
func (c *Client) Submit(ctx context.Context, m Message) (string, *Failure) {
	mgr, f := c.connect(ctx, m.SystemID, m.Password)
	if f != nil {
		return "", f
	}
	reg := byte(0)
	if m.WantDLR {
		reg = 1
	}
	res, err := mgr.Submit(ctx, message.SubmitRequest{ProviderID: c.name, From: m.From, To: m.To, Text: m.Text,
		RegisteredDelivery: reg, UseSAR: c.cfg.UseSAR, Meta: map[string]string{"reference": m.Reference}})
	if err != nil {
		f := fail("upstream", err)
		// A failure without an SMPP status may mean a dead session: rebind next time.
		if f.Code == "" {
			f.Kind = "transport"
			if ctx.Err() != nil {
				f.Kind = "timeout"
			}
			c.drop(m.SystemID, m.Password)
		}
		return "", f
	}
	if len(res.ProviderIDs) == 0 {
		return "", &Failure{Kind: "upstream", Code: "no_receipt", Message: "the carrier accepted the message without an id"}
	}
	return res.ProviderIDs[0], nil
}

// Close ends the bind.
func (c *Client) Close() error {
	c.mu.Lock()
	all := c.conns
	c.conns = nil
	c.mu.Unlock()
	for _, cn := range all {
		cn.end()
	}
	return nil
}

func buildSubmit(build platform.BuildContext, spec platform.NodeSpec) (platform.Action, error) {
	res, ok := build.Resource(spec.Resource)
	if !ok {
		return nil, fmt.Errorf("node %q: resource %q is not declared", spec.Name, spec.Resource)
	}
	client, ok := res.(*Client)
	if !ok {
		return nil, fmt.Errorf("node %q: resource %q is not a service.smpp", spec.Name, spec.Resource)
	}
	if len(spec.Provides) != 1 {
		return nil, fmt.Errorf("node %q: smpp.submit provides exactly one fact", spec.Name)
	}
	tmpl := func(key, def string) (*platform.Template, error) {
		raw, _ := spec.Config[key].(string)
		if raw == "" {
			raw = def
		}
		if raw == "" {
			return nil, fmt.Errorf("node %q: smpp.submit needs config.%s", spec.Name, key)
		}
		return platform.CompileTemplate(raw)
	}
	to, err := tmpl("to", "")
	if err != nil {
		return nil, err
	}
	from, err := tmpl("from", "")
	if err != nil {
		return nil, err
	}
	text, err := tmpl("text", "")
	if err != nil {
		return nil, err
	}
	dlr, err := tmpl("dlr", "true")
	if err != nil {
		return nil, err
	}
	ref, err := tmpl("reference", " ")
	if err != nil {
		return nil, err
	}
	optional := func(key string) (*platform.Template, error) {
		if raw, _ := spec.Config[key].(string); raw != "" {
			return platform.CompileTemplate(raw)
		}
		return nil, nil
	}
	sysTmpl, err := optional("system_id")
	if err != nil {
		return nil, err
	}
	passTmpl, err := optional("password")
	if err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	return platform.ActionFunc(func(ctx *platform.ActionContext) (platform.ActionResult, error) {
		env := platform.Env{}
		for k, v := range ctx.Inputs {
			env[k] = v
		}
		render := func(t *platform.Template) (string, error) { return t.Render(env) }
		var m Message
		var err error
		if m.To, err = render(to); err != nil {
			return platform.ActionResult{}, err
		}
		if m.From, err = render(from); err != nil {
			return platform.ActionResult{}, err
		}
		if m.Text, err = render(text); err != nil {
			return platform.ActionResult{}, err
		}
		d, err := render(dlr)
		if err != nil {
			return platform.ActionResult{}, err
		}
		m.WantDLR = d == "true" || d == "1"
		if sysTmpl != nil {
			if m.SystemID, err = render(sysTmpl); err != nil {
				return platform.ActionResult{}, err
			}
			if passTmpl != nil {
				if m.Password, err = render(passTmpl); err != nil {
					return platform.ActionResult{}, err
				}
			}
		}
		m.Reference, _ = render(ref)
		m.Reference = strings.TrimSpace(m.Reference)

		started := time.Now()
		id, f := client.Submit(ctx.Context, m)
		out := map[string]any{"ok": f == nil, "latency_ms": time.Since(started).Milliseconds(), "provider_message_id": id}
		if f != nil {
			out["error"] = map[string]any{
				"kind": f.Kind, "protocol": "smpp", "status": f.Status, "code": f.Code,
				"message": f.Message, "retry_after_s": 0,
			}
		}
		return platform.ActionResult{Outputs: map[string]any{provides: out}}, nil
	}), nil
}
