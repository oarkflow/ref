package sms

import (
	"fmt"
	"strings"
	"time"

	"github.com/oarkflow/ref/examples/smsgateway/internal/cfgdec"
)

// Duration is a BCL duration.
type Duration = cfgdec.Duration

// RetryConfig is a provider's retry policy: how often to try the provider for
// one message, and how long to wait between tries, before failing over to the
// next provider in the route.
type RetryConfig struct {
	// MaxAttempts is the number of tries on this provider (default 3). One means
	// fail over at the first error.
	MaxAttempts int      `json:"max_attempts"`
	Initial     Duration `json:"initial"`
	Max         Duration `json:"max"`
	Factor      float64  `json:"factor"`
	// Jitter spreads retries by up to this fraction of the delay (0..1).
	Jitter float64 `json:"jitter"`
}

func (r RetryConfig) withDefaults() RetryConfig {
	if r.MaxAttempts <= 0 {
		r.MaxAttempts = 3
	}
	if r.Initial == 0 {
		r.Initial = Duration(time.Second)
	}
	if r.Max == 0 {
		r.Max = Duration(time.Minute)
	}
	if r.Factor < 1 {
		r.Factor = 2
	}
	if r.Jitter < 0 || r.Jitter > 1 {
		r.Jitter = 0.2
	}
	return r
}

// Delay is the wait before attempt n+1, after n attempts (n >= 1), without
// jitter.
func (r RetryConfig) Delay(n int) time.Duration {
	r = r.withDefaults()
	d := r.Initial.D()
	for i := 1; i < n; i++ {
		d = time.Duration(float64(d) * r.Factor)
		if d >= r.Max.D() {
			return r.Max.D()
		}
	}
	return d
}

// Capabilities states what a provider supports. Every capability defaults to
// true; set it false to keep messages that need it away from the provider.
type Capabilities struct {
	Unicode       *bool `json:"unicode"`
	DLR           *bool `json:"dlr"`
	AlphaSender   *bool `json:"alpha_sender"`
	NumericSender *bool `json:"numeric_sender"`
	LongSMS       *bool `json:"long_sms"`
}

func yes(b *bool) bool { return b == nil || *b }

func (c Capabilities) unicode() bool { return yes(c.Unicode) }
func (c Capabilities) dlr() bool     { return yes(c.DLR) }
func (c Capabilities) alpha() bool   { return yes(c.AlphaSender) }
func (c Capabilities) numeric() bool { return yes(c.NumericSender) }
func (c Capabilities) long() bool    { return yes(c.LongSMS) }

// ProviderConfig is the config of a sms.gateway.* resource, or of a provider
// created at runtime. Everything specific to the upstream goes in Plugin and is
// interpreted by the gateway plugin alone.
type ProviderConfig struct {
	// Hub names the sms.hub resource the provider registers with.
	Hub string `json:"hub"`
	// Owner makes the provider private to one user: it is routed for that user
	// only, and exclusively unless Exclusive is false.
	Owner     string `json:"owner"`
	Exclusive *bool  `json:"exclusive"`
	Disabled  bool   `json:"disabled"`
	// AssignedOnly keeps the provider out of the platform's general routes: it is
	// used only for users and tenants it is assigned to (a premium route, a
	// route sold to one customer).
	AssignedOnly bool `json:"assigned_only"`

	// Countries the provider serves (ISO alpha-2). Empty means any country, at
	// the lowest routing priority: a catch-all.
	Countries []string `json:"countries"`
	// Prefixes narrow the provider to recipient number ranges, as E.164 digits
	// without the plus ("97798" for an operator's block). With prefixes set, they
	// replace the countries' dial codes in the provider's routes: this is how a
	// route is bought per operator.
	Prefixes []string `json:"prefixes"`
	// SenderCountries limits numeric sender ids to numbers registered in these
	// countries: a US long code needs a provider that may send from the US. An
	// alphanumeric sender id is not affected.
	SenderCountries []string `json:"sender_countries"`
	MessageTypes    []string `json:"message_types"`

	// Cost to the platform, in the platform currency, per segment: a default and
	// per-country overrides ("default" or a country code).
	Currency       string             `json:"currency"`
	CostPerSegment float64            `json:"cost_per_segment"`
	Cost           map[string]float64 `json:"cost"`

	// Quality is a 0..100 score and DeliveryRate a 0..1 prior; observed results
	// take over as messages flow.
	Quality      float64 `json:"quality"`
	DeliveryRate float64 `json:"delivery_rate"`
	Weight       int     `json:"weight"`
	// Priority nudges the provider within its tier; lower wins.
	Priority  int    `json:"priority"`
	Objective string `json:"objective"`

	// TPS caps submissions per second to the provider; 0 is unlimited.
	TPS int `json:"tps"`
	// Concurrency is the number of consumers working the provider's queue.
	Concurrency int `json:"concurrency"`
	// Timeout bounds one send.
	Timeout Duration `json:"timeout"`

	Retry        RetryConfig  `json:"retry"`
	Capabilities Capabilities `json:"capabilities"`

	// WebhookSecret, when set, must arrive in the X-Webhook-Secret header of the
	// provider's delivery-receipt webhook.
	WebhookSecret string `json:"webhook_secret"`

	// Plugin is passed to the gateway plugin.
	Plugin map[string]any `json:"plugin"`
}

func (c *ProviderConfig) normalize(name string) error {
	if c.Currency == "" {
		c.Currency = "USD"
	}
	for i, ct := range c.Countries {
		ct = strings.ToUpper(strings.TrimSpace(ct))
		if !KnownCountry(ct) {
			return fmt.Errorf("provider %q: unknown country %q", name, ct)
		}
		c.Countries[i] = ct
	}
	for i, ct := range c.SenderCountries {
		ct = strings.ToUpper(strings.TrimSpace(ct))
		if !KnownCountry(ct) {
			return fmt.Errorf("provider %q: unknown sender country %q", name, ct)
		}
		c.SenderCountries[i] = ct
	}
	for _, pfx := range c.Prefixes {
		if !validPrefix(pfx) {
			return fmt.Errorf("provider %q: prefix %q must be 1-15 digits", name, pfx)
		}
	}
	for ct := range c.Cost {
		if !strings.EqualFold(ct, "default") && !KnownCountry(ct) {
			return fmt.Errorf("provider %q: cost for unknown country %q", name, ct)
		}
	}
	if c.CostPerSegment < 0 {
		return fmt.Errorf("provider %q: cost_per_segment cannot be negative", name)
	}
	if c.Quality < 0 || c.Quality > 100 {
		return fmt.Errorf("provider %q: quality must be between 0 and 100", name)
	}
	if c.DeliveryRate < 0 || c.DeliveryRate > 1 {
		return fmt.Errorf("provider %q: delivery_rate must be between 0 and 1", name)
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 4
	}
	if c.Timeout == 0 {
		c.Timeout = Duration(15 * time.Second)
	}
	for _, t := range c.MessageTypes {
		if t == "" {
			return fmt.Errorf("provider %q: empty message type", name)
		}
	}
	c.Retry = c.Retry.withDefaults()
	return nil
}

func decodeProviderConfig(config map[string]any) (ProviderConfig, error) {
	var c ProviderConfig
	if err := cfgdec.Decode(config, &c); err != nil {
		return c, err
	}
	return c, nil
}

func validPrefix(p string) bool {
	if p == "" || len(p) > 15 {
		return false
	}
	for _, r := range p {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
