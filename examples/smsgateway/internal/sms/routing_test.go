package sms

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/oarkflow/smppflow/pkg/routing"
)

func pv(name string, cfg ProviderConfig) ProviderView {
	if cfg.Currency == "" {
		cfg.Currency = "USD"
	}
	return ProviderView{Name: name, Config: cfg, Owner: cfg.Owner, Disabled: cfg.Disabled}
}

func snapshot(providers []ProviderView, assigns []Assignment, users ...User) Snapshot {
	s := Snapshot{Users: map[string]User{}, Providers: providers, Assignments: assigns}
	for _, u := range users {
		if u.Tenant == "" {
			u.Tenant = u.ID
		}
		s.Users[u.ID] = u
	}
	return s
}

func plan(t *testing.T, rt *Router, req Request) []string {
	t.Helper()
	ex, err := rt.Plan(context.Background(), req)
	if err != nil {
		t.Fatalf("plan for %+v: %v", req, err)
	}
	var out []string
	for _, p := range ex.Plan {
		out = append(out, p.Provider)
	}
	return out
}

func baseReq(user, tenant string) Request {
	return Request{UserID: user, Tenant: tenant, From: "ACME", To: "9779841234567", Country: "NP", Type: TypeTransactional, Segments: 1, WantDLR: true}
}

func mustRebuild(t *testing.T, rt *Router, s Snapshot) {
	t.Helper()
	if err := rt.Rebuild(s); err != nil {
		t.Fatal(err)
	}
}

func TestRouteTiers(t *testing.T) {
	alice := User{ID: "alice", Tenant: "acme"}
	bob := User{ID: "bob", Tenant: "acme"}
	carol := User{ID: "carol", Tenant: "other"}
	provs := []ProviderView{
		pv("platform_any", ProviderConfig{Quality: 80}),
		pv("np_country", ProviderConfig{Countries: []string{"NP"}, Quality: 80}),
		pv("alice_own", ProviderConfig{Countries: []string{"NP"}, Quality: 80, AssignedOnly: true}),
		pv("acme_shared", ProviderConfig{Countries: []string{"NP"}, Quality: 80, AssignedOnly: true}),
	}
	assigns := []Assignment{
		{ID: "a1", UserID: "alice", Provider: "alice_own"},
		{ID: "a2", Tenant: "acme", Provider: "acme_shared"},
	}
	rt := NewRouter(RoutingPolicy{})
	mustRebuild(t, rt, snapshot(provs, assigns, alice, bob, carol))

	// alice: her own provider, then her tenant's, then the country's, then the
	// catch-all.
	if got, want := plan(t, rt, baseReq("alice", "acme")), []string{"alice_own", "acme_shared", "np_country", "platform_any"}; !slices.Equal(got, want) {
		t.Fatalf("alice's chain = %v, want %v", got, want)
	}
	// bob shares the tenant but not alice's provider.
	if got, want := plan(t, rt, baseReq("bob", "acme")), []string{"acme_shared", "np_country", "platform_any"}; !slices.Equal(got, want) {
		t.Fatalf("bob's chain = %v, want %v", got, want)
	}
	// carol is in another tenant: platform providers only.
	if got, want := plan(t, rt, baseReq("carol", "other")), []string{"np_country", "platform_any"}; !slices.Equal(got, want) {
		t.Fatalf("carol's chain = %v, want %v", got, want)
	}
	// A destination no country provider serves falls to the catch-all.
	in := baseReq("carol", "other")
	in.To, in.Country = "919812345678", "IN"
	if got, want := plan(t, rt, in), []string{"platform_any"}; !slices.Equal(got, want) {
		t.Fatalf("an unserved country must use the catch-all, got %v", got)
	}
}

