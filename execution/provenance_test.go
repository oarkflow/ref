package execution_test

import (
	"context"
	"testing"
	"time"

	"github.com/oarkflow/ref/execution"
	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/invocation"
)

func TestTracedExecutionCapturesProvenanceAndDecisions(t *testing.T) {
	nodes := []*graph.Node{
		{ID: 0, Name: "load", Kind: graph.PureNode, Speculation: graph.PreAuthSafe, Provides: []fact.PlanSlot{0}},
		{ID: 1, Name: "policy", Kind: graph.DecisionNode, Requires: []fact.PlanSlot{0}},
	}
	g, err := graph.Build(nodes, 1)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := graph.Compile(g, "traced", 1)
	if err != nil {
		t.Fatal(err)
	}
	trace := execution.NewTrace("inv-trace", "traced", 1)
	outcome, err := execution.NewScheduler().ExecuteTraced(context.Background(), &invocation.Invocation{ID: "inv-trace", Intent: "traced"}, plan, []execution.NodeExecutor{
		func(nc *execution.NodeContext) error {
			execution.PublishFact(nc, 0, "value")
			return nil
		},
		func(nc *execution.NodeContext) error {
			nc.Decisions().RecordAllow("test", nil)
			return nil
		},
	}, execution.NewBudget(time.Second, 0, 0, 0, 0), trace)
	if err != nil {
		t.Fatal(err)
	}
	execution.ReleaseOutcome(outcome)
	snapshot := trace.Snapshot()
	if len(snapshot.Nodes) != 2 || len(snapshot.Facts) != 1 {
		t.Fatalf("unexpected trace shape: nodes=%d facts=%d", len(snapshot.Nodes), len(snapshot.Facts))
	}
	if snapshot.Facts[0].Producer != 0 || snapshot.Facts[0].Value != "value" {
		t.Fatalf("unexpected fact provenance: %+v", snapshot.Facts[0])
	}
	if len(snapshot.Decisions) != 1 || snapshot.Decisions[0].Verdict != execution.VerdictAllow {
		t.Fatalf("unexpected decision trace: %+v", snapshot.Decisions)
	}
	if _, err := trace.Digest(); err != nil {
		t.Fatal(err)
	}

	program := &execution.Program{Plan: plan, Runners: []execution.NodeExecutor{
		func(nc *execution.NodeContext) error {
			execution.PublishFact(nc, 0, "value")
			return nil
		},
		func(nc *execution.NodeContext) error {
			nc.Decisions().RecordAllow("test", nil)
			return nil
		},
	}}
	replayed, replayTrace, err := execution.NewScheduler().ExecuteProgramReplay(context.Background(), &invocation.Invocation{ID: "inv-trace", Intent: "traced"}, program, execution.NewBudget(time.Second, 0, 0, 0, 0), trace)
	if err != nil {
		t.Fatal(err)
	}
	execution.ReleaseOutcome(replayed)
	if replayTrace == nil {
		t.Fatal("expected replay trace")
	}
}

func TestPlanSimulation(t *testing.T) {
	nodes := []*graph.Node{
		{ID: 0, Name: "root", Provides: []fact.PlanSlot{0}},
		{ID: 1, Name: "left", Requires: []fact.PlanSlot{0}, Provides: []fact.PlanSlot{1}},
		{ID: 2, Name: "right", Requires: []fact.PlanSlot{0}, Provides: []fact.PlanSlot{2}},
		{ID: 3, Name: "join", Requires: []fact.PlanSlot{1, 2}},
	}
	g, err := graph.Build(nodes, 3)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := graph.Compile(g, "simulation", 1)
	if err != nil {
		t.Fatal(err)
	}
	simulation := plan.Simulation()
	if simulation.Nodes != 4 || simulation.Stages != 3 || simulation.MaxParallelism != 2 || simulation.CriticalPath != 3 {
		t.Fatalf("unexpected simulation: %+v", simulation)
	}
}
