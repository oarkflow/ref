package smpp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oarkflow/ref/platform/spi"
)

// Ensure Client satisfies spi.SMSProvider, spi.SMSProviderHealth, and spi.Notifier.
var (
	_ spi.SMSProvider       = (*Client)(nil)
	_ spi.SMSProviderHealth = (*Client)(nil)
	_ spi.Notifier          = (*Client)(nil)
)

// ProviderID returns the configured name of the SMPP client resource.
func (c *Client) ProviderID() string {
	return c.name
}

// Submit sends one message through the SMPP session, returning a structured
// spi.SMSResult. Per the SPI contract, gateway-level refusals are recorded
// in the Result without returning a Go error.
func (c *Client) Submit(ctx context.Context, msg spi.SMSMessage) (spi.SMSResult, error) {
	m := Message{
		From:    msg.From,
		To:      msg.To,
		Text:    msg.Text,
		WantDLR: msg.DLR,
	}
	if msg.Tags != nil {
		m.Reference = msg.Tags["reference"]
		if sid := msg.Tags["system_id"]; sid != "" {
			m.SystemID = sid
		}
		if pwd := msg.Tags["password"]; pwd != "" {
			m.Password = pwd
		}
	}

	started := time.Now()
	msgID, failure := c.SubmitSMPP(ctx, m)
	latency := time.Since(started).Milliseconds()

	if failure == nil {
		if c.tracker != nil {
			c.tracker.record(true, latency)
		}
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

	if c.tracker != nil {
		c.tracker.record(false, latency)
	}

	retryable := isSMPPRetryable(failure)
	smsErr := &spi.SMSError{
		Kind:       failure.Kind,
		Protocol:   "smpp",
		StatusCode: failure.Status,
		Code:       failure.Code,
		Message:    failure.Message,
		Retryable:  retryable,
	}

	return spi.SMSResult{
		OK:         false,
		ProviderID: c.name,
		LatencyMs:  latency,
		Attempts:   1,
		Error:      smsErr,
	}, nil
}

// Health reports the provider's current observed reliability.
func (c *Client) Health(ctx context.Context) spi.SMSHealthReport {
	if c.tracker == nil {
		return spi.SMSHealthReport{
			Available:   true,
			SuccessRate: 1.0,
			OpenWindow:  c.cfg.WindowSize,
		}
	}
	return c.tracker.Report()
}

// Notify allows Client to also satisfy spi.Notifier for generic outbound routing.
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
			return "", fmt.Errorf("smpp notification failed: %s (%s)", res.Error.Message, res.Error.Code)
		}
		return "", fmt.Errorf("smpp notification failed")
	}
	return res.ProviderMsgID, nil
}

func isSMPPRetryable(f *Failure) bool {
	if f == nil {
		return false
	}
	if f.Kind == "transport" || f.Kind == "timeout" {
		return true
	}
	// Common transient SMPP status codes:
	// ESME_RTHROTTLED (0x58), ESME_RMSGQFUL (0x14), ESME_RSYSERR (0x08)
	switch f.Code {
	case "ESME_RTHROTTLED", "ESME_RMSGQFUL", "ESME_RSYSERR", "THROTTLED", "CONGESTION":
		return true
	}
	switch f.Status {
	case 0x00000058, 0x00000014, 0x00000008:
		return true
	}
	return false
}

// calculateSegments calculates the number of SMS segments required based on character encoding.
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
		// Auto-detect: if all runes fit in basic ASCII / GSM-7 range
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

	// UCS-2: 70 chars for single segment, 67 chars per concatenated segment
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
		capacity = 16
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

	// Prune older than window
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
		Available:    successRate > 0.1 || total < 5, // Mark unavailable only if high failure volume
		SuccessRate:  successRate,
		AvgLatencyMs: avgLatency,
		OpenWindow:   ht.openWindow,
	}
}

// Suppress unused warning for utf8
var _ = utf8.RuneCountInString