func TestAssignmentOnlyAppliesToItsCountries(t *testing.T) {
	alice := User{ID: "alice", Tenant: "alice"}
	provs := []ProviderView{
		pv("global_a", ProviderConfig{Countries: []string{"NP", "IN"}, Quality: 80}),
		pv("global_b", ProviderConfig{Countries: []string{"NP", "IN"}, Quality: 80, Weight: 1, AssignedOnly: true}),
	}
	assigns := []Assignment{{ID: "a1", UserID: "alice", Provider: "global_b", Countries: []string{"IN"}}}
	rt := NewRouter(RoutingPolicy{})
	mustRebuild(t, rt, snapshot(provs, assigns, alice))

	np := baseReq("alice", "alice")
	if got := plan(t, rt, np); got[0] != "global_a" {
		t.Fatalf("for Nepal alice has no assignment, chain = %v", got)
	}
	in := baseReq("alice", "alice")
	in.To, in.Country = "919812345678", "IN"
	if got := plan(t, rt, in); got[0] != "global_b" {
		t.Fatalf("for India alice's assigned provider goes first, chain = %v", got)
	}
}

func TestExclusiveAssignmentEndsTheChain(t *testing.T) {
	alice := User{ID: "alice", Tenant: "alice"}
	provs := []ProviderView{
		pv("platform", ProviderConfig{Countries: []string{"NP"}, Quality: 99}),
		pv("hers", ProviderConfig{Countries: []string{"NP"}, Quality: 50, AssignedOnly: true}),
	}
	rt := NewRouter(RoutingPolicy{})
	mustRebuild(t, rt, snapshot(provs, []Assignment{{ID: "a", UserID: "alice", Provider: "hers", Exclusive: true}}, alice))
	if got := plan(t, rt, baseReq("alice", "alice")); !slices.Equal(got, []string{"hers"}) {
		t.Fatalf("an exclusive assignment must not fall back to the platform, got %v", got)
	}
}

func TestUserOwnedProviderIsPrivateAndExclusive(t *testing.T) {
	alice := User{ID: "alice", Tenant: "alice"}
	bob := User{ID: "bob", Tenant: "bob"}
	provs := []ProviderView{
		pv("platform", ProviderConfig{Countries: []string{"NP"}}),
		pv("alice_twilio", ProviderConfig{Countries: []string{"NP"}, Owner: "alice"}),
	}
	rt := NewRouter(RoutingPolicy{})
	mustRebuild(t, rt, snapshot(provs, nil, alice, bob))
	if got := plan(t, rt, baseReq("alice", "alice")); !slices.Equal(got, []string{"alice_twilio"}) {
		t.Fatalf("alice = %v", got)
	}
	if got := plan(t, rt, baseReq("bob", "bob")); !slices.Equal(got, []string{"platform"}) {
		t.Fatalf("bob must never reach alice's provider, got %v", got)
	}
}

func TestObjectivesOrderByCostOrDelivery(t *testing.T) {
	provs := []ProviderView{
		pv("cheap", ProviderConfig{Countries: []string{"NP"}, CostPerSegment: 0.004, Quality: 70, DeliveryRate: 0.90}),
		pv("premium", ProviderConfig{Countries: []string{"NP"}, CostPerSegment: 0.012, Quality: 97, DeliveryRate: 0.995}),
	}
	rt := NewRouter(RoutingPolicy{Objectives: map[string]string{TypeOTP: ObjectiveDelivery, TypePromotional: ObjectiveCost}})
	mustRebuild(t, rt, snapshot(provs, nil, User{ID: "u"}))

	otp := baseReq("u", "u")
	otp.Type = TypeOTP
	if got := plan(t, rt, otp); got[0] != "premium" {
		t.Fatalf("an OTP wants delivery, chain = %v", got)
	}
	promo := baseReq("u", "u")
	promo.Type = TypePromotional
	if got := plan(t, rt, promo); got[0] != "cheap" {
		t.Fatalf("a promotion wants the lowest cost, chain = %v", got)
	}
	// A user's own preference overrides the message type's.
	promo.Objective = ObjectiveDelivery
	if got := plan(t, rt, promo); got[0] != "premium" {
		t.Fatalf("the user's objective must win, chain = %v", got)
	}
}

