package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/smppflow/pkg/events"
	"github.com/oarkflow/smppflow/pkg/lifecycle"
	"github.com/oarkflow/smppflow/pkg/message"
	"github.com/oarkflow/smppflow/pkg/protocol"
	"github.com/oarkflow/smppflow/pkg/session"

	"github.com/oarkflow/ref/examples/smsgateway/internal/cfgdec"
)

func init() { Register("smpp", openSMPP) }

type smppConfig struct {
	Addr            string          `json:"addr"`
	SystemID        string          `json:"system_id"`
	Password        string          `json:"password"`
	SystemType      string          `json:"system_type"`
	BindMode        string          `json:"bind_mode"` // transceiver (default), transmitter
	Version         string          `json:"version"`   // 3.3, 3.4 (default), 5.0
	WindowSize      int             `json:"window_size"`
	EnquireInterval cfgdec.Duration `json:"enquire_interval"`
	ResponseTimeout cfgdec.Duration `json:"response_timeout"`
	ConnectTimeout  cfgdec.Duration `json:"connect_timeout"`
	SourceTON       int             `json:"source_ton"`
	SourceNPI       int             `json:"source_npi"`
	DestTON         int             `json:"dest_ton"`
	DestNPI         int             `json:"dest_npi"`
	UseSAR          bool            `json:"use_sar"`
}

// SMPP delivers through an SMPP bind using smppflow's session and message
// layers: connection management, windowing, enquire_link, segmentation and
// DLR parsing are smppflow's. This plugin adds lazy (re)binding, error
// classification and the receipt bridge.
type SMPP struct {
	name string
	cfg  smppConfig

	mu       sync.Mutex
	client   *session.Client
	manager  *message.Manager
	bus      *events.MemoryBus
	stopRecv context.CancelFunc
	sink     DLRSink
	taxonomy *message.ErrorTaxonomy
}

