package sms

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oarkflow/ref/examples/smsgateway/internal/gateway"
)

const price = 20_000 // 0.02 per segment

func TestDeliveredMessageIsChargedOnce(t *testing.T) {
	r := newRig(t)
	gw := r.provider("np1", np(ProviderConfig{CostPerSegment: 0.01}), map[string]any{"dlr_delay": "5ms"})
	r.user("acme", 1.0)
	r.start()

	res, err := r.send("acme", SendRequest{To: npNumber, Text: "Your code is 123456", Type: "otp"})
	if err != nil {
		t.Fatal(err)
	}
	m := r.waitState(res.Message.ID, StateDelivered)

	if gw.Count(m.ID) != 1 {
		t.Fatalf("provider received the message %d times", gw.Count(m.ID))
	}
	b := r.balance("acme")
	if b.Balance != 1_000_000-price || b.Held != 0 {
		t.Fatalf("balance = %+v, want %d owned and nothing held", b, 1_000_000-price)
	}
	if st, _ := r.hub.Store.HoldState(context.Background(), m.ID); st != "captured" {
		t.Fatalf("hold is %q, want captured", st)
	}
	if m.Text != "" {
		t.Fatal("the text of a finished message must be erased")
	}
	if m.CostMicros != 10_000 || m.PriceMicros != price {
		t.Fatalf("cost/price = %d/%d", m.CostMicros, m.PriceMicros)
	}
}

func TestSameReferenceNeverChargesTwice(t *testing.T) {
	r := newRig(t)
	gw := r.provider("np1", np(ProviderConfig{}), nil)
	r.user("acme", 1.0)
	r.start()

	first, err := r.send("acme", SendRequest{To: npNumber, Text: "hello", Reference: "order-1"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]AcceptResult, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = r.send("acme", SendRequest{To: npNumber, Text: "hello", Reference: "order-1"})
		}()
	}
	wg.Wait()
	for i, res := range results {
		if res.Message == nil || res.Message.ID != first.Message.ID || !res.Duplicate {
			t.Fatalf("replay %d = %+v, want the first message flagged duplicate", i, res)
		}
	}
	r.waitState(first.Message.ID, StateDelivered, StateSubmitted)
	if gw.Count(first.Message.ID) != 1 {
		t.Fatalf("delivered %d times", gw.Count(first.Message.ID))
	}
	if b := r.balance("acme"); b.Balance+b.Held != 1_000_000-price {
		t.Fatalf("charged %d, want exactly one message", 1_000_000-b.Balance-b.Held)
	}

	_, err = r.send("acme", SendRequest{To: npNumber, Text: "a different text", Reference: "order-1"})
	var f *Fault
	if !errors.As(err, &f) || f.Code != "REFERENCE_REUSED" {
		t.Fatalf("a reused reference with another payload must conflict, got %v", err)
	}
}

func TestInsufficientFundsStoresNothing(t *testing.T) {
	r := newRig(t)
	gw := r.provider("np1", np(ProviderConfig{}), nil)
	r.user("poor", 0.01)
	r.start()

	_, err := r.send("poor", SendRequest{To: npNumber, Text: "hello"})
	var f *Fault
	if !errors.As(err, &f) || f.Kind != FaultFunds || f.Code != "INSUFFICIENT_FUNDS" {
		t.Fatalf("err = %v, want INSUFFICIENT_FUNDS", err)
	}
	ms, _ := r.hub.Store.ListMessages(context.Background(), "poor", "", 10)
	if len(ms) != 0 || len(gw.Sent()) != 0 {
		t.Fatalf("an unpaid message must not exist: stored %d, sent %d", len(ms), len(gw.Sent()))
	}
	if b := r.balance("poor"); b.Held != 0 || b.Balance != 10_000 {
		t.Fatalf("balance = %+v", b)
	}
}

