package httpsms

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	cfgdec "github.com/oarkflow/ref/contrib/messaging/internal/cfg"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/platform/spi"
)

var registerOnce sync.Once

// Register installs the service.httpsms resource provider.
func Register() {
	registerOnce.Do(func() {
		platform.RegisterResourceDriver("service.httpsms", platform.ResourceFactoryFunc(open), platform.ResourceKindInfo{
			Family:   "service",
			Summary:  "An HTTP SMS gateway client (Twilio, Vonage, MessageBird, Infobip, AWS SNS, or Custom REST).",
			Provides: []string{"SMSProvider", "Notifier"},
			Config: []platform.ConfigField{
				{Name: "provider", Type: "string", Required: true, Summary: "twilio | vonage | messagebird | infobip | aws_sns | custom"},
				{Name: "base_url", Type: "string", Summary: "Base endpoint of the HTTP provider"},
				{Name: "submit_url", Type: "string", Summary: "Explicit URL for custom gateway submit endpoint"},
				{Name: "account_sid", Type: "string"},
				{Name: "auth_token", Type: "string"},
				{Name: "api_key", Type: "string"},
				{Name: "api_secret", Type: "string"},
				{Name: "from", Type: "string", Summary: "Default sender ID or alphanumeric source"},
				{Name: "timeout", Type: "duration", Default: "10s"},
				{Name: "queue", Type: "resource", Summary: "Queue that receives delivery receipts"},
				{Name: "receipt_job", Type: "string", Summary: "Job type of a receipt"},
				{Name: "callback_url", Type: "string", Summary: "Webhook URL to receive status callbacks"},
				{Name: "method", Type: "string", Default: "POST"},
				{Name: "body_type", Type: "string", Default: "json"},
				{Name: "to_field", Type: "string", Default: "to"},
				{Name: "from_field", Type: "string", Default: "from"},
				{Name: "text_field", Type: "string", Default: "text"},
			},
		})
	})
}

// Config defines the configuration fields for service.httpsms.
type Config struct {
	Provider    string            `json:"provider"`
	BaseURL     string            `json:"base_url"`
	SubmitURL   string            `json:"submit_url"`
	Method      string            `json:"method"`
	AccountSID  string            `json:"account_sid"`
	AuthToken   string            `json:"auth_token"`
	APIKey      string            `json:"api_key"`
	APISecret   string            `json:"api_secret"`
	From        string            `json:"from"`
	Timeout     cfgdec.Duration   `json:"timeout"`
	Headers     map[string]string `json:"headers"`
	CallbackURL string            `json:"callback_url"`
	Queue       string            `json:"queue"`
	ReceiptJob  string            `json:"receipt_job"`

	// Custom format options
	BodyType    string `json:"body_type"`
	ToField     string `json:"to_field"`
	FromField   string `json:"from_field"`
	TextField   string `json:"text_field"`
	IDField     string `json:"id_field"`
	StatusField string `json:"status_field"`
}

// Client implements spi.SMSProvider and spi.SMSProviderHealth over HTTP.
type Client struct {
	name       string
	cfg        Config
	handler    ProviderHandler
	httpClient *http.Client
	tracker    *healthTracker
	publish    func(job string, payload any) error
}

var (
	_ spi.SMSProvider       = (*Client)(nil)
	_ spi.SMSProviderHealth = (*Client)(nil)
	_ spi.Notifier          = (*Client)(nil)
	_ io.Closer             = (*Client)(nil)
)

func open(_ context.Context, spec platform.ResourceSpec) (platform.Resource, io.Closer, error) {
	var cfg Config
	if err := cfgdec.Decode(spec.Config, &cfg); err != nil {
		return nil, nil, fmt.Errorf("service.httpsms %q: %w", spec.Name, err)
	}

	if cfg.Provider == "" {
		return nil, nil, fmt.Errorf("service.httpsms %q: provider is required (twilio, vonage, aws_sns, etc.)", spec.Name)
	}

	handler := GetProviderHandler(cfg.Provider)
	timeout := cfg.Timeout.D()
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	c := &Client{
		name:    spec.Name,
		cfg:     cfg,
		handler: handler,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		tracker: newHealthTracker(100),
	}

	if cfg.Queue != "" {
		if cfg.ReceiptJob == "" {
			return nil, nil, fmt.Errorf("service.httpsms %q: receipt_job is required with queue", spec.Name)
		}
		res, ok := spec.Dependency("queue")
		if !ok {
			return nil, nil, fmt.Errorf("service.httpsms %q: queue %q is not a declared resource", spec.Name, cfg.Queue)
		}
		q, ok := res.(interface {
			Enqueue(string, any, ...map[string]string) (string, error)
		})
		if !ok {
			return nil, nil, fmt.Errorf("service.httpsms %q: resource %q is not a job queue", spec.Name, cfg.Queue)
		}
		c.publish = func(job string, payload any) error { _, err := q.Enqueue(job, payload); return err }
	}

	return c, c, nil
}

// ProviderID returns the configured resource name.
func (c *Client) ProviderID() string {
	return c.name
}