func TestPerCountryCostDrivesTheCostObjective(t *testing.T) {
	provs := []ProviderView{
		pv("a", ProviderConfig{CostPerSegment: 0.01, Cost: map[string]float64{"NP": 0.02}, Quality: 80}),
		pv("b", ProviderConfig{CostPerSegment: 0.015, Quality: 80}),
	}
	rt := NewRouter(RoutingPolicy{Default: ObjectiveCost})
	mustRebuild(t, rt, snapshot(provs, nil, User{ID: "u"}))
	if got := plan(t, rt, baseReq("u", "u")); got[0] != "b" {
		t.Fatalf("in Nepal b is cheaper (0.015 against 0.02), chain = %v", got)
	}
	in := baseReq("u", "u")
	in.To, in.Country = "919812345678", "IN"
	if got := plan(t, rt, in); got[0] != "a" {
		t.Fatalf("in India a is cheaper (0.01 against 0.015), chain = %v", got)
	}
}

func TestQualityFloorKeepsOTPOffWeakProviders(t *testing.T) {
	provs := []ProviderView{
		pv("weak", ProviderConfig{Countries: []string{"NP"}, Quality: 60}),
		pv("strong", ProviderConfig{Countries: []string{"NP"}, Quality: 95}),
	}
	rt := NewRouter(RoutingPolicy{QualityFloor: map[string]float64{TypeOTP: 90}})
	mustRebuild(t, rt, snapshot(provs, nil, User{ID: "u"}))
	otp := baseReq("u", "u")
	otp.Type = TypeOTP
	if got := plan(t, rt, otp); !slices.Equal(got, []string{"strong"}) {
		t.Fatalf("an OTP must avoid the weak provider, got %v", got)
	}
	if got := plan(t, rt, baseReq("u", "u")); len(got) != 2 {
		t.Fatalf("a transactional message may use both, got %v", got)
	}
}

func TestProviderCapabilitiesAreRespected(t *testing.T) {
	no := false
	provs := []ProviderView{
		pv("ascii_only", ProviderConfig{Countries: []string{"NP"}, Quality: 99, Capabilities: Capabilities{Unicode: &no}}),
		pv("no_dlr", ProviderConfig{Countries: []string{"NP"}, Quality: 98, Capabilities: Capabilities{DLR: &no}}),
		pv("numeric_only", ProviderConfig{Countries: []string{"NP"}, Quality: 97, Capabilities: Capabilities{AlphaSender: &no}}),
		pv("short_only", ProviderConfig{Countries: []string{"NP"}, Quality: 96, Capabilities: Capabilities{LongSMS: &no}}),
		pv("full", ProviderConfig{Countries: []string{"NP"}, Quality: 50}),
	}
	rt := NewRouter(RoutingPolicy{})
	mustRebuild(t, rt, snapshot(provs, nil, User{ID: "u"}))

	cases := []struct {
		name string
		mod  func(*Request)
		not  string
	}{
		{"unicode", func(r *Request) { r.Unicode = true }, "ascii_only"},
		{"dlr", func(r *Request) { r.WantDLR = true }, "no_dlr"},
		{"alpha sender", func(r *Request) { r.From = "ACME" }, "numeric_only"},
		{"long message", func(r *Request) { r.Segments = 3 }, "short_only"},
	}
	for _, c := range cases {
		req := baseReq("u", "u")
		c.mod(&req)
		got := plan(t, rt, req)
		if slices.Contains(got, c.not) || !slices.Contains(got, "full") {
			t.Errorf("%s: chain %v must not contain %s", c.name, got, c.not)
		}
	}
	plain := baseReq("u", "u")
	plain.From, plain.WantDLR = "9779800000000", false
	if got := plan(t, rt, plain); !slices.Contains(got, "ascii_only") || !slices.Contains(got, "numeric_only") {
		t.Errorf("a plain numeric-sender message can use every provider, got %v", got)
	}
}

