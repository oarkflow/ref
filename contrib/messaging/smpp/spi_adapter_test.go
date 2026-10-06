package smpp

import (
	"context"
	"testing"

	"github.com/oarkflow/ref/platform/spi"
)

func TestCalculateSegments(t *testing.T) {
	// Standard GSM short
	if segs := calculateSegments("Hello world", "gsm7"); segs != 1 {
		t.Fatalf("expected 1 segment, got %d", segs)
	}

	// 160 chars GSM
	ascii160 := ""
	for i := 0; i < 160; i++ {
		ascii160 += "a"
	}
	if segs := calculateSegments(ascii160, "gsm7"); segs != 1 {
		t.Fatalf("expected 1 segment for 160 chars, got %d", segs)
	}

	// 161 chars GSM -> 2 segments (153 + 8)
	if segs := calculateSegments(ascii160+"b", "gsm7"); segs != 2 {
		t.Fatalf("expected 2 segments for 161 chars, got %d", segs)
	}

	// UCS-2: 70 chars
	unicode70 := ""
	for i := 0; i < 70; i++ {
		unicode70 += "न"
	}
	if segs := calculateSegments(unicode70, "auto"); segs != 1 {
		t.Fatalf("expected 1 segment for 70 unicode chars, got %d", segs)
	}
	if segs := calculateSegments(unicode70+"प", "auto"); segs != 2 {
		t.Fatalf("expected 2 segments for 71 unicode chars, got %d", segs)
	}
}

func TestHealthTracker(t *testing.T) {
	ht := newHealthTracker(10)
	rep := ht.Report()
	if !rep.Available || rep.SuccessRate != 1.0 {
		t.Fatalf("expected initial healthy report, got %+v", rep)
	}

	ht.record(true, 50)
	ht.record(true, 70)
	ht.record(false, 30)

	rep = ht.Report()
	if rep.SuccessRate < 0.65 || rep.SuccessRate > 0.67 {
		t.Fatalf("expected ~0.66 success rate, got %f", rep.SuccessRate)
	}
	if rep.AvgLatencyMs != 50 {
		t.Fatalf("expected 50ms avg latency, got %d", rep.AvgLatencyMs)
	}
}

func TestRetryableClassification(t *testing.T) {
	if !isSMPPRetryable(&Failure{Kind: "timeout"}) {
		t.Fatal("expected timeout to be retryable")
	}
	if !isSMPPRetryable(&Failure{Code: "ESME_RTHROTTLED"}) {
		t.Fatal("expected throttled to be retryable")
	}
	if !isSMPPRetryable(&Failure{Status: 0x00000058}) {
		t.Fatal("expected 0x58 to be retryable")
	}
	if isSMPPRetryable(&Failure{Code: "ESME_RINVPASWD"}) {
		t.Fatal("invalid password should not be retryable")
	}
}

func TestClientSatisfiesInterfaces(t *testing.T) {
	var c *Client = &Client{name: "test_smpp", tracker: newHealthTracker(16)}
	if c.ProviderID() != "test_smpp" {
		t.Fatalf("expected provider ID 'test_smpp', got %q", c.ProviderID())
	}
	h := c.Health(context.Background())
	if !h.Available {
		t.Fatalf("expected client to be healthy")
	}
	var _ spi.SMSProvider = c
	var _ spi.SMSProviderHealth = c
	var _ spi.Notifier = c
}