func TestConcurrentAcceptsNeverOverspend(t *testing.T) {
	r := newRig(t)
	r.provider("np1", np(ProviderConfig{}), nil)
	r.user("acme", 0.02*20) // exactly 20 messages
	r.start()

	var ok, funds atomic.Int32
	var wg sync.WaitGroup
	for range 60 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.send("acme", SendRequest{To: npNumber, Text: "hello"})
			var f *Fault
			switch {
			case err == nil:
				ok.Add(1)
			case errors.As(err, &f) && f.Kind == FaultFunds:
				funds.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 20 || funds.Load() != 40 {
		t.Fatalf("accepted %d, refused %d; want 20 and 40", ok.Load(), funds.Load())
	}
	eventually(t, "all messages settled", func() bool {
		b := r.balance("acme")
		return b.Held == 0 && b.Balance == 0
	})
}

func TestRetryOnTheSameProviderThenDelivered(t *testing.T) {
	r := newRig(t)
	gw := r.provider("np1", np(ProviderConfig{}), nil)
	r.user("acme", 1.0)
	gw.FailNext(2, gateway.Errorf(gateway.ClassRetryable, "http_503", "upstream unavailable"))
	r.start()

	res, _ := r.send("acme", SendRequest{To: npNumber, Text: "hello"})
	m := r.waitState(res.Message.ID, StateSubmitted, StateDelivered)
	if m.TotalAttempts != 3 || m.Provider != "np1" || gw.Count(m.ID) != 1 {
		t.Fatalf("attempts=%d provider=%s deliveries=%d, want 3 attempts, np1, 1 delivery", m.TotalAttempts, m.Provider, gw.Count(m.ID))
	}
	atts, _ := r.hub.Store.Attempts(context.Background(), m.ID)
	if len(atts) != 3 || atts[0].Outcome != "retry" || atts[2].Outcome != "accepted" {
		t.Fatalf("attempt log = %+v", atts)
	}
	if b := r.balance("acme"); b.Balance != 1_000_000-price || b.Held != 0 {
		t.Fatalf("balance = %+v after retries", b)
	}
	if r.hub.Counters.Retries.Load() != 2 {
		t.Fatalf("retries counted = %d", r.hub.Counters.Retries.Load())
	}
}

func TestFailoverToTheNextProvider(t *testing.T) {
	r := newRig(t)
	primary := r.provider("cheap", np(ProviderConfig{CostPerSegment: 0.005, Quality: 90}), nil)
	backup := r.provider("backup", np(ProviderConfig{CostPerSegment: 0.015, Quality: 90}), nil)
	r.user("acme", 1.0)
	primary.SetDown(true)
	r.start()

	res, err := r.send("acme", SendRequest{To: npNumber, Text: "hello", Type: "promotional"})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Message.Plan; len(got) != 2 || got[0].Provider != "cheap" {
		t.Fatalf("a promotional message should try the cheapest provider first, plan = %+v", got)
	}
	m := r.waitState(res.Message.ID, StateSubmitted, StateDelivered)
	if m.Provider != "backup" || backup.Count(m.ID) != 1 || len(primary.Sent()) != 0 {
		t.Fatalf("provider=%s backup=%d primary=%d", m.Provider, backup.Count(m.ID), len(primary.Sent()))
	}
	if m.CostMicros != 15_000 {
		t.Fatalf("cost = %d, want the backup's price", m.CostMicros)
	}
	if b := r.balance("acme"); b.Balance != 1_000_000-price {
		t.Fatalf("the user pays one price however many providers were tried: %+v", b)
	}
	if r.hub.Counters.Failovers.Load() != 1 {
		t.Fatalf("failovers = %d", r.hub.Counters.Failovers.Load())
	}
}

func TestProviderFaultFailsOverWithoutRetrying(t *testing.T) {
	r := newRig(t)
	bad := r.provider("badauth", np(ProviderConfig{Quality: 99}), nil)
	good := r.provider("good", np(ProviderConfig{Quality: 80}), nil)
	r.user("acme", 1.0)
	bad.FailNext(10, gateway.Errorf(gateway.ClassProvider, "auth", "invalid credentials"))
	r.start()

	res, _ := r.send("acme", SendRequest{To: npNumber, Text: "hello", Type: "otp"})
	m := r.waitState(res.Message.ID, StateSubmitted, StateDelivered)
	if m.Provider != "good" || good.Count(m.ID) != 1 {
		t.Fatalf("provider = %s", m.Provider)
	}
	if m.TotalAttempts != 2 {
		t.Fatalf("a provider fault must fail over at once: %d attempts", m.TotalAttempts)
	}
}

func TestPermanentFailureReleasesTheHoldAndStops(t *testing.T) {
	r := newRig(t)
	first := r.provider("np1", np(ProviderConfig{Quality: 99}), map[string]any{"reject_prefixes": []any{"977984"}})
	second := r.provider("np2", np(ProviderConfig{Quality: 80}), nil)
	r.user("acme", 1.0)
	r.start()

	res, _ := r.send("acme", SendRequest{To: npNumber, Text: "hello"})
	m := r.waitState(res.Message.ID, StateFailed)
	if m.ErrorCode != "invalid_destination" {
		t.Fatalf("error code = %q", m.ErrorCode)
	}
	if len(second.Sent()) != 0 || len(first.Sent()) != 0 {
		t.Fatal("a permanently bad destination must not be tried elsewhere")
	}
	if b := r.balance("acme"); b.Balance != 1_000_000 || b.Held != 0 {
		t.Fatalf("a failed message must cost nothing: %+v", b)
	}
	if st, _ := r.hub.Store.HoldState(context.Background(), m.ID); st != "released" {
		t.Fatalf("hold = %q", st)
	}
}

func TestEveryProviderFailingFailsTheMessageAndReleasesFunds(t *testing.T) {
	r := newRig(t)
	a := r.provider("a", np(ProviderConfig{}), nil)
	b := r.provider("b", np(ProviderConfig{}), nil)
	a.SetDown(true)
	b.SetDown(true)
	r.user("acme", 1.0)
	r.start()

	res, _ := r.send("acme", SendRequest{To: npNumber, Text: "hello"})
	m := r.waitState(res.Message.ID, StateFailed)
	if m.ErrorCode != "all_providers_failed" {
		t.Fatalf("code = %q", m.ErrorCode)
	}
	if bal := r.balance("acme"); bal.Balance != 1_000_000 || bal.Held != 0 {
		t.Fatalf("balance = %+v", bal)
	}
}

func TestAtLeastOnceWhenTheResponseIsLost(t *testing.T) {
	r := newRig(t)
	gw := r.provider("np1", np(ProviderConfig{}), nil)
	r.user("acme", 1.0)
	gw.LoseResponseNext(1) // delivered upstream, but we saw a timeout
	r.start()

	res, _ := r.send("acme", SendRequest{To: npNumber, Text: "hello"})
	m := r.waitState(res.Message.ID, StateSubmitted, StateDelivered)
	if n := gw.Count(m.ID); n < 1 {
		t.Fatalf("delivered %d times; at least once is the guarantee", n)
	}
	if b := r.balance("acme"); b.Balance != 1_000_000-price || b.Held != 0 {
		t.Fatalf("a duplicate delivery must not be a duplicate charge: %+v", b)
	}
}

func TestDuplicateJobsSendOnce(t *testing.T) {
	r := newRig(t)
	gw := r.provider("np1", np(ProviderConfig{}), map[string]any{"latency": "50ms"})
	r.user("acme", 1.0)
	r.start()

	res, _ := r.send("acme", SendRequest{To: npNumber, Text: "hello"})
	// The queue redelivers the same job several times at once.
	job := DispatchJob{MessageID: res.Message.ID, Provider: "np1", Seq: res.Message.DispatchSeq}
	for range 5 {
		if _, err := r.q.Enqueue(JobDispatchPrefix+"np1", job); err != nil {
			t.Fatal(err)
		}
	}
	r.waitState(res.Message.ID, StateSubmitted, StateDelivered)
	time.Sleep(300 * time.Millisecond)
	if gw.Count(res.Message.ID) != 1 {
		t.Fatalf("redelivered jobs sent the message %d times", gw.Count(res.Message.ID))
	}
	if b := r.balance("acme"); b.Balance != 1_000_000-price {
		t.Fatalf("balance = %+v", b)
	}
}

func TestRecoveryRepublishesALostJob(t *testing.T) {
	r := newRig(t)
	gw := r.provider("np1", np(ProviderConfig{}), nil)
	r.user("acme", 1.0)
	r.start()

	// Accept without ever publishing: the process died between commit and
	// publish.
	ctx := context.Background()
	u, _ := r.hub.ValidateUser(ctx, "acme")
	d, _ := r.hub.CheckData(ctx, u, SendRequest{To: npNumber, Text: "hello"})
	ex, _ := r.hub.PlanRoute(ctx, u, d)
	res, err := r.hub.Accept(ctx, u, d, ex)
	if err != nil {
		t.Fatal(err)
	}
	m := r.waitState(res.Message.ID, StateSubmitted, StateDelivered)
	if gw.Count(m.ID) != 1 || r.hub.Counters.Recovered.Load() == 0 {
		t.Fatalf("deliveries=%d recovered=%d", gw.Count(m.ID), r.hub.Counters.Recovered.Load())
	}
}

func TestRecoveryReclaimsAWorkerThatDied(t *testing.T) {
	r := newRig(t, func(c *HubConfig) { c.Pipeline.ClaimLease = Duration(100 * time.Millisecond) })
	gw := r.provider("np1", np(ProviderConfig{}), nil)
	r.user("acme", 1.0)

	ctx := context.Background()
	u, _ := r.hub.ValidateUser(ctx, "acme")
	d, _ := r.hub.CheckData(ctx, u, SendRequest{To: npNumber, Text: "hello"})
	ex, _ := r.hub.PlanRoute(ctx, u, d)
	res, _ := r.hub.Accept(ctx, u, d, ex)
	// A worker claims the message and dies before sending.
	if _, err := r.hub.Store.Claim(ctx, res.Message.ID, 1, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	r.start()
	m := r.waitState(res.Message.ID, StateSubmitted, StateDelivered)
	if gw.Count(m.ID) != 1 {
		t.Fatalf("deliveries = %d", gw.Count(m.ID))
	}
}

func TestFailedReceiptRefundsACapturedCharge(t *testing.T) {
	r := newRig(t, func(c *HubConfig) { c.RefundOnDLRFailure = true })
	r.provider("np1", np(ProviderConfig{}), map[string]any{"dlr": "failed", "dlr_delay": "5ms"})
	r.user("acme", 1.0)
	r.start()

	res, _ := r.send("acme", SendRequest{To: npNumber, Text: "hello"})
	r.waitState(res.Message.ID, StateFailed)
	if b := r.balance("acme"); b.Balance != 1_000_000 || b.Held != 0 {
		t.Fatalf("balance after a failed receipt = %+v", b)
	}
	if st, _ := r.hub.Store.HoldState(context.Background(), res.Message.ID); st != "refunded" {
		t.Fatalf("hold = %q", st)
	}
}

func TestChargeOnDeliveryCapturesOnTheReceipt(t *testing.T) {
	r := newRig(t, func(c *HubConfig) { c.CaptureOn = "delivered" })
	r.provider("np1", np(ProviderConfig{}), map[string]any{"dlr_delay": "300ms"})
	r.user("acme", 1.0)
	r.start()

	res, _ := r.send("acme", SendRequest{To: npNumber, Text: "hello"})
	r.waitState(res.Message.ID, StateSubmitted)
	if b := r.balance("acme"); b.Balance != 1_000_000 || b.Held != price {
		t.Fatalf("before the receipt the funds are held, not spent: %+v", b)
	}
	r.waitState(res.Message.ID, StateDelivered)
	if b := r.balance("acme"); b.Balance != 1_000_000-price || b.Held != 0 {
		t.Fatalf("after the receipt: %+v", b)
	}
}

func TestDuplicateReceiptsChangeNothing(t *testing.T) {
	r := newRig(t)
	r.provider("np1", np(ProviderConfig{}), map[string]any{"dlr": "none"})
	r.user("acme", 1.0)
	r.start()

	res, _ := r.send("acme", SendRequest{To: npNumber, Text: "hello"})
	m := r.waitState(res.Message.ID, StateSubmitted)
	dlr := gateway.DLR{Provider: "np1", ProviderMessageID: m.ProviderMsgID, Status: gateway.StatusDelivered}
	for range 4 {
		if err := r.hub.EnqueueDLR(dlr); err != nil {
			t.Fatal(err)
		}
	}
	r.waitState(m.ID, StateDelivered)
	time.Sleep(150 * time.Millisecond)
	if b := r.balance("acme"); b.Balance != 1_000_000-price || b.Held != 0 {
		t.Fatalf("balance = %+v", b)
	}
	if r.hub.Counters.Delivered.Load() != 1 {
		t.Fatalf("delivered counted %d times", r.hub.Counters.Delivered.Load())
	}
}

func TestScheduledMessageWaitsForItsTime(t *testing.T) {
	r := newRig(t)
	gw := r.provider("np1", np(ProviderConfig{}), nil)
	r.user("acme", 1.0)
	r.start()

	at := time.Now().Add(700 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	res, err := r.send("acme", SendRequest{To: npNumber, Text: "later", ScheduleAt: at})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if gw.Count(res.Message.ID) != 0 {
		t.Fatal("a scheduled message was sent early")
	}
	if b := r.balance("acme"); b.Held != price {
		t.Fatalf("a scheduled message holds its price: %+v", b)
	}
	r.waitState(res.Message.ID, StateSubmitted, StateDelivered)
}

func TestExpiredMessageFailsAndReleases(t *testing.T) {
	r := newRig(t)
	gw := r.provider("np1", np(ProviderConfig{}), nil)
	r.user("acme", 1.0)
	// Do not start the consumers until the message has expired.
	ctx := context.Background()
	u, _ := r.hub.ValidateUser(ctx, "acme")
	d, _ := r.hub.CheckData(ctx, u, SendRequest{To: npNumber, Text: "hello", ExpiresIn: 1})
	d.ExpiresAt = time.Now().Add(-time.Second)
	ex, _ := r.hub.PlanRoute(ctx, u, d)
	res, err := r.hub.Accept(ctx, u, d, ex)
	if err != nil {
		t.Fatal(err)
	}
	r.start()
	m := r.waitState(res.Message.ID, StateFailed)
	if m.ErrorCode != "expired" || len(gw.Sent()) != 0 {
		t.Fatalf("code=%s sent=%d", m.ErrorCode, len(gw.Sent()))
	}
	if b := r.balance("acme"); b.Balance != 1_000_000 || b.Held != 0 {
		t.Fatalf("balance = %+v", b)
	}
}

func TestTopUpIsIdempotentByReference(t *testing.T) {
	r := newRig(t)
	r.user("acme", 0)
	ctx := context.Background()
	for range 3 {
		if _, _, err := r.hub.TopUp(ctx, "acme", 5, "pay-1001"); err != nil {
			t.Fatal(err)
		}
	}
	if b := r.balance("acme"); b.Balance != 5_000_000 {
		t.Fatalf("balance = %d, a replayed payment must credit once", b.Balance)
	}
}

func TestTextIsErasedOnceAProviderHasIt(t *testing.T) {
	r := newRig(t)
	r.provider("np1", np(ProviderConfig{}), map[string]any{"dlr": "none"})
	r.user("acme", 1.0)
	r.start()

	res, _ := r.send("acme", SendRequest{To: npNumber, Text: "Your one-time code is 904113", Type: "otp"})
	if res.Message.Text == "" {
		t.Fatal("the text must exist until a provider accepts the message")
	}
	m := r.waitState(res.Message.ID, StateSubmitted)
	if m.Text != "" {
		t.Fatalf("an OTP must not outlive its delivery, found %q", m.Text)
	}
	var body string
	if err := r.db.QueryRow(`SELECT body FROM sms_messages WHERE id = ?`, m.ID).Scan(&body); err != nil || strings.Contains(body, "904113") {
		t.Fatalf("the stored body still holds the code: %s (%v)", body, err)
	}
}

func TestKeepTextRetainsIt(t *testing.T) {
	r := newRig(t, func(c *HubConfig) { c.KeepText = true })
	r.provider("np1", np(ProviderConfig{}), map[string]any{"dlr": "none"})
	r.user("acme", 1.0)
	r.start()
	res, _ := r.send("acme", SendRequest{To: npNumber, Text: "keep me"})
	if m := r.waitState(res.Message.ID, StateSubmitted); m.Text != "keep me" {
		t.Fatalf("text = %q", m.Text)
	}
}

func TestRuntimeProviderIsBuiltBeforeItIsStoredAndNotRebuiltNeedlessly(t *testing.T) {
	r := newRig(t)
	r.user("acme", 1.0)
	r.start()
	ctx := context.Background()

	// A provider that cannot be built is refused and leaves no trace.
	bad := ProviderRecord{Name: "bad", Kind: "http", Enabled: true, Config: map[string]any{"plugin": map[string]any{"url": "ftp://nowhere"}}}
	if err := r.hub.PutProvider(ctx, bad); err == nil {
		t.Fatal("an unbuildable provider must be refused")
	}
	if recs, _ := r.hub.Store.ListProviders(ctx); len(recs) != 0 {
		t.Fatalf("a refused provider was stored: %+v", recs)
	}
	if _, ok := r.hub.provider("bad"); ok {
		t.Fatal("a refused provider is running")
	}

	good := ProviderRecord{Name: "dyn", Kind: "mock", Enabled: true, Config: map[string]any{"countries": []any{"NP"}}}
	if err := r.hub.PutProvider(ctx, good); err != nil {
		t.Fatal(err)
	}
	first, _ := r.hub.Gateway("dyn")

	// The change event this node just published must not rebuild the gateway.
	time.Sleep(200 * time.Millisecond)
	if again, _ := r.hub.Gateway("dyn"); again != first {
		t.Fatal("the node's own configuration event rebuilt an unchanged provider")
	}
	// A real change does.
	good.Config = map[string]any{"countries": []any{"NP"}, "cost_per_segment": 0.5}
	if err := r.hub.PutProvider(ctx, good); err != nil {
		t.Fatal(err)
	}
	if changed, _ := r.hub.Gateway("dyn"); changed == first {
		t.Fatal("a changed provider kept its old gateway")
	}

	// It survives a restart of the hub's in-memory state: it is reloaded from the store.
	recs, _ := r.hub.Store.ListProviders(ctx)
	if len(recs) != 1 || recs[0].Name != "dyn" {
		t.Fatalf("stored providers = %+v", recs)
	}
}

func TestConfigurationSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	r1 := newRigIn(t, dir)
	r1.provider("platform_np", np(ProviderConfig{Quality: 70}), nil)
	r1.user("acme", 2.0, func(u *User) { u.Tenant = "acme_org" })
	r1.start()
	ctx := context.Background()
	if err := r1.hub.PutProvider(ctx, ProviderRecord{Name: "acme_gw", Kind: "mock", Owner: "acme", Enabled: true,
		Config: map[string]any{"countries": []any{"NP"}, "cost_per_segment": 0.004}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r1.hub.PutRate(ctx, Rate{ID: "r1", UserID: "acme", SellPerSegmentMicros: 5_000}); err != nil {
		t.Fatal(err)
	}
	r1.close()

	// The platform provider is declared in BCL, so the new process registers it
	// again; everything created at runtime must come back from the store.
	r2 := newRigIn(t, dir)
	r2.provider("platform_np", np(ProviderConfig{Quality: 70}), nil)
	r2.start()
	if _, ok := r2.hub.Gateway("acme_gw"); !ok {
		t.Fatal("a runtime provider was not restored")
	}
	u, ok := r2.hub.Dir.User("acme")
	if !ok || u.Tenant != "acme_org" {
		t.Fatalf("user not restored: %+v", u)
	}
	if price, err := r2.hub.Dir.Price("acme", "NP", "transactional"); err != nil || price != 5_000 {
		t.Fatalf("rate not restored: %d %v", price, err)
	}
	res, err := r2.send("acme", SendRequest{To: npNumber, Text: "after restart"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Message.Plan[0].Provider != "acme_gw" || len(res.Message.Plan) != 1 {
		t.Fatalf("the user's own provider must route exclusively after a restart: %+v", res.Message.Plan)
	}
	r2.waitState(res.Message.ID, StateSubmitted, StateDelivered)
	if b := r2.balance("acme"); b.Balance != 2_000_000-5_000 {
		t.Fatalf("balance = %+v: the opening credit must not be repeated and the new rate must apply", b)
	}
}