func TestMessageTypeRestrictedProvider(t *testing.T) {
	provs := []ProviderView{
		pv("otp_route", ProviderConfig{Countries: []string{"NP"}, MessageTypes: []string{TypeOTP}, Quality: 99}),
		pv("bulk", ProviderConfig{Countries: []string{"NP"}, Quality: 50}),
	}
	rt := NewRouter(RoutingPolicy{})
	mustRebuild(t, rt, snapshot(provs, nil, User{ID: "u"}))
	promo := baseReq("u", "u")
	promo.Type = TypePromotional
	if got := plan(t, rt, promo); !slices.Equal(got, []string{"bulk"}) {
		t.Fatalf("the OTP route must not carry promotions, got %v", got)
	}
	otp := baseReq("u", "u")
	otp.Type = TypeOTP
	if got := plan(t, rt, otp); got[0] != "otp_route" {
		t.Fatalf("got %v", got)
	}
}

func TestNoRouteIsInvalidWhenNothingServesAndTransientWhenDown(t *testing.T) {
	provs := []ProviderView{pv("np", ProviderConfig{Countries: []string{"NP"}})}
	rt := NewRouter(RoutingPolicy{})
	mustRebuild(t, rt, snapshot(provs, nil, User{ID: "u"}))

	in := baseReq("u", "u")
	in.To, in.Country = "919812345678", "IN"
	_, err := rt.Plan(context.Background(), in)
	var nr *ErrNoRoute
	if !errors.As(err, &nr) || nr.Transient {
		t.Fatalf("an unserved country is a permanent no-route, got %v", err)
	}

	rt.SetState("np", routing.ProviderDown)
	_, err = rt.Plan(context.Background(), baseReq("u", "u"))
	if !errors.As(err, &nr) || !nr.Transient {
		t.Fatalf("a provider that is down is a transient no-route, got %v", err)
	}
	rt.SetState("np", routing.ProviderActive)
	if got := plan(t, rt, baseReq("u", "u")); got[0] != "np" {
		t.Fatalf("got %v", got)
	}
}

func TestCircuitBreakerOpensAndRoutesAround(t *testing.T) {
	provs := []ProviderView{
		pv("flaky", ProviderConfig{Countries: []string{"NP"}, Quality: 99}),
		pv("steady", ProviderConfig{Countries: []string{"NP"}, Quality: 80}),
	}
	rt := NewRouter(RoutingPolicy{BreakerFailures: 3, BreakerOpen: Duration(200 * time.Millisecond)})
	mustRebuild(t, rt, snapshot(provs, nil, User{ID: "u"}))
	req := baseReq("u", "u")
	if got := plan(t, rt, req); got[0] != "flaky" {
		t.Fatalf("got %v", got)
	}
	for range 3 {
		rt.Feedback("flaky", req, false, time.Millisecond)
	}
	if rt.Allow("flaky", "NP", TypeTransactional) {
		t.Fatal("the circuit must be open after three failures")
	}
	if got := plan(t, rt, req); !slices.Equal(got, []string{"steady"}) {
		t.Fatalf("an open circuit must be routed around, got %v", got)
	}
	// The breaker is per route: Indian traffic is unaffected.
	if !rt.Allow("flaky", "IN", TypeTransactional) {
		t.Fatal("the circuit for another country must stay closed")
	}
	time.Sleep(250 * time.Millisecond)
	if !rt.Allow("flaky", "NP", TypeTransactional) {
		t.Fatal("the circuit must half-open after its timeout")
	}
}

func TestRebuildKeepsHealthState(t *testing.T) {
	provs := []ProviderView{pv("a", ProviderConfig{Countries: []string{"NP"}, Quality: 99}), pv("b", ProviderConfig{Countries: []string{"NP"}, Quality: 80})}
	rt := NewRouter(RoutingPolicy{BreakerFailures: 2})
	snap := snapshot(provs, nil, User{ID: "u"})
	mustRebuild(t, rt, snap)
	req := baseReq("u", "u")
	rt.Feedback("a", req, false, 0)
	rt.Feedback("a", req, false, 0)
	mustRebuild(t, rt, snap) // a config change elsewhere
	if rt.Allow("a", "NP", TypeTransactional) {
		t.Fatal("a configuration change must not close an open circuit")
	}
	if st, _ := rt.Stats("a"); st.Failures != 2 {
		t.Fatalf("failure count lost across a rebuild: %+v", st)
	}
}

