package sim

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	cfgdec "github.com/oarkflow/ref/contrib/messaging/internal/cfg"
	"github.com/oarkflow/ref/platform"
)

var registerOnce sync.Once

// Register installs the sandbox.upstreams resource: the stand-in carrier and
// vendors of this package, started from BCL so that an application can run
// end to end with nothing external and no environment variables.
//
//	resource "sandbox" {
//	  kind "sandbox.upstreams"
//	  config {
//	    enabled        true
//	    smpp_addr      "127.0.0.1:2775"    system_id "smsgw"  password "sandbox"
//	    vendor_addr    "127.0.0.1:9100"    token "sandbox-token"
//	    callback_url   "http://127.0.0.1:8080/v1/webhooks/dlr/{provider}"
//	    callback_secret "webhook-secret-change-me"
//	  }
//	}
func Register() {
	registerOnce.Do(func() {
		platform.RegisterResourceDriver("sandbox.upstreams", platform.ResourceFactoryFunc(openSandbox), platform.ResourceKindInfo{
			Family:   "service",
			Summary:  "Stand-in SMPP carrier and vendor HTTP APIs for local runs and tests. Does nothing when enabled is false.",
			Provides: []string{"Sandbox"},
			Config: []platform.ConfigField{
				{Name: "enabled", Type: "bool", Default: "true"},
				{Name: "smpp_addr", Type: "string", Default: "127.0.0.1:2775"},
				{Name: "system_id", Type: "string", Default: "smsgw"},
				{Name: "password", Type: "string", Default: "sandbox"},
				{Name: "vendor_addr", Type: "string", Default: "127.0.0.1:9100"},
				{Name: "token", Type: "string", Default: "sandbox-token"},
				{Name: "callback_url", Type: "string", Summary: "Where vendors post receipts; {provider} is the path prefix the vendor was called under"},
				{Name: "callback_secret", Type: "string"},
				{Name: "dlr_delay", Type: "duration", Default: "100ms"},
			},
		})
	})
}

type sandboxConfig struct {
	Enabled        *cfgdec.Bool    `json:"enabled"`
	SMPPAddr       string          `json:"smpp_addr"`
	SystemID       string          `json:"system_id"`
	Password       string          `json:"password"`
	VendorAddr     string          `json:"vendor_addr"`
	Token          string          `json:"token"`
	CallbackURL    string          `json:"callback_url"`
	CallbackSecret string          `json:"callback_secret"`
	DLRDelay       cfgdec.Duration `json:"dlr_delay"`
}

// Sandbox is the running stand-ins.
type Sandbox struct {
	SMSC   *SMSC
	Vendor *Vendor
}

type sandboxCloser struct{ s *Sandbox }

func (c sandboxCloser) Close() error {
	if c.s.SMSC != nil {
		c.s.SMSC.Close()
	}
	if c.s.Vendor != nil {
		c.s.Vendor.Close()
	}
	return nil
}

func openSandbox(_ context.Context, spec platform.ResourceSpec) (platform.Resource, io.Closer, error) {
	var cfg sandboxConfig
	if err := cfgdec.Decode(spec.Config, &cfg); err != nil {
		return nil, nil, fmt.Errorf("sandbox.upstreams %q: %w", spec.Name, err)
	}
	box := &Sandbox{}
	if cfg.Enabled != nil && !bool(*cfg.Enabled) {
		return box, io.NopCloser(nil2{}), nil
	}
	def := func(v *string, d string) {
		if *v == "" {
			*v = d
		}
	}
	def(&cfg.SMPPAddr, "127.0.0.1:2775")
	def(&cfg.SystemID, "smsgw")
	def(&cfg.Password, "sandbox")
	def(&cfg.VendorAddr, "127.0.0.1:9100")
	def(&cfg.Token, "sandbox-token")
	delay := cfg.DLRDelay.D()
	if delay == 0 {
		delay = 100 * time.Millisecond
	}
	smsc, err := StartSMSC(SMSCConfig{Addr: cfg.SMPPAddr, SystemID: cfg.SystemID, Password: cfg.Password, DLRDelay: delay})
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox.upstreams %q: smpp: %w", spec.Name, err)
	}
	vendor, err := StartVendorOn(cfg.VendorAddr, cfg.Token)
	if err != nil {
		smsc.Close()
		return nil, nil, fmt.Errorf("sandbox.upstreams %q: vendor: %w", spec.Name, err)
	}
	vendor.Delay = delay
	if cfg.CallbackURL != "" {
		vendor.SetCallback(cfg.CallbackURL, cfg.CallbackSecret)
	}
	box.SMSC, box.Vendor = smsc, vendor
	return box, sandboxCloser{box}, nil
}

type nil2 struct{}

func (nil2) Read([]byte) (int, error) { return 0, io.EOF }
