package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"
)

// slaEngine builds the grant pipeline with a custom screen-stage SLA and
// returns a case sitting on that stage.
func slaEngine(t *testing.T, sla *SLA, claimable bool, focus string) (*Engine, *clock, *Case) {
	t.Helper()
	def := grantDef()
	for i := range def.Stages {
		if def.Stages[i].Name == "screen" {
			def.Stages[i].SLA = sla
			if claimable {
				def.Stages[i].Routing = nil
				def.Stages[i].Claimable = true
			}
		}
	}
	comp, err := Compile(def)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	clk := &clock{}
	clk.set("2026-03-04T05:00:00Z")
	e := NewEngine(comp)
	e.Eval = bclEval{}
	e.Now = clk.now
	e.SigningKey = []byte("grant-signing-key-0123456789abcdef")
	e.SealKey = []byte("grant-seal-key-0123456789abcdef!")
	e.Workload = func(context.Context, string) (map[string]Load, error) { return loads{}, nil }
	return e, clk, submitGrant(t, e, focus)
}

func TestSLABreachRelease(t *testing.T) {
	e, clk, c := slaEngine(t, &SLA{Duration: "2h", OnBreach: "release"}, false, "education")
	ctx := context.Background()
	if c.Stages["screen"].Assignee == "" {
		t.Fatal("expected routing to assign the case")
	}
	clk.add(3 * time.Hour)
	c, res, err := e.Sweep(ctx, c)
	if err != nil || !res.Changed {
		t.Fatalf("sweep: %v %+v", err, res)
	}
	ss := c.Stages["screen"]
	if ss.SLA.Status != SLABreached || ss.Assignee != "" || !strings.Contains(eventNames(c), "released") {
		t.Fatalf("release on breach: %+v assignee=%q %s", ss.SLA, ss.Assignee, eventNames(c))
	}
}

func TestSLABreachAutomaticAction(t *testing.T) {
	e, clk, c := slaEngine(t, &SLA{Duration: "2h", OnBreach: "accept"}, false, "education")
	clk.add(3 * time.Hour)
	c, _, err := e.Sweep(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if c.Stage != "panel" || !strings.Contains(eventNames(c), "sla.breached") {
		t.Fatalf("auto-transition: stage=%s %s", c.Stage, eventNames(c))
	}
}

func TestSLAPickupTimeout(t *testing.T) {
	sla := &SLA{Duration: "8h", PickupTimeout: "1h", OnPickupTimeout: "reassign"}
	e, clk, c := slaEngine(t, sla, true, "education")
	ctx := context.Background()
	ss := c.Stages["screen"]
	if ss.Assignee != "" || ss.SLA.PickupDueAt == nil {
		t.Fatalf("expected an unclaimed stage with a pickup deadline: %+v", ss)
	}
	clk.add(30 * time.Minute)
	if _, res, _ := e.Sweep(ctx, c); res.Changed {
		t.Fatal("pickup breached early")
	}
	clk.add(40 * time.Minute)
	c, res, err := e.Sweep(ctx, c)
	if err != nil || !res.Changed {
		t.Fatalf("sweep: %v %+v", err, res)
	}
	ss = c.Stages["screen"]
	if !ss.SLA.PickupBreached || !strings.Contains(eventNames(c), "sla.pickup_breached") || ss.SLA.Status == SLABreached {
		t.Fatalf("pickup: %+v %s", ss.SLA, eventNames(c))
	}
	// Breaches once only.
	if _, res, _ := e.Sweep(ctx, c); res.Changed {
		t.Fatal("pickup breach repeated")
	}
}

func TestSLAPickupSkippedWhenClaimed(t *testing.T) {
	e, clk, c := slaEngine(t, &SLA{Duration: "8h", PickupTimeout: "1h"}, true, "education")
	ctx := context.Background()
	c, err := e.Claim(ctx, c, Actor{ID: "bo", Roles: []string{"officer"}}, "screen")
	if err != nil {
		t.Fatal(err)
	}
	clk.add(2 * time.Hour)
	c, _, _ = e.Sweep(ctx, c)
	if c.Stages["screen"].SLA.PickupBreached {
		t.Fatal("claimed work breached pickup")
	}
}