// Submit sends one SMS via HTTP and returns a structured spi.SMSResult.
func (c *Client) Submit(ctx context.Context, msg spi.SMSMessage) (spi.SMSResult, error) {
	req, err := c.handler.BuildRequest(ctx, c.cfg, msg)
	if err != nil {
		return spi.SMSResult{
			OK:         false,
			ProviderID: c.name,
			Attempts:   1,
			Error: &spi.SMSError{
				Kind:      "invalid_request",
				Protocol:  "http",
				Message:   err.Error(),
				Retryable: false,
			},
		}, nil
	}

	started := time.Now()
	resp, err := c.httpClient.Do(req)
	latency := time.Since(started).Milliseconds()

	if err != nil {
		c.tracker.record(false, latency)
		retryable := ctx.Err() == nil // network/transport issue is retryable unless cancelled
		return spi.SMSResult{
			OK:         false,
			ProviderID: c.name,
			LatencyMs:  latency,
			Attempts:   1,
			Error: &spi.SMSError{
				Kind:      "transport",
				Protocol:  "http",
				Message:   err.Error(),
				Retryable: retryable,
			},
		}, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	msgID, smsErr, parseErr := c.handler.ParseResponse(resp, body)
	if parseErr != nil {
		c.tracker.record(false, latency)
		return spi.SMSResult{
			OK:         false,
			ProviderID: c.name,
			LatencyMs:  latency,
			Attempts:   1,
			Error: &spi.SMSError{
				Kind:      "parse_error",
				Protocol:  "http",
				Message:   parseErr.Error(),
				Retryable: false,
			},
		}, nil
	}

	if smsErr != nil {
		c.tracker.record(false, latency)
		return spi.SMSResult{
			OK:         false,
			ProviderID: c.name,
			LatencyMs:  latency,
			Attempts:   1,
			Error:      smsErr,
		}, nil
	}

	c.tracker.record(true, latency)
	segs := calculateSegments(msg.Text, msg.Encoding)
	return spi.SMSResult{
		OK:            true,
		ProviderMsgID: msgID,
		ProviderID:    c.name,
		Segments:      segs,
		LatencyMs:     latency,
		Attempts:      1,
	}, nil
}

// Health reports the provider's current observed reliability snapshot.
func (c *Client) Health(ctx context.Context) spi.SMSHealthReport {
	if c.tracker == nil {
		return spi.SMSHealthReport{
			Available:   true,
			SuccessRate: 1.0,
			OpenWindow:  100,
		}
	}
	return c.tracker.Report()
}

// Notify satisfies spi.Notifier.
func (c *Client) Notify(ctx context.Context, msg spi.Notification) (string, error) {
	res, err := c.Submit(ctx, spi.SMSMessage{
		To:   msg.Target,
		Text: msg.Body,
	})
	if err != nil {
		return "", err
	}
	if !res.OK {
		if res.Error != nil {
			return "", fmt.Errorf("httpsms notify error: %s (%s)", res.Error.Message, res.Error.Code)
		}
		return "", fmt.Errorf("httpsms notify failed")
	}
	return res.ProviderMsgID, nil
}

// Close closes any idle HTTP connections.
func (c *Client) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

// calculateSegments calculates the number of SMS segments.
func calculateSegments(text string, encoding string) int {
	runes := []rune(text)
	n := len(runes)
	if n == 0 {
		return 1
	}

	isGSM := true
	if strings.EqualFold(encoding, "ucs2") || strings.EqualFold(encoding, "utf16") {
		isGSM = false
	} else if strings.EqualFold(encoding, "gsm7") {
		isGSM = true
	} else {
		for _, r := range runes {
			if r > 127 {
				isGSM = false
				break
			}
		}
	}

	if isGSM {
		if n <= 160 {
			return 1
		}
		return (n + 152) / 153
	}

	if n <= 70 {
		return 1
	}
	return (n + 66) / 67
}

// ---------------------------------------------------------------------------
// Health Tracker (Rolling Window)
// ---------------------------------------------------------------------------

type sample struct {
	at        time.Time
	success   bool
	latencyMs int64
}

type healthTracker struct {
	mu         sync.RWMutex
	samples    []sample
	window     time.Duration
	openWindow int
}

func newHealthTracker(capacity int) *healthTracker {
	if capacity <= 0 {
		capacity = 100
	}
	return &healthTracker{
		samples:    make([]sample, 0, 100),
		window:     5 * time.Minute,
		openWindow: capacity,
	}
}

func (ht *healthTracker) record(success bool, latencyMs int64) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	now := time.Now()
	ht.samples = append(ht.samples, sample{
		at:        now,
		success:   success,
		latencyMs: latencyMs,
	})

	cutoff := now.Add(-ht.window)
	start := 0
	for start < len(ht.samples) && ht.samples[start].at.Before(cutoff) {
		start++
	}
	if start > 0 {
		ht.samples = ht.samples[start:]
	}
}

func (ht *healthTracker) Report() spi.SMSHealthReport {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	now := time.Now()
	cutoff := now.Add(-ht.window)

	var total, successes int
	var totalLatency int64

	for _, s := range ht.samples {
		if s.at.After(cutoff) {
			total++
			if s.success {
				successes++
			}
			totalLatency += s.latencyMs
		}
	}

	if total == 0 {
		return spi.SMSHealthReport{
			Available:    true,
			SuccessRate:  1.0,
			AvgLatencyMs: 0,
			OpenWindow:   ht.openWindow,
		}
	}

	successRate := float64(successes) / float64(total)
	avgLatency := totalLatency / int64(total)

	return spi.SMSHealthReport{
		Available:    successRate > 0.1 || total < 5,
		SuccessRate:  successRate,
		AvgLatencyMs: avgLatency,
		OpenWindow:   ht.openWindow,
	}
}