func TestDisabledProviderIsNotRouted(t *testing.T) {
	provs := []ProviderView{
		pv("off", ProviderConfig{Countries: []string{"NP"}, Disabled: true}),
		pv("on", ProviderConfig{Countries: []string{"NP"}}),
	}
	rt := NewRouter(RoutingPolicy{})
	mustRebuild(t, rt, snapshot(provs, nil, User{ID: "u"}))
	if got := plan(t, rt, baseReq("u", "u")); !slices.Equal(got, []string{"on"}) {
		t.Fatalf("got %v", got)
	}
}

func TestExcludeRemovesTriedProviders(t *testing.T) {
	provs := []ProviderView{pv("a", ProviderConfig{Countries: []string{"NP"}, Quality: 99}), pv("b", ProviderConfig{Countries: []string{"NP"}, Quality: 80})}
	rt := NewRouter(RoutingPolicy{})
	mustRebuild(t, rt, snapshot(provs, nil, User{ID: "u"}))
	req := baseReq("u", "u")
	req.Exclude = []string{"a"}
	if got := plan(t, rt, req); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("got %v", got)
	}
}

func TestPriceResolutionPrefersTheMostSpecificRate(t *testing.T) {
	d := NewDirectory()
	d.SetBaseRates([]Rate{
		{ID: "base:*", SellPerSegmentMicros: 20_000},
		{ID: "base:NP", Country: "NP", SellPerSegmentMicros: 15_000},
		{ID: "base:type:otp", MessageType: "otp", SellPerSegmentMicros: 30_000},
		{ID: "base:np-otp", Country: "NP", MessageType: "otp", SellPerSegmentMicros: 35_000},
	})
	d.PutRate(Rate{ID: "vip", UserID: "vip", SellPerSegmentMicros: 10_000})
	d.PutRate(Rate{ID: "vip-np", UserID: "vip", Country: "NP", SellPerSegmentMicros: 8_000})

	cases := []struct {
		user, country, typ string
		want               int64
	}{
		{"u", "IN", "transactional", 20_000},
		{"u", "NP", "transactional", 15_000},
		{"u", "IN", "otp", 30_000},
		{"u", "NP", "otp", 35_000},
		{"vip", "IN", "transactional", 10_000},
		{"vip", "NP", "transactional", 8_000},
		{"vip", "NP", "otp", 8_000},
	}
	for _, c := range cases {
		got, err := d.Price(c.user, c.country, c.typ)
		if err != nil || got != c.want {
			t.Errorf("price(%s,%s,%s) = %d, %v; want %d", c.user, c.country, c.typ, got, err, c.want)
		}
	}
	empty := NewDirectory()
	if _, err := empty.Price("u", "NP", "otp"); err == nil {
		t.Fatal("a destination with no price must be refused, not given away")
	}
}

func TestNormalizeNumber(t *testing.T) {
	good := []struct{ in, def, digits, iso string }{
		{"+977 984-123-4567", "", "9779841234567", "NP"},
		{"009779841234567", "", "9779841234567", "NP"},
		{"9841234567", "NP", "9779841234567", "NP"},
		{"09841234567", "NP", "9779841234567", "NP"},
		{"9779841234567", "NP", "9779841234567", "NP"},
		{"+919876543210", "", "919876543210", "IN"},
		{"+44 7911 123456", "", "447911123456", "GB"},
		{"(415) 555-2671", "US", "14155552671", "US"},
	}
	for _, c := range good {
		d, iso, err := NormalizeNumber(c.in, c.def)
		if err != nil || d != c.digits || iso != c.iso {
			t.Errorf("%q = %q %q %v; want %q %q", c.in, d, iso, err, c.digits, c.iso)
		}
	}
	for _, in := range []string{"", "abc", "+97798412", "9841234567", "+0000000000", "+9779841234567890", "12-34"} {
		if d, _, err := NormalizeNumber(in, ""); err == nil {
			t.Errorf("%q must be rejected, got %q", in, d)
		}
	}
}