func openSMPP(_ context.Context, name string, config map[string]any) (Gateway, error) {
	var cfg smppConfig
	if err := cfgdec.Decode(config, &cfg); err != nil {
		return nil, fmt.Errorf("smpp gateway %q: %w", name, err)
	}
	if cfg.Addr == "" || cfg.SystemID == "" {
		return nil, fmt.Errorf("smpp gateway %q: addr and system_id are required", name)
	}
	switch cfg.BindMode {
	case "", "transceiver", "transmitter":
	default:
		return nil, fmt.Errorf("smpp gateway %q: bind_mode must be transceiver or transmitter", name)
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
	return &SMPP{name: name, cfg: cfg, taxonomy: message.NewErrorTaxonomy()}, nil
}

// SetDLRSink implements DLRSource.
func (g *SMPP) SetDLRSink(s DLRSink) {
	g.mu.Lock()
	g.sink = s
	g.mu.Unlock()
}

func (g *SMPP) version() protocol.Version {
	switch g.cfg.Version {
	case "3.3":
		return protocol.Version33
	case "5.0":
		return protocol.Version50
	default:
		return protocol.Version34
	}
}

// discardStore satisfies message.Store without keeping anything: the
// application's own message table is the system of record, so smppflow's
// per-message bookkeeping would only be a second, unbounded copy.
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

// connect returns a bound session, binding if there is none.
func (g *SMPP) connect(ctx context.Context) (*message.Manager, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.manager != nil && g.client != nil {
		return g.manager, nil
	}
	mode := protocol.BindModeTransceiver
	if g.cfg.BindMode == "transmitter" {
		mode = protocol.BindModeTransmitter
	}
	bus := events.NewMemoryBus(true)
	client := session.NewClient(session.Config{
		Addr: g.cfg.Addr,
		Bind: protocol.BindRequest{
			Mode: mode, SystemID: g.cfg.SystemID, Password: g.cfg.Password,
			SystemType: g.cfg.SystemType, InterfaceVersion: g.version(),
		},
		WindowSize:      g.cfg.WindowSize,
		EnquireInterval: g.cfg.EnquireInterval.D(),
		ResponseTimeout: g.cfg.ResponseTimeout.D(),
		Bus:             bus,
	})
	cctx, cancel := context.WithTimeout(ctx, g.cfg.ConnectTimeout.D())
	defer cancel()
	if err := client.Connect(cctx); err != nil {
		return nil, &Error{Class: ClassRetryable, Code: "connect", Message: err.Error(), Cause: err}
	}
	if err := client.Bind(cctx); err != nil {
		_ = client.Close(context.Background())
		return nil, g.classify(err)
	}
	mgr := message.NewManager(message.ManagerConfig{
		Client: client, Store: discardStore{}, Bus: bus,
		DefaultSourceTON: protocol.TON(g.cfg.SourceTON), DefaultSourceNPI: protocol.NPI(g.cfg.SourceNPI),
		DefaultDestTON: protocol.TON(g.cfg.DestTON), DefaultDestNPI: protocol.NPI(g.cfg.DestNPI),
		ProviderID: g.name, Taxonomy: g.taxonomy, UseSAR: g.cfg.UseSAR,
	})
	rctx, stop := context.WithCancel(context.Background())
	mgr.StartInbound(rctx)
	bus.Subscribe(events.DLRReceived, func(ctx context.Context, e events.Event) error {
		d, ok := e.Data.(message.DeliveryReceipt)
		if !ok {
			return nil
		}
		g.mu.Lock()
		sink := g.sink
		g.mu.Unlock()
		if sink != nil {
			sink(ctx, g.convert(d))
		}
		return nil
	})
	g.client, g.manager, g.bus, g.stopRecv = client, mgr, bus, stop
	return mgr, nil
}

func (g *SMPP) convert(d message.DeliveryReceipt) DLR {
	st := StatusUnknown
	switch d.State {
	case lifecycle.MsgDelivered:
		st = StatusDelivered
	case lifecycle.MsgFailed, lifecycle.MsgRejected:
		st = StatusFailed
	case lifecycle.MsgExpired:
		st = StatusExpired
	}
	at := d.DoneAt
	if at.IsZero() {
		at = d.ReceivedAt
	}
	return DLR{Provider: g.name, ProviderMessageID: d.ProviderMessageID, MessageID: d.MessageID,
		Status: st, Code: d.ErrorCode, Raw: d.Raw, At: at}
}

// drop discards a session that failed, so the next Send rebinds.
func (g *SMPP) drop() {
	g.mu.Lock()
	client, stop := g.client, g.stopRecv
	g.client, g.manager, g.bus, g.stopRecv = nil, nil, nil, nil
	g.mu.Unlock()
	if stop != nil {
		stop()
	}
	if client != nil {
		_ = client.Close(context.Background())
	}
}

// smppStatuses maps the status names smppflow puts in its errors (it prints
// INVALID_PASSWORD, and a bare hex code for a status it has no name for) and
// the standard ESME_R names onto how the pipeline should react.
var smppStatuses = []struct {
	class  Class
	code   string
	tokens []string
}{
	// The destination is at fault: no provider will do better.
	{ClassPermanent, "invalid_destination", []string{"INVALID_DESTINATION_ADDRESS", "ESME_RINVDSTADR", "0X0000000B", "ESME_RINVNUMDESTS", "0X00000033", "ESME_RINVMSGLEN", "0X00000001"}},
	// The provider refuses us, but another provider may accept the message.
	{ClassProvider, "provider_refused", []string{"INVALID_PASSWORD", "ESME_RINVPASWD", "0X0000000E", "INVALID_SYSTEM_ID", "ESME_RINVSYSID", "0X0000000F",
		"BIND_FAILED", "ESME_RBINDFAIL", "0X0000000D", "ESME_RINVBNDSTS", "0X00000004", "INVALID_SOURCE_ADDRESS", "ESME_RINVSRCADR", "0X0000000A",
		"ESME_RINVSRCTON", "0X00000048", "ESME_RINVSERTYP", "0X00000015", "ESME_RINVESMCLASS", "0X00000043"}},
	// The provider is busy: back off and retry it.
	{ClassThrottled, "throttled", []string{"THROTTLED", "ESME_RTHROTTLED", "0X00000058", "ESME_RMSGQFUL", "0X00000014"}},
}

// classify maps an smppflow error onto a delivery class.
func (g *SMPP) classify(err error) *Error {
	s := strings.ToUpper(err.Error())
	for _, st := range smppStatuses {
		for _, tok := range st.tokens {
			if strings.Contains(s, tok) {
				return &Error{Class: st.class, Code: strings.ToLower(tok), Message: err.Error(), Cause: err}
			}
		}
	}
	c := g.taxonomy.Classify("", err)
	switch c.Class {
	case message.ErrInvalidAddress, message.ErrBlockedContent:
		return &Error{Class: ClassPermanent, Code: c.Code, Message: err.Error(), Cause: err}
	case message.ErrThrottled:
		return &Error{Class: ClassThrottled, Code: c.Code, Message: err.Error(), Cause: err}
	case message.ErrAuth:
		return &Error{Class: ClassProvider, Code: c.Code, Message: err.Error(), Cause: err}
	}
	return &Error{Class: ClassRetryable, Code: "smpp", Message: err.Error(), Cause: err}
}

// Send implements Gateway.
func (g *SMPP) Send(ctx context.Context, msg Message) (Receipt, error) {
	start := time.Now()
	mgr, err := g.connect(ctx)
	if err != nil {
		return Receipt{}, err
	}
	reg := byte(0)
	if msg.WantDLR {
		reg = 1
	}
	res, err := mgr.Submit(ctx, message.SubmitRequest{
		ProviderID: g.name, From: msg.From, To: msg.To, Text: msg.Text,
		RegisteredDelivery: reg, UseSAR: g.cfg.UseSAR,
		ExpiresAt: msg.ExpiresAt, Meta: map[string]string{"message_id": msg.ID},
	})
	if err != nil {
		ge := g.classify(err)
		// A session that failed in transport may be dead; rebind next time. A
		// protocol-level refusal leaves the session healthy.
		if ge.Class == ClassRetryable {
			g.drop()
		}
		return Receipt{}, ge
	}
	if len(res.ProviderIDs) == 0 {
		return Receipt{}, Errorf(ClassRetryable, "no_receipt", "smpp upstream accepted the message without an id")
	}
	return Receipt{ProviderMessageID: res.ProviderIDs[0], Latency: time.Since(start)}, nil
}

// Ping implements Pinger by checking the bind.
func (g *SMPP) Ping(ctx context.Context) error {
	_, err := g.connect(ctx)
	return err
}

// Close implements Gateway.
func (g *SMPP) Close() error {
	g.drop()
	return nil
}