func TestOperatorPrefixRoutes(t *testing.T) {
	// Two operators' blocks in Nepal bought from different providers.
	provs := []ProviderView{
		pv("ntc_direct", ProviderConfig{Countries: []string{"NP"}, Prefixes: []string{"977984", "977985", "977986"}, Quality: 90}),
		pv("ncell_direct", ProviderConfig{Countries: []string{"NP"}, Prefixes: []string{"977980", "977981", "977982"}, Quality: 90}),
		pv("np_any", ProviderConfig{Countries: []string{"NP"}, Quality: 60}),
	}
	rt := NewRouter(RoutingPolicy{})
	mustRebuild(t, rt, snapshot(provs, nil, User{ID: "u"}))

	ntc := baseReq("u", "u") // 9779841234567
	if got := plan(t, rt, ntc); !slices.Equal(got, []string{"ntc_direct", "np_any"}) {
		t.Fatalf("an NTC number: %v", got)
	}
	ncell := baseReq("u", "u")
	ncell.To = "9779812345678"
	if got := plan(t, rt, ncell); !slices.Equal(got, []string{"ncell_direct", "np_any"}) {
		t.Fatalf("an Ncell number: %v", got)
	}
	other := baseReq("u", "u")
	other.To = "9779761234567"
	if got := plan(t, rt, other); !slices.Equal(got, []string{"np_any"}) {
		t.Fatalf("a number in neither block: %v", got)
	}
}

func TestAssignmentLimitedToASender(t *testing.T) {
	alice := User{ID: "alice", Tenant: "alice"}
	provs := []ProviderView{
		pv("brand_route", ProviderConfig{Countries: []string{"NP"}, Quality: 99, AssignedOnly: true}),
		pv("general", ProviderConfig{Countries: []string{"NP"}, Quality: 80}),
	}
	assigns := []Assignment{{ID: "a", UserID: "alice", Provider: "brand_route", Senders: []string{"ACMEBANK"}}}
	rt := NewRouter(RoutingPolicy{})
	mustRebuild(t, rt, snapshot(provs, assigns, alice))

	bank := baseReq("alice", "alice")
	bank.From = "ACMEBANK"
	if got := plan(t, rt, bank); got[0] != "brand_route" {
		t.Fatalf("the registered sender uses its route: %v", got)
	}
	other := baseReq("alice", "alice")
	other.From = "OTHER"
	if got := plan(t, rt, other); !slices.Equal(got, []string{"general"}) {
		t.Fatalf("another sender must not: %v", got)
	}
}

func TestNumericSenderCountryDecidesTheProvider(t *testing.T) {
	provs := []ProviderView{
		pv("us_longcode", ProviderConfig{Countries: []string{"NP"}, Quality: 99, SenderCountries: []string{"US"}}),
		pv("np_shortcode", ProviderConfig{Countries: []string{"NP"}, Quality: 80, SenderCountries: []string{"NP"}}),
		pv("any_sender", ProviderConfig{Countries: []string{"NP"}, Quality: 50}),
	}
	rt := NewRouter(RoutingPolicy{})
	mustRebuild(t, rt, snapshot(provs, nil, User{ID: "u"}))

	us := baseReq("u", "u")
	us.From, us.SenderCountry = "14155552671", "US"
	if got := plan(t, rt, us); !slices.Equal(got, []string{"us_longcode", "any_sender"}) {
		t.Fatalf("a US number: %v", got)
	}
	np := baseReq("u", "u")
	np.From, np.SenderCountry = "9779800000000", "NP"
	if got := plan(t, rt, np); !slices.Equal(got, []string{"np_shortcode", "any_sender"}) {
		t.Fatalf("a Nepali number: %v", got)
	}
	alpha := baseReq("u", "u") // an alphanumeric id has no country
	if got := plan(t, rt, alpha); len(got) != 3 {
		t.Fatalf("an alphanumeric sender may use every provider: %v", got)
	}
}
