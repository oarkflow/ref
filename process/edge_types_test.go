package process

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Behavioural tests for every edge type.
//
// Each test drives a real run through the engine and the memory store and
// asserts the documented semantics end to end: which steps ran, how often, with
// what input, and what state the run ended in. Every engine call is bounded (see
// edge_harness_test.go), so a deadlock fails the test instead of hanging it, and
// time is a fake clock so deadlines and backoff are asserted at their exact
// boundaries.

// def is a small builder so each test reads as its graph.
func def(name, start string, steps []*Step, edges ...*Edge) *Definition {
	d := &Definition{Name: name, Start: start, Steps: map[string]*Step{}, Edges: edges}
	for _, s := range steps {
		d.Steps[s.Name] = s
	}
	return d
}

func taskStep(name, role string) *Step {
	return &Step{Name: name, Task: &TaskDefinition{Role: role}}
}

// ---------------------------------------------------------------------------
// Sequencing: simple, branch, switch, conditional_fork, threshold, weighted,
// priority
// ---------------------------------------------------------------------------

func TestEdgeSimplePassesTheResultAsTheNextInput(t *testing.T) {
	h := newEdgeHarnessFor(t, def("e-simple", "a",
		[]*Step{step("a", "do.a"), step("b", "do.b"), terminal("c", "do.c")},
		edge("ab", EdgeSimple, "a", "b"), edge("bc", EdgeSimple, "b", "c")))
	h.runner.returns("do.a", map[string]any{"n": 1.0})
	h.runner.on("do.b", func(call StepCall, _ int) (any, error) {
		return map[string]any{"n": field(call.Input, "n").(float64) + 1}, nil
	})

	run := h.start("e-simple", map[string]any{"seed": "x"})
	expectStatus(t, run, StatusCompleted)
	if got := h.runner.intents(); !slices.Equal(got, []string{"do.a", "do.b", "do.c"}) {
		t.Fatalf("order = %v", got)
	}
	if got := field(h.runner.callsTo("do.a")[0].Input, "seed"); got != "x" {
		t.Fatalf("the start step did not receive the run input: %v", got)
	}
	if got := field(h.runner.callsTo("do.c")[0].Input, "n"); got != 2.0 {
		t.Fatalf("c received %v, want the result of b", got)
	}
}

func TestEdgeBranchTakesOnlyTheBranchWhoseConditionHolds(t *testing.T) {
	definition := def("e-branch", "decide",
		[]*Step{step("decide", "do.decide"), terminal("big", "do.big"), terminal("small", "do.small")},
		&Edge{Name: "big", Type: EdgeBranch, From: "decide", To: "big", Guard: exprGuard{path: "result.size", equal: "large"}},
		&Edge{Name: "small", Type: EdgeBranch, From: "decide", To: "small", Guard: exprGuard{path: "result.size", equal: "small"}})
	h := newEdgeHarnessFor(t, definition)
	h.runner.on("do.decide", echo)

	expectStatus(t, h.start("e-branch", map[string]any{"size": "small"}), StatusCompleted)
	expectStatus(t, h.start("e-branch", map[string]any{"size": "large"}), StatusCompleted)
	expectCount(t, h, "do.big", 1)
	expectCount(t, h, "do.small", 1)

	// Neither condition holds: a dead end is reported, not silently completed.
	run := h.start("e-branch", map[string]any{"size": "medium"})
	expectStatus(t, run, StatusFailed)
	if !strings.Contains(run.Error, "none of its") {
		t.Fatalf("dead end not explained: %q", run.Error)
	}
}

func TestEdgeSwitchTakesTheFirstMatchingCaseOrTheDefault(t *testing.T) {
	definition := def("e-switch", "pick",
		[]*Step{step("pick", "do.pick"), terminal("a", "do.a"), terminal("b", "do.b"), terminal("d", "do.default")},
		&Edge{Name: "case-a", Type: EdgeSwitch, From: "pick", To: "a", Guard: exprGuard{path: "result.kind", equal: "a"}},
		&Edge{Name: "case-b", Type: EdgeSwitch, From: "pick", To: "b", Guard: exprGuard{path: "result.flag", equal: true}},
		// A switch case without a condition is the default: taken only when no
		// other case matched.
		&Edge{Name: "default", Type: EdgeSwitch, From: "pick", To: "d"})
	h := newEdgeHarnessFor(t, definition)
	h.runner.on("do.pick", echo)

	// Both case-a and case-b hold: the first declared case wins, alone.
	expectStatus(t, h.start("e-switch", map[string]any{"kind": "a", "flag": true}), StatusCompleted)
	if got := h.runner.intents(); !slices.Equal(got, []string{"do.pick", "do.a"}) {
		t.Fatalf("a switch fired more than one case: %v", got)
	}
	expectStatus(t, h.start("e-switch", map[string]any{"kind": "z", "flag": true}), StatusCompleted)
	expectCount(t, h, "do.b", 1)
	expectCount(t, h, "do.default", 0)
	expectStatus(t, h.start("e-switch", map[string]any{"kind": "z", "flag": false}), StatusCompleted)
	expectCount(t, h, "do.default", 1)
	expectCount(t, h, "do.a", 1)
	expectCount(t, h, "do.b", 1)
}

func TestEdgeConditionalForkFiresEveryTargetOfEveryMatchingFork(t *testing.T) {
	definition := def("e-cfork", "src",
		[]*Step{step("src", "do.src"), terminal("x", "do.x"), terminal("y", "do.y"), terminal("z", "do.z"), terminal("w", "do.w")},
		&Edge{Name: "yes", Type: EdgeConditionalFork, From: "src", Targets: []string{"x", "y"}, Guard: exprGuard{path: "result.go", equal: "yes"}},
		&Edge{Name: "no", Type: EdgeConditionalFork, From: "src", Targets: []string{"z", "w"}, Guard: exprGuard{path: "result.go", equal: "no"}})
	h := newEdgeHarnessFor(t, definition)
	h.runner.on("do.src", echo)

	expectStatus(t, h.start("e-cfork", map[string]any{"go": "yes"}), StatusCompleted)
	for intent, want := range map[string]int{"do.x": 1, "do.y": 1, "do.z": 0, "do.w": 0} {
		expectCount(t, h, intent, want)
	}
}

func TestEdgeThresholdRoutesByBandAndCarriesTheBand(t *testing.T) {
	low, mid, high := 0.0, 100.0, 1000.0
	definition := def("e-threshold", "score",
		[]*Step{step("score", "do.score"), terminal("auto", "do.auto"), terminal("review", "do.review")},
		&Edge{Name: "band", Type: EdgeThreshold, From: "score", Targets: []string{"auto", "review"},
			Thresholds: []Threshold{
				{Name: "small", Min: &low, Max: &mid, Value: pathValuer{path: "result.amount"}, Target: "auto"},
				{Name: "large", Min: &mid, Max: &high, Value: pathValuer{path: "result.amount"}, Target: "review",
					Reason: "needs a human", Data: map[string]any{"queue": "risk"}},
			}})
	h := newEdgeHarnessFor(t, definition)
	h.runner.on("do.score", echo)

	// Min is inclusive, so exactly 100 is the large band.
	expectStatus(t, h.start("e-threshold", map[string]any{"amount": 100}), StatusCompleted)
	calls := h.runner.callsTo("do.review")
	if len(calls) != 1 {
		t.Fatalf("review ran %d times", len(calls))
	}
	input := calls[0].Input
	if field(input, "threshold") != "large" || field(input, "threshold_reason") != "needs a human" || field(input, "queue") != "risk" {
		t.Fatalf("the matched band is not carried forward: %v", input)
	}
	expectStatus(t, h.start("e-threshold", map[string]any{"amount": 5}), StatusCompleted)
	expectCount(t, h, "do.auto", 1)

	// A value outside every band is a configuration gap: the run fails loudly.
	expectStatus(t, h.start("e-threshold", map[string]any{"amount": 5000}), StatusFailed)
}

func TestEdgeWeightedChoosesExactlyOneEligibleSiblingByWeight(t *testing.T) {
	definition := def("e-weighted", "pick",
		[]*Step{step("pick", "do.pick"), terminal("light", "do.light"), terminal("heavy", "do.heavy"), terminal("blocked", "do.blocked")},
		&Edge{Name: "light", Type: EdgeWeighted, From: "pick", To: "light", Weight: 1},
		&Edge{Name: "heavy", Type: EdgeWeighted, From: "pick", To: "heavy", Weight: 99},
		// The heaviest edge is ineligible, so it must never be drawn.
		&Edge{Name: "blocked", Type: EdgeWeighted, From: "pick", To: "blocked", Weight: 100000, Guard: never})
	h := newEdgeHarnessFor(t, definition)

	const runs = 100
	for range runs {
		expectStatus(t, h.start("e-weighted", nil), StatusCompleted)
	}
	light, heavy := h.runner.count("do.light"), h.runner.count("do.heavy")
	if light+heavy != runs {
		t.Fatalf("each resolution must fire exactly one weighted edge: light=%d heavy=%d over %d runs", light, heavy, runs)
	}
	expectCount(t, h, "do.blocked", 0)
	if heavy < runs/2 {
		t.Fatalf("weights were not honoured: heavy=%d light=%d", heavy, light)
	}
}

func TestEdgePriorityTakesTheLowestEligiblePriority(t *testing.T) {
	definition := def("e-priority", "pick",
		[]*Step{step("pick", "do.pick"), terminal("p5", "do.p5"), terminal("p1", "do.p1"), terminal("p2a", "do.p2a"), terminal("p2b", "do.p2b")},
		&Edge{Name: "p5", Type: EdgePriority, From: "pick", To: "p5", Priority: 5},
		&Edge{Name: "p1", Type: EdgePriority, From: "pick", To: "p1", Priority: 1, Guard: never},
		&Edge{Name: "p2a", Type: EdgePriority, From: "pick", To: "p2a", Priority: 2},
		&Edge{Name: "p2b", Type: EdgePriority, From: "pick", To: "p2b", Priority: 2})
	h := newEdgeHarnessFor(t, definition)

	expectStatus(t, h.start("e-priority", nil), StatusCompleted)
	// p1 is ineligible; of the two priority-2 edges the earlier declaration wins.
	if got := h.runner.intents(); !slices.Equal(got, []string{"do.pick", "do.p2a"}) {
		t.Fatalf("priority selection = %v", got)
	}
}

// ---------------------------------------------------------------------------
// Concurrency: fanout, dynamic_fanout, parallel
// ---------------------------------------------------------------------------

func TestEdgeFanOutRunsEveryTargetConcurrently(t *testing.T) {
	definition := def("e-fanout", "split",
		[]*Step{step("split", "do.split"), terminal("a", "do.a"), terminal("b", "do.b"), terminal("c", "do.c")},
		&Edge{Name: "out", Type: EdgeFanOut, From: "split", Targets: []string{"a", "b", "c"}})
	h := newEdgeHarnessFor(t, definition)
	for _, intent := range []string{"do.a", "do.b", "do.c"} {
		h.runner.slow(intent, 30*time.Millisecond)
	}

	expectStatus(t, h.start("e-fanout", nil), StatusCompleted)
	for _, intent := range []string{"do.a", "do.b", "do.c"} {
		expectCount(t, h, intent, 1)
	}
	if peak := h.runner.peakConcurrency(); peak != 3 {
		t.Fatalf("fan-out targets ran with peak concurrency %d, want 3", peak)
	}
}

func TestEdgeFanOutToATerminalBranchDoesNotDropTheOtherBranch(t *testing.T) {
	// One branch ends immediately at a terminal step; the other still has two
	// steps to go. Completing the run at the first terminal step would silently
	// drop the ledger write.
	definition := def("e-fanout-terminal", "split",
		[]*Step{step("split", "do.split"), terminal("notify", "do.notify"), step("ledger", "do.ledger"), terminal("audit", "do.audit")},
		&Edge{Name: "out", Type: EdgeFanOut, From: "split", Targets: []string{"notify", "ledger"}},
		edge("ledger-audit", EdgeSimple, "ledger", "audit"))
	h := newEdgeHarnessFor(t, definition)

	expectStatus(t, h.start("e-fanout-terminal", nil), StatusCompleted)
	expectCount(t, h, "do.ledger", 1)
	expectCount(t, h, "do.audit", 1)
}

func TestEdgeMaxConcurrencyBoundsFanOutParallelAndIterators(t *testing.T) {
	for _, kind := range []EdgeType{EdgeFanOut, EdgeParallel, EdgeIterator} {
		t.Run(string(kind), func(t *testing.T) {
			steps := []*Step{step("src", "do.src"), terminal("t1", "do.work"), terminal("t2", "do.work"), terminal("t3", "do.work"), terminal("t4", "do.work")}
			e := &Edge{Name: "out", Type: kind, From: "src", Targets: []string{"t1", "t2", "t3", "t4"}, MaxConcurrency: 2}
			if kind == EdgeIterator {
				steps = []*Step{step("src", "do.src"), terminal("t1", "do.work")}
				e = &Edge{Name: "out", Type: kind, From: "src", To: "t1", ItemsPath: "items", MaxConcurrency: 2}
			}
			h := newEdgeHarnessFor(t, def("e-maxc-"+string(kind), "src", steps, e))
			h.runner.returns("do.src", map[string]any{"items": []any{1, 2, 3, 4}})
			h.runner.slow("do.work", 20*time.Millisecond)

			run := h.start("e-maxc-"+string(kind), nil)
			expectStatus(t, run, StatusCompleted)
			expectCount(t, h, "do.work", 4)
			if peak := h.runner.peakConcurrency(); peak != 2 {
				t.Fatalf("peak concurrency = %d, want max_concurrency 2", peak)
			}
		})
	}
}

func TestEdgeDynamicFanOutDispatchesToRuntimeTargets(t *testing.T) {
	// targets_path alone — the catalog's documented field set — must compile.
	dynamic := def("e-dynamic", "plan",
		[]*Step{step("plan", "do.plan"), terminal("x", "do.x"), terminal("y", "do.y"), terminal("z", "do.z")},
		&Edge{Name: "dispatch", Type: EdgeDynamicFanOut, From: "plan", TargetsPath: "route.to"})
	fallback := def("e-dynamic-fallback", "plan",
		[]*Step{step("plan", "do.plan"), terminal("x", "do.x"), terminal("y", "do.y")},
		&Edge{Name: "dispatch", Type: EdgeDynamicFanOut, From: "plan", TargetsPath: "route.to", Targets: []string{"x", "y"}})
	h := newEdgeHarness(t, []*Definition{dynamic, fallback})
	h.runner.on("do.plan", echo)

	// Undeclared names are dropped: a step result must not be able to dispatch to
	// arbitrary steps.
	expectStatus(t, h.start("e-dynamic", map[string]any{"route": map[string]any{"to": []any{"x", "z", "no-such-step"}}}), StatusCompleted)
	for intent, want := range map[string]int{"do.x": 1, "do.y": 0, "do.z": 1} {
		expectCount(t, h, intent, want)
	}

	// No runtime list: the static targets are the fallback.
	expectStatus(t, h.start("e-dynamic-fallback", map[string]any{}), StatusCompleted)
	expectCount(t, h, "do.x", 2)
	expectCount(t, h, "do.y", 1)

	// Nothing valid to dispatch to and no fallback: a dead end, reported.
	expectStatus(t, h.start("e-dynamic", map[string]any{"route": map[string]any{"to": []any{"nope"}}}), StatusFailed)
}

func TestEdgeParallelFailFastDropsSiblingsThatHaveNotStarted(t *testing.T) {
	build := func(name string, failFast bool) *Definition {
		return def(name, "src",
			[]*Step{step("src", "do.src"), terminal("a", "do.a"), terminal("b", "do.b"), terminal("c", "do.c"), terminal("handler", "do.handler")},
			&Edge{Name: "par", Type: EdgeParallel, From: "src", Targets: []string{"a", "b", "c"}, MaxConcurrency: 1, FailFast: failFast},
			edge("a-failed", EdgeError, "a", "handler"))
	}
	h := newEdgeHarness(t, []*Definition{build("e-par-ff", true), build("e-par-slow", false)})
	h.runner.fails("do.a", "a broke")

	expectStatus(t, h.start("e-par-ff", nil), StatusCompleted)
	expectCount(t, h, "do.handler", 1)
	expectCount(t, h, "do.b", 0)
	expectCount(t, h, "do.c", 0)

	// Without fail_fast the siblings still run after a caught failure.
	expectStatus(t, h.start("e-par-slow", nil), StatusCompleted)
	expectCount(t, h, "do.b", 1)
	expectCount(t, h, "do.c", 1)
}

func TestEdgeParallelContinueOnErrorCollectsTheFailureAndContinues(t *testing.T) {
	build := func(name string, continueOnError bool) *Definition {
		return def(name, "src",
			[]*Step{step("src", "do.src"), terminal("a", "do.a"), terminal("b", "do.b"), terminal("c", "do.c")},
			&Edge{Name: "par", Type: EdgeParallel, From: "src", Targets: []string{"a", "b", "c"}, ContinueOnError: continueOnError})
	}
	h := newEdgeHarness(t, []*Definition{build("e-par-coe", true), build("e-par-strict", false)})
	h.runner.fails("do.b", "b broke")

	run := h.start("e-par-coe", nil)
	expectStatus(t, run, StatusCompleted)
	expectCount(t, h, "do.a", 1)
	expectCount(t, h, "do.c", 1)
	states, _ := h.store.ListSteps(context.Background(), run.ID)
	failed := slices.ContainsFunc(states, func(s *StepState) bool { return s.Step == "b" && s.Status == StepFailed })
	if !failed {
		t.Fatal("the collected failure is not recorded against its step")
	}

	expectStatus(t, h.start("e-par-strict", nil), StatusFailed)
}

// ---------------------------------------------------------------------------
// Joins: fanin, join, quorum
// ---------------------------------------------------------------------------

func joinDefinition(name string, kind EdgeType, strategy string, quorum int, sources ...string) *Definition {
	steps := []*Step{step("split", "do.split"), terminal("finish", "do.finish")}
	for _, source := range sources {
		steps = append(steps, step(source, "do."+source))
	}
	return def(name, "split", steps,
		&Edge{Name: "out", Type: EdgeFanOut, From: "split", Targets: sources},
		&Edge{Name: "in", Type: kind, Sources: sources, To: "finish", Strategy: strategy, Quorum: quorum})
}

func TestEdgeFanInAllWaitsForEverySourceAndFiresOnce(t *testing.T) {
	h := newEdgeHarnessFor(t, joinDefinition("e-fanin", EdgeFanIn, "all", 0, "a", "b", "c"))
	h.runner.slow("do.b", 30*time.Millisecond)
	h.runner.returns("do.a", map[string]any{"v": "A"})

	expectStatus(t, h.start("e-fanin", nil), StatusCompleted)
	calls := h.runner.callsTo("do.finish")
	if len(calls) != 1 {
		t.Fatalf("the join fired %d times", len(calls))
	}
	results, _ := field(calls[0].Input, "results").(map[string]any)
	if len(results) != 3 || field(results, "a.v") != "A" {
		t.Fatalf("the join did not carry every source's result: %v", calls[0].Input)
	}
}

func TestEdgeFanInWhoseSourceFailsFailsTheRunInsteadOfWaiting(t *testing.T) {
	h := newEdgeHarnessFor(t, joinDefinition("e-fanin-fail", EdgeFanIn, "all", 0, "a", "b"))
	h.runner.fails("do.b", "b broke")

	run := h.start("e-fanin-fail", nil)
	expectStatus(t, run, StatusFailed)
	expectCount(t, h, "do.finish", 0)
}

func TestEdgeFanInAfterABranchThatWasNotTakenDoesNotCompleteSilently(t *testing.T) {
	build := func(name, strategy string) *Definition {
		return def(name, "decide",
			[]*Step{step("decide", "do.decide"), step("a", "do.a"), step("b", "do.b"), terminal("finish", "do.finish")},
			&Edge{Name: "to-a", Type: EdgeBranch, From: "decide", To: "a", Guard: always},
			&Edge{Name: "to-b", Type: EdgeBranch, From: "decide", To: "b", Guard: never},
			&Edge{Name: "in", Type: EdgeFanIn, Sources: []string{"a", "b"}, To: "finish", Strategy: strategy})
	}
	h := newEdgeHarness(t, []*Definition{build("e-fanin-untaken", "all"), build("e-fanin-partial", "partial_success")})

	// "all" can never be met: b will not run. Completing the run without finish
	// would report success for work that was never done.
	run := h.start("e-fanin-untaken", nil)
	expectStatus(t, run, StatusFailed)
	if !strings.Contains(run.Error, "in") || !strings.Contains(run.Error, "b") {
		t.Fatalf("the failure does not name the join and the missing source: %q", run.Error)
	}
	expectCount(t, h, "do.finish", 0)

	// partial_success continues with what arrived once nothing more can.
	expectStatus(t, h.start("e-fanin-partial", nil), StatusCompleted)
	expectCount(t, h, "do.finish", 1)
}

func TestEdgeJoinPartialSuccessCarriesFailuresForward(t *testing.T) {
	h := newEdgeHarnessFor(t, joinDefinition("e-join-partial", EdgeJoin, "partial_success", 0, "a", "b", "c"))
	h.runner.fails("do.b", "b broke")

	expectStatus(t, h.start("e-join-partial", nil), StatusCompleted)
	calls := h.runner.callsTo("do.finish")
	if len(calls) != 1 {
		t.Fatalf("finish ran %d times", len(calls))
	}
	if field(calls[0].Input, "errors.b") != "b broke" {
		t.Fatalf("the failure was not carried forward: %v", calls[0].Input)
	}
	if results, _ := field(calls[0].Input, "results").(map[string]any); len(results) != 2 {
		t.Fatalf("results = %v, want a and c", results)
	}

	// Every source failing leaves partial_success nothing to continue with.
	h.runner.fails("do.a", "a broke")
	h.runner.fails("do.c", "c broke")
	expectStatus(t, h.start("e-join-partial", nil), StatusFailed)
	expectCount(t, h, "do.finish", 1)
}

func TestEdgeJoinFirstFailureFiresOnTheFirstFailureOnly(t *testing.T) {
	definition := def("e-join-ff", "split",
		[]*Step{step("split", "do.split"), step("a", "do.a"), step("b", "do.b"), terminal("alert", "do.alert")},
		&Edge{Name: "out", Type: EdgeFanOut, From: "split", Targets: []string{"a", "b"}},
		&Edge{Name: "in", Type: EdgeJoin, Sources: []string{"a", "b"}, To: "alert", Strategy: "first_failure"})
	h := newEdgeHarnessFor(t, definition)

	// Nobody failed: the failure handler does not fire and the run completes.
	expectStatus(t, h.start("e-join-ff", nil), StatusCompleted)
	expectCount(t, h, "do.alert", 0)

	h.runner.fails("do.b", "b broke")
	expectStatus(t, h.start("e-join-ff", nil), StatusCompleted)
	expectCount(t, h, "do.alert", 1)
}

func TestEdgeJoinAnyFiresOnTheFirstSuccessOnce(t *testing.T) {
	h := newEdgeHarnessFor(t, joinDefinition("e-join-any", EdgeJoin, "any", 0, "a", "b"))
	h.runner.slow("do.b", 20*time.Millisecond)
	expectStatus(t, h.start("e-join-any", nil), StatusCompleted)
	expectCount(t, h, "do.finish", 1)

	// A failed source does not stop "any" while another can still succeed.
	h.runner.fails("do.a", "a broke")
	expectStatus(t, h.start("e-join-any", nil), StatusCompleted)
	expectCount(t, h, "do.finish", 2)

	// Every source failed: nothing can satisfy "any".
	h.runner.fails("do.b", "b broke")
	expectStatus(t, h.start("e-join-any", nil), StatusFailed)
	expectCount(t, h, "do.finish", 2)
}

func TestEdgeJoinCountsASkippedSourceAsReported(t *testing.T) {
	definition := joinDefinition("e-join-skip", EdgeJoin, "all", 0, "a", "b")
	definition.Steps["b"].SkipWhen = always
	definition.Steps["b"].SkipResult = map[string]any{"skipped": true}
	h := newEdgeHarnessFor(t, definition)

	expectStatus(t, h.start("e-join-skip", nil), StatusCompleted)
	expectCount(t, h, "do.b", 0)
	calls := h.runner.callsTo("do.finish")
	if len(calls) != 1 || field(calls[0].Input, "results.b.skipped") != true {
		t.Fatalf("the skipped source's skip result did not reach the join: %v", calls)
	}
}

func TestEdgeQuorumDefaultsToAMajorityAndFailsWhenUnreachable(t *testing.T) {
	h := newEdgeHarnessFor(t, joinDefinition("e-quorum", EdgeQuorum, "", 0, "a", "b", "c"))
	h.runner.fails("do.c", "c broke")

	// Two of three is a majority, so one failure is tolerated.
	expectStatus(t, h.start("e-quorum", nil), StatusCompleted)
	expectCount(t, h, "do.finish", 1)

	// Two failures make a majority impossible: the run fails instead of waiting
	// for a quorum that cannot arrive.
	h.runner.fails("do.b", "b broke")
	run := h.start("e-quorum", nil)
	expectStatus(t, run, StatusFailed)
	expectCount(t, h, "do.finish", 1)
}

// ---------------------------------------------------------------------------
// Race
// ---------------------------------------------------------------------------

func raceDefinition(name string, cancelLosers bool, timeout time.Duration, onTimeout string, targets ...*Step) *Definition {
	steps := []*Step{step("start", "do.start"), terminal("next", "do.next"), terminal("gave-up", "do.gaveup")}
	names := make([]string, 0, len(targets))
	edges := []*Edge{}
	for _, target := range targets {
		steps = append(steps, target)
		names = append(names, target.Name)
		edges = append(edges, edge(target.Name+"-next", EdgeSimple, target.Name, "next"))
	}
	race := &Edge{Name: "race", Type: EdgeRace, From: "start", Targets: names, CancelLosers: cancelLosers, Timeout: timeout, OnTimeout: onTimeout}
	// gave-up must be reachable even when the race has no on_timeout.
	edges = append(edges, race, &Edge{Name: "unused", Type: EdgeError, From: "start", To: "gave-up"})
	return def(name, "start", steps, edges...)
}

func TestEdgeRaceTheFirstToFinishWinsAndOnlyItContinues(t *testing.T) {
	// slow is declared first so declaration order and finishing order disagree.
	h := newEdgeHarnessFor(t, raceDefinition("e-race", true, 0, "", step("slow", "do.slow"), step("fast", "do.fast")))
	h.runner.slow("do.slow", 40*time.Millisecond)
	h.runner.returns("do.slow", map[string]any{"winner": "slow"})
	h.runner.returns("do.fast", map[string]any{"winner": "fast"})

	expectStatus(t, h.start("e-race", nil), StatusCompleted)
	calls := h.runner.callsTo("do.next")
	if len(calls) != 1 {
		t.Fatalf("the race continued %d times, want exactly once", len(calls))
	}
	if got := field(calls[0].Input, "winner"); got != "fast" {
		t.Fatalf("winner = %v, want the first to finish", got)
	}
}

func TestEdgeRaceCancelLosersClosesAParkedLoser(t *testing.T) {
	build := func(name string, cancel bool) *Definition {
		return raceDefinition(name, cancel, 0, "", taskStep("human", "clerks"), step("machine", "do.machine"))
	}
	h := newEdgeHarness(t, []*Definition{build("e-race-cancel", true), build("e-race-keep", false)})

	run := h.start("e-race-cancel", nil)
	expectStatus(t, run, StatusCompleted)
	expectCount(t, h, "do.next", 1)
	if open := h.openTasks(run.ID); len(open) != 0 {
		t.Fatalf("the losing task is still open: %+v", open[0])
	}

	// Without cancel_losers the loser's work is left to finish, but it never
	// continues the graph a second time.
	run = h.start("e-race-keep", nil)
	expectStatus(t, run, StatusWaiting)
	if err := h.completeTask(h.taskFor(run.ID, "human"), ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	expectStatus(t, h.get(run.ID), StatusCompleted)
	expectCount(t, h, "do.next", 2)
}

func TestEdgeRaceIgnoresALosingFailureButFailsWhenEveryEntrantFails(t *testing.T) {
	h := newEdgeHarnessFor(t, raceDefinition("e-race-fail", false, 0, "", step("bad", "do.bad"), step("good", "do.good")))
	h.runner.fails("do.bad", "bad broke")

	expectStatus(t, h.start("e-race-fail", nil), StatusCompleted)
	expectCount(t, h, "do.next", 1)

	h.runner.fails("do.good", "good broke")
	run := h.start("e-race-fail", nil)
	expectStatus(t, run, StatusFailed)
	expectCount(t, h, "do.next", 1)
}

func TestEdgeRaceTimeoutTakesOnTimeoutAndClosesTheEntrants(t *testing.T) {
	build := func(name, onTimeout string) *Definition {
		return raceDefinition(name, true, time.Minute, onTimeout, taskStep("left", "clerks"), taskStep("right", "clerks"))
	}
	h := newEdgeHarness(t, []*Definition{build("e-race-timeout", "gave-up"), build("e-race-timeout-fail", "")})

	run := h.start("e-race-timeout", nil)
	expectStatus(t, run, StatusWaiting)
	h.passTime(59 * time.Second)
	expectStatus(t, h.get(run.ID), StatusWaiting)
	h.passTime(time.Second)
	expectStatus(t, h.get(run.ID), StatusCompleted)
	expectCount(t, h, "do.gaveup", 1)
	expectCount(t, h, "do.next", 0)
	if open := h.openTasks(run.ID); len(open) != 0 {
		t.Fatalf("race entrants still open after the timeout: %d", len(open))
	}

	// Decided before the deadline: the deadline must not hold the run open or
	// fire later.
	run = h.start("e-race-timeout", nil)
	if err := h.completeTask(h.taskFor(run.ID, "left"), ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	expectStatus(t, h.get(run.ID), StatusCompleted)
	h.passTime(time.Hour)
	expectCount(t, h, "do.gaveup", 1)

	// No on_timeout: an expired race is a failure.
	run = h.start("e-race-timeout-fail", nil)
	h.passTime(time.Minute)
	expectStatus(t, h.get(run.ID), StatusFailed)
}

// ---------------------------------------------------------------------------
// Iteration: iterator, batch_iterator, loop_until
// ---------------------------------------------------------------------------

func TestEdgeIteratorRunsOncePerItemWithItsOwnStateKey(t *testing.T) {
	definition := def("e-iterator", "load",
		[]*Step{step("load", "do.load"), terminal("each", "do.each"), terminal("after", "do.after")},
		&Edge{Name: "items", Type: EdgeIterator, From: "load", To: "each", ItemsPath: "list"},
		edge("after", EdgeSimple, "load", "after"))
	h := newEdgeHarnessFor(t, definition)
	h.runner.on("do.load", echo)

	run := h.start("e-iterator", map[string]any{"list": []any{"x", "y", "z"}})
	expectStatus(t, run, StatusCompleted)
	calls := h.runner.callsTo("do.each")
	if len(calls) != 3 {
		t.Fatalf("each ran %d times", len(calls))
	}
	keys := []string{}
	for _, call := range calls {
		keys = append(keys, call.Key)
		index := int(field(call.Input, "index").(float64))
		if field(call.Input, "item") != []string{"x", "y", "z"}[index] || field(call.Input, "total") != 3.0 {
			t.Fatalf("iteration input = %v", call.Input)
		}
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"each[0]", "each[1]", "each[2]"}) {
		t.Fatalf("iteration keys = %v", keys)
	}

	// An empty collection runs the body zero times and is not a failure.
	expectStatus(t, h.start("e-iterator", map[string]any{"list": []any{}}), StatusCompleted)
	expectCount(t, h, "do.each", 3)
	expectCount(t, h, "do.after", 2)
}

func TestEdgeIteratorContinueOnErrorKeepsTheOtherItems(t *testing.T) {
	build := func(name string, continueOnError bool) *Definition {
		return def(name, "load",
			[]*Step{step("load", "do.load"), terminal("each", "do.each")},
			&Edge{Name: "items", Type: EdgeIterator, From: "load", To: "each", ItemsPath: "list", ContinueOnError: continueOnError})
	}
	h := newEdgeHarness(t, []*Definition{build("e-iter-coe", true), build("e-iter-strict", false)})
	h.runner.on("do.load", echo)
	h.runner.on("do.each", func(call StepCall, _ int) (any, error) {
		if field(call.Input, "item") == "bad" {
			return nil, errors.New("bad item")
		}
		return map[string]any{"ok": true}, nil
	})

	expectStatus(t, h.start("e-iter-coe", map[string]any{"list": []any{"a", "bad", "c"}}), StatusCompleted)
	expectCount(t, h, "do.each", 3)
	expectStatus(t, h.start("e-iter-strict", map[string]any{"list": []any{"a", "bad", "c"}}), StatusFailed)
}

func TestEdgeBatchIteratorRunsOncePerBatch(t *testing.T) {
	definition := def("e-batch", "load",
		[]*Step{step("load", "do.load"), terminal("each", "do.batch")},
		&Edge{Name: "batches", Type: EdgeBatchIterator, From: "load", To: "each", ItemsPath: "list", BatchSize: 2})
	defaulted := def("e-batch-default", "load",
		[]*Step{step("load", "do.load"), terminal("each", "do.batch-default")},
		&Edge{Name: "batches", Type: EdgeBatchIterator, From: "load", To: "each", ItemsPath: "list"})
	h := newEdgeHarness(t, []*Definition{definition, defaulted})
	h.runner.on("do.load", echo)

	expectStatus(t, h.start("e-batch", map[string]any{"list": []any{1, 2, 3, 4, 5}}), StatusCompleted)
	calls := h.runner.callsTo("do.batch")
	if len(calls) != 3 {
		t.Fatalf("batch body ran %d times, want 3", len(calls))
	}
	sizes := map[int]int{}
	for _, call := range calls {
		index := int(field(call.Input, "index").(float64))
		batch, _ := field(call.Input, "item").([]any)
		sizes[index] = len(batch)
		if field(call.Input, "total") != 3.0 {
			t.Fatalf("total = %v", field(call.Input, "total"))
		}
	}
	if sizes[0] != 2 || sizes[1] != 2 || sizes[2] != 1 {
		t.Fatalf("batch sizes = %v, want 2,2,1", sizes)
	}

	// The default batch size is 25.
	items := make([]any, 30)
	for i := range items {
		items[i] = i
	}
	expectStatus(t, h.start("e-batch-default", map[string]any{"list": items}), StatusCompleted)
	expectCount(t, h, "do.batch-default", 2)
}

func TestEdgeLoopUntilRepeatsUntilTheConditionHoldsThenExitsOnce(t *testing.T) {
	definition := def("e-loop", "body",
		[]*Step{step("body", "do.body"), terminal("after", "do.after")},
		&Edge{Name: "again", Type: EdgeLoopUntil, From: "body", To: "body", Guard: exprGuard{path: "result.done", equal: true}},
		edge("exit", EdgeSimple, "body", "after"))
	h := newEdgeHarnessFor(t, definition)
	h.runner.on("do.body", func(_ StepCall, n int) (any, error) {
		return map[string]any{"n": n, "done": n >= 3}, nil
	})

	run := h.start("e-loop", nil)
	expectStatus(t, run, StatusCompleted)
	expectCount(t, h, "do.body", 3)
	// The exit path runs once, after the loop — not once per iteration.
	calls := h.runner.callsTo("do.after")
	if len(calls) != 1 || field(calls[0].Input, "n") != 3.0 {
		t.Fatalf("exit ran %d times with %v", len(calls), calls)
	}
}

func TestEdgeLoopUntilIsBoundedWhenTheConditionNeverHolds(t *testing.T) {
	definition := def("e-loop-bounded", "body",
		[]*Step{step("body", "do.body"), terminal("after", "do.after")},
		&Edge{Name: "again", Type: EdgeLoopUntil, From: "body", To: "body", Guard: never, MaxConcurrency: 4},
		edge("exit", EdgeSimple, "body", "after"))
	h := newEdgeHarnessFor(t, definition)

	run := h.start("e-loop-bounded", nil)
	expectStatus(t, run, StatusFailed)
	expectCount(t, h, "do.body", 4)
	expectCount(t, h, "do.after", 0)
	if !strings.Contains(run.Error, "again") {
		t.Fatalf("the failure does not name the loop: %q", run.Error)
	}
	// Failed, not wedged: another advance does nothing and runs nothing.
	h.advance(run.ID)
	expectCount(t, h, "do.body", 4)
}

// ---------------------------------------------------------------------------
// Reliability: retry, timeout, rate_limited, error, fallback, compensate
// ---------------------------------------------------------------------------

func TestEdgeRetryRetriesTheTargetWithBackoff(t *testing.T) {
	definition := def("e-retry", "start",
		[]*Step{step("start", "do.start"), step("flaky", "do.flaky"), terminal("done", "do.done")},
		&Edge{Name: "call", Type: EdgeRetry, From: "start", To: "flaky", Attempts: 3, Timeout: time.Second},
		edge("ok", EdgeSimple, "flaky", "done"))
	h := newEdgeHarnessFor(t, definition)
	h.runner.on("do.flaky", func(_ StepCall, n int) (any, error) {
		if n < 3 {
			return nil, fmt.Errorf("attempt %d failed", n)
		}
		return map[string]any{"attempt": n}, nil
	})

	run := h.start("e-retry", nil)
	expectStatus(t, run, StatusWaiting)
	expectCount(t, h, "do.flaky", 1)

	// The first backoff is the edge's timeout.
	h.passTime(999 * time.Millisecond)
	expectCount(t, h, "do.flaky", 1)
	h.passTime(time.Millisecond)
	expectCount(t, h, "do.flaky", 2)

	// Then it grows.
	h.passTime(1999 * time.Millisecond)
	expectCount(t, h, "do.flaky", 2)
	h.passTime(time.Millisecond)
	expectCount(t, h, "do.flaky", 3)
	expectStatus(t, h.get(run.ID), StatusCompleted)
	expectCount(t, h, "do.done", 1)
	if calls := h.runner.callsTo("do.flaky"); calls[2].Attempt != 3 {
		t.Fatalf("attempt numbers not carried: %+v", calls)
	}
	if timers := h.timers(run.ID); len(timers) != 0 {
		t.Fatalf("timers left behind: %s", describeTimers(timers))
	}
}

func TestEdgeRetryExhaustionTakesTheErrorPath(t *testing.T) {
	definition := def("e-retry-exhausted", "start",
		[]*Step{step("start", "do.start"), step("flaky", "do.flaky"), terminal("done", "do.done"), terminal("handler", "do.handler")},
		&Edge{Name: "call", Type: EdgeRetry, From: "start", To: "flaky", Attempts: 2, Timeout: time.Second},
		edge("ok", EdgeSimple, "flaky", "done"),
		edge("gave-up", EdgeError, "flaky", "handler"))
	h := newEdgeHarnessFor(t, definition)
	h.runner.fails("do.flaky", "always")

	run := h.start("e-retry-exhausted", nil)
	h.passTime(time.Second)
	expectStatus(t, h.get(run.ID), StatusCompleted)
	expectCount(t, h, "do.flaky", 2)
	expectCount(t, h, "do.handler", 1)
	expectCount(t, h, "do.done", 0)
}

func TestEdgeRetryAdvancedDirectlyDoesNotStrandTheRun(t *testing.T) {
	// Advancing a retrying run by hand (as a queue worker does) instead of via its
	// wake timer must not leave the wake timer holding a finished run open.
	definition := def("e-retry-direct", "start",
		[]*Step{step("start", "do.start"), step("flaky", "do.flaky"), step("mid", "do.mid"), terminal("done", "do.done")},
		&Edge{Name: "call", Type: EdgeRetry, From: "start", To: "flaky", Attempts: 2, Timeout: time.Second},
		edge("ok", EdgeSimple, "flaky", "mid"), edge("fin", EdgeSimple, "mid", "done"))
	h := newEdgeHarnessFor(t, definition)
	h.runner.on("do.flaky", func(_ StepCall, n int) (any, error) {
		if n == 1 {
			return nil, errors.New("first attempt fails")
		}
		return map[string]any{}, nil
	})

	run := h.start("e-retry-direct", nil)
	h.clock.Add(time.Second)
	h.advance(run.ID)
	expectStatus(t, h.get(run.ID), StatusCompleted)
	h.tick()
	expectStatus(t, h.get(run.ID), StatusCompleted)
}

func TestEdgeTimeoutDoesNotHoldAFastTargetOpen(t *testing.T) {
	definition := def("e-timeout-fast", "start",
		[]*Step{step("start", "do.start"), step("work", "do.work"), terminal("done", "do.done"), terminal("late", "do.late")},
		&Edge{Name: "bounded", Type: EdgeTimeout, From: "start", To: "work", Timeout: time.Hour, OnTimeout: "late"},
		edge("ok", EdgeSimple, "work", "done"))
	h := newEdgeHarnessFor(t, definition)

	run := h.start("e-timeout-fast", nil)
	// The target finished well inside its deadline: the run is done now, not in
	// an hour, and the on_timeout path never runs.
	expectStatus(t, run, StatusCompleted)
	h.passTime(2 * time.Hour)
	expectCount(t, h, "do.late", 0)
	expectStatus(t, h.get(run.ID), StatusCompleted)
}

func TestEdgeTimeoutTakesOnTimeoutWhenAParkedTargetOverruns(t *testing.T) {
	build := func(name, onTimeout string) *Definition {
		return def(name, "start",
			[]*Step{step("start", "do.start"), taskStep("approve", "clerks"), terminal("done", "do.done"), terminal("late", "do.late")},
			&Edge{Name: "bounded", Type: EdgeTimeout, From: "start", To: "approve", Timeout: time.Minute, OnTimeout: onTimeout},
			edge("ok", EdgeSimple, "approve", "done"),
			&Edge{Name: "reach-late", Type: EdgeError, From: "start", To: "late"})
	}
	h := newEdgeHarness(t, []*Definition{build("e-timeout-task", "late"), build("e-timeout-task-fail", "")})

	run := h.start("e-timeout-task", nil)
	expectStatus(t, run, StatusWaiting)
	task := h.taskFor(run.ID, "approve")
	h.passTime(time.Minute)
	final := h.get(run.ID)
	expectStatus(t, final, StatusCompleted)
	expectCount(t, h, "do.late", 1)
	expectCount(t, h, "do.done", 0)
	if calls := h.runner.callsTo("do.late"); field(calls[0].Input, "timed_out") != true {
		t.Fatalf("on_timeout input = %v", calls[0].Input)
	}
	// The overrun task is closed: completing it now must not resume the run.
	if err := h.completeTask(task, ""); err == nil {
		t.Fatal("a task whose deadline passed could still be completed")
	}
	expectCount(t, h, "do.done", 0)

	// Completed inside the deadline: the normal path, no on_timeout later.
	run = h.start("e-timeout-task", nil)
	if err := h.completeTask(h.taskFor(run.ID, "approve"), ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	expectStatus(t, h.get(run.ID), StatusCompleted)
	h.passTime(time.Hour)
	expectCount(t, h, "do.late", 1)
	expectCount(t, h, "do.done", 1)

	// No on_timeout: an overrun fails the run.
	run = h.start("e-timeout-task-fail", nil)
	h.passTime(time.Minute)
	expectStatus(t, h.get(run.ID), StatusFailed)
}

func TestEdgeTimeoutBoundsASlowStepBody(t *testing.T) {
	definition := def("e-timeout-body", "start",
		[]*Step{step("start", "do.start"), step("work", "do.work"), terminal("done", "do.done"), terminal("late", "do.late")},
		&Edge{Name: "bounded", Type: EdgeTimeout, From: "start", To: "work", Timeout: 20 * time.Millisecond, OnTimeout: "late"},
		edge("ok", EdgeSimple, "work", "done"))
	h := newEdgeHarnessFor(t, definition)
	h.runner.slow("do.work", 2*time.Second)

	began := time.Now()
	run := h.start("e-timeout-body", nil)
	if elapsed := time.Since(began); elapsed > time.Second {
		t.Fatalf("the step body ran for %s despite a 20ms timeout edge", elapsed)
	}
	expectStatus(t, run, StatusCompleted)
	expectCount(t, h, "do.late", 1)
	expectCount(t, h, "do.done", 0)
}

func TestEdgeRateLimitedParksUntilTheLimiterAdmits(t *testing.T) {
	definition := def("e-rate", "start",
		[]*Step{step("start", "do.start"), terminal("call", "do.call")},
		&Edge{Name: "throttled", Type: EdgeRateLimited, From: "start", To: "call", RateLimit: "api", Limit: 1, Window: time.Minute})
	h := newEdgeHarnessFor(t, definition)
	h.limiter.set(false)

	run := h.start("e-rate", nil)
	expectStatus(t, run, StatusWaiting)
	expectCount(t, h, "do.call", 0)

	// The window ends but the limiter still refuses: the run parks again rather
	// than slipping past the limit.
	h.passTime(time.Minute)
	expectStatus(t, h.get(run.ID), StatusWaiting)
	expectCount(t, h, "do.call", 0)

	h.limiter.set(true)
	h.passTime(time.Minute)
	expectStatus(t, h.get(run.ID), StatusCompleted)
	expectCount(t, h, "do.call", 1)
}

func TestEdgeRateLimitedWithoutALimiterFailsInsteadOfWedging(t *testing.T) {
	definition := def("e-rate-none", "start",
		[]*Step{step("start", "do.start"), terminal("call", "do.call")},
		&Edge{Name: "throttled", Type: EdgeRateLimited, From: "start", To: "call", RateLimit: "api", Limit: 1, Window: time.Minute})
	h := newEdgeHarnessFor(t, definition, withoutLimiter())

	run, err := boundedCall(t, "Start", func() (*Run, error) {
		return h.engine.Start(context.Background(), "e-rate-none", nil, StartOptions{})
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	expectStatus(t, run, StatusFailed)
	if !strings.Contains(run.Error, "rate limiter") {
		t.Fatalf("error = %q", run.Error)
	}
	h.advance(run.ID)
	expectCount(t, h, "do.start", 1)
}

func TestEdgeErrorAndFallbackFireOnlyOnFailure(t *testing.T) {
	definition := def("e-error", "risky",
		[]*Step{step("risky", "do.risky"), terminal("onward", "do.onward"), terminal("specific", "do.specific"),
			terminal("fallback", "do.fallback"), terminal("errored", "do.errored")},
		edge("ok", EdgeSimple, "risky", "onward"),
		&Edge{Name: "specific", Type: EdgeFallback, From: "risky", To: "specific", Guard: exprGuard{path: "result.error", equal: "quota"}},
		&Edge{Name: "fallback", Type: EdgeFallback, From: "risky", To: "fallback", Guard: exprGuard{path: "result.error", equal: "quota", negate: true}},
		&Edge{Name: "errored", Type: EdgeError, From: "risky", To: "errored", Guard: exprGuard{path: "result.error", equal: "fatal"}})
	h := newEdgeHarnessFor(t, definition)

	// Success: no error-path edge fires.
	expectStatus(t, h.start("e-error", nil), StatusCompleted)
	if got := h.runner.intents(); !slices.Equal(got, []string{"do.risky", "do.onward"}) {
		t.Fatalf("success order = %v", got)
	}

	// Failure: only the matching error-path edges fire, never the success edge.
	h.runner.fails("do.risky", "quota")
	expectStatus(t, h.start("e-error", nil), StatusCompleted)
	expectCount(t, h, "do.specific", 1)
	expectCount(t, h, "do.fallback", 0)
	expectCount(t, h, "do.onward", 1)

	h.runner.fails("do.risky", "fatal")
	expectStatus(t, h.start("e-error", nil), StatusCompleted)
	expectCount(t, h, "do.fallback", 1)
	expectCount(t, h, "do.errored", 1)
}

func TestEdgeCompensateRollsBackCommittedStepsInReverse(t *testing.T) {
	definition := def("e-compensate", "reserve",
		[]*Step{
			{Name: "reserve", Intent: "do.reserve", Compensate: "undo.reserve"},
			{Name: "split", Intent: "do.split"},
			{Name: "left", Intent: "do.left", Compensate: "undo.left"},
			{Name: "right", Intent: "do.right", Compensate: "undo.right"},
			{Name: "charge", Intent: "do.charge", Compensate: "undo.charge"},
			terminal("ship", "do.ship"),
		},
		edge("r-s", EdgeSimple, "reserve", "split"),
		&Edge{Name: "out", Type: EdgeFanOut, From: "split", Targets: []string{"left", "right"}},
		&Edge{Name: "in", Type: EdgeJoin, Sources: []string{"left", "right"}, To: "charge"},
		edge("c-s", EdgeSimple, "charge", "ship"),
		&Edge{Name: "rollback", Type: EdgeCompensate, From: "charge", To: "ship"})
	h := newEdgeHarnessFor(t, definition)
	h.runner.slow("do.left", 20*time.Millisecond)

	// Success: nothing is compensated.
	expectStatus(t, h.start("e-compensate", nil), StatusCompleted)
	for _, undo := range []string{"undo.reserve", "undo.left", "undo.right", "undo.charge"} {
		expectCount(t, h, undo, 0)
	}

	h.runner.fails("do.charge", "card declined")
	run := h.start("e-compensate", nil)
	expectStatus(t, run, StatusFailed)
	var undos []string
	for _, intent := range h.runner.intents() {
		if strings.HasPrefix(intent, "undo.") {
			undos = append(undos, intent)
		}
	}
	// right finished before left (left is slow), so left is undone first; the
	// failed charge never committed and is not undone.
	if want := []string{"undo.left", "undo.right", "undo.reserve"}; !slices.Equal(undos, want) {
		t.Fatalf("compensation order = %v, want %v", undos, want)
	}
	if !strings.Contains(run.Error, "rolled back") {
		t.Fatalf("error = %q", run.Error)
	}
}

// ---------------------------------------------------------------------------
// Suspension: delayed, wait_event, manual, escalation
// ---------------------------------------------------------------------------

func TestEdgeDelayedParksForItsDurationAndCarriesThePayload(t *testing.T) {
	definition := def("e-delayed", "first",
		[]*Step{step("first", "do.first"), step("second", "do.second"), terminal("third", "do.third")},
		&Edge{Name: "pause", Type: EdgeDelayed, From: "first", To: "second", Timeout: time.Hour},
		edge("then", EdgeSimple, "second", "third"))
	h := newEdgeHarnessFor(t, definition)
	h.runner.returns("do.first", map[string]any{"token": "abc"})

	run := h.start("e-delayed", nil)
	expectStatus(t, run, StatusWaiting)
	h.passTime(59 * time.Minute)
	expectCount(t, h, "do.second", 0)
	h.passTime(time.Minute)
	// second is not terminal: completing needs the run to settle with the fired
	// timer already consumed, not parked on its own claimed row.
	expectStatus(t, h.get(run.ID), StatusCompleted)
	if calls := h.runner.callsTo("do.second"); len(calls) != 1 || field(calls[0].Input, "token") != "abc" {
		t.Fatalf("delayed payload = %v", calls)
	}
	if timers := h.timers(run.ID); len(timers) != 0 {
		t.Fatalf("timers left behind: %s", describeTimers(timers))
	}
}

func TestEdgeWaitEventCorrelatesDeliversAndTimesOut(t *testing.T) {
	build := func(name, onTimeout string) *Definition {
		return def(name, "ask",
			[]*Step{step("ask", "do.ask"), step("answer", "do.answer"), terminal("after", "do.after"), terminal("late", "do.late")},
			&Edge{Name: "await", Type: EdgeWaitEvent, From: "ask", To: "answer", Event: "reply",
				Correlation: pathValuer{path: "run.input.order"}, Timeout: time.Hour, OnTimeout: onTimeout},
			edge("then", EdgeSimple, "answer", "after"),
			&Edge{Name: "reach-late", Type: EdgeError, From: "ask", To: "late"})
	}
	h := newEdgeHarness(t, []*Definition{build("e-wait", "late"), build("e-wait-fail", "")})

	run := h.start("e-wait", map[string]any{"order": "o-1"})
	expectStatus(t, run, StatusWaiting)
	if woken := h.signal("reply", "o-2", nil); len(woken) != 0 {
		t.Fatalf("an uncorrelated event woke %v", woken)
	}
	if woken := h.signal("reply", "o-1", map[string]any{"answer": 42}); len(woken) != 1 {
		t.Fatalf("the correlated event woke %v", woken)
	}
	expectStatus(t, h.get(run.ID), StatusCompleted)
	if calls := h.runner.callsTo("do.answer"); field(calls[0].Input, "answer") != 42.0 {
		t.Fatalf("the event payload was not delivered: %v", calls[0].Input)
	}
	h.passTime(2 * time.Hour)
	expectCount(t, h, "do.late", 0)

	// Timeout with on_timeout: the alternative runs, a late event is ignored.
	run = h.start("e-wait", map[string]any{"order": "o-3"})
	h.passTime(time.Hour)
	expectStatus(t, h.get(run.ID), StatusCompleted)
	expectCount(t, h, "do.late", 1)
	if woken := h.signal("reply", "o-3", nil); len(woken) != 0 {
		t.Fatalf("an event after the timeout woke %v", woken)
	}
	expectCount(t, h, "do.answer", 1)

	// Timeout without on_timeout fails the run.
	run = h.start("e-wait-fail", map[string]any{"order": "o-4"})
	h.passTime(time.Hour)
	expectStatus(t, h.get(run.ID), StatusFailed)
}

func TestEdgeWaitEventParallelDeadlinesAreIndependent(t *testing.T) {
	// Two branches each wait for their own event with their own deadline. The
	// first event arriving must not cancel the other branch's deadline, or that
	// branch would wait forever.
	definition := def("e-wait-pair", "split",
		[]*Step{step("split", "do.split"), step("a", "do.a"), step("b", "do.b"),
			terminal("a-done", "do.a-done"), terminal("b-done", "do.b-done"), terminal("b-late", "do.b-late")},
		&Edge{Name: "out", Type: EdgeFanOut, From: "split", Targets: []string{"a", "b"}},
		&Edge{Name: "wait-a", Type: EdgeWaitEvent, From: "a", To: "a-done", Event: "ev-a", Correlation: constValuer{value: "k"}, Timeout: time.Hour},
		&Edge{Name: "wait-b", Type: EdgeWaitEvent, From: "b", To: "b-done", Event: "ev-b", Correlation: constValuer{value: "k"}, Timeout: time.Hour, OnTimeout: "b-late"})
	h := newEdgeHarnessFor(t, definition)

	run := h.start("e-wait-pair", nil)
	expectStatus(t, run, StatusWaiting)
	h.signal("ev-a", "k", nil)
	expectCount(t, h, "do.a-done", 1)
	expectStatus(t, h.get(run.ID), StatusWaiting)
	h.passTime(time.Hour)
	expectCount(t, h, "do.b-late", 1)
	expectStatus(t, h.get(run.ID), StatusCompleted)
}

func TestEdgeManualIsReleasedOnlyByAnOperatorPerGate(t *testing.T) {
	definition := def("e-manual", "split",
		[]*Step{step("split", "do.split"), step("p", "do.p"), step("q", "do.q"), terminal("p-go", "do.p-go"), terminal("q-go", "do.q-go")},
		&Edge{Name: "out", Type: EdgeFanOut, From: "split", Targets: []string{"p", "q"}},
		&Edge{Name: "gate-p", Type: EdgeManual, From: "p", To: "p-go"},
		&Edge{Name: "gate-q", Type: EdgeManual, From: "q", To: "q-go"})
	h := newEdgeHarnessFor(t, definition)
	h.runner.returns("do.q", map[string]any{"from": "q"})

	run := h.start("e-manual", nil)
	expectStatus(t, run, StatusWaiting)
	h.passTime(1000 * time.Hour)
	expectCount(t, h, "do.p-go", 0)
	expectCount(t, h, "do.q-go", 0)

	if _, err := boundedCall(t, "AdvanceManual", func() (struct{}, error) {
		return struct{}{}, h.engine.AdvanceManual(context.Background(), run.ID, "no-such-gate")
	}); err == nil {
		t.Fatal("releasing an unknown gate succeeded")
	}
	if _, err := boundedCall(t, "AdvanceManual", func() (struct{}, error) {
		return struct{}{}, h.engine.AdvanceManual(context.Background(), run.ID, "gate-q")
	}); err != nil {
		t.Fatalf("advance manual: %v", err)
	}
	expectCount(t, h, "do.q-go", 1)
	expectCount(t, h, "do.p-go", 0)
	if calls := h.runner.callsTo("do.q-go"); field(calls[0].Input, "from") != "q" {
		t.Fatalf("the gate did not carry its source's result: %v", calls[0].Input)
	}
	expectStatus(t, h.get(run.ID), StatusWaiting)

	if _, err := boundedCall(t, "AdvanceManual", func() (struct{}, error) {
		return struct{}{}, h.engine.AdvanceManual(context.Background(), run.ID, "gate-p")
	}); err != nil {
		t.Fatalf("advance manual: %v", err)
	}
	expectStatus(t, h.get(run.ID), StatusCompleted)
}

func TestEdgeEscalationAfterATimeoutNotifiesReassignsAndContinues(t *testing.T) {
	definition := def("e-escalate", "split",
		[]*Step{step("split", "do.split"), step("work", "do.work"), taskStep("approve", "clerks"),
			terminal("chase", "do.chase"), terminal("approved", "do.approved")},
		&Edge{Name: "out", Type: EdgeFanOut, From: "split", Targets: []string{"work", "approve"}},
		&Edge{Name: "escalate", Type: EdgeEscalation, From: "work", To: "chase", Timeout: time.Hour, Escalate: "managers", Notify: "ops"},
		edge("done", EdgeSimple, "approve", "approved"))
	h := newEdgeHarnessFor(t, definition)

	run := h.start("e-escalate", nil)
	expectStatus(t, run, StatusWaiting)
	h.passTime(59 * time.Minute)
	expectCount(t, h, "do.chase", 0)
	h.passTime(time.Minute)
	expectCount(t, h, "do.chase", 1)

	notices := h.noticesOf("escalation")
	if len(notices) == 0 || notices[0].Channel != "ops" {
		t.Fatalf("escalation notices = %+v", notices)
	}
	task := h.taskFor(run.ID, "approve")
	if task.Role != "managers" || task.Status != TaskEscalated {
		t.Fatalf("the outstanding task was not escalated: role=%s status=%s", task.Role, task.Status)
	}
	if err := h.completeTask(task, ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	expectStatus(t, h.get(run.ID), StatusCompleted)
}

func TestEdgeEscalationFromATaskStepFiresOnlyIfTheTaskIsStillOpen(t *testing.T) {
	definition := def("e-escalate-task", "start",
		[]*Step{step("start", "do.start"), taskStep("approve", "clerks"), terminal("chase", "do.chase"), terminal("approved", "do.approved")},
		edge("ask", EdgeSimple, "start", "approve"),
		&Edge{Name: "overdue", Type: EdgeEscalation, From: "approve", To: "chase", Timeout: time.Hour, Escalate: "managers"},
		edge("done", EdgeSimple, "approve", "approved"))
	h := newEdgeHarnessFor(t, definition)

	// Completed in time: no escalation, now or later.
	run := h.start("e-escalate-task", nil)
	if err := h.completeTask(h.taskFor(run.ID, "approve"), ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	expectStatus(t, h.get(run.ID), StatusCompleted)
	h.passTime(2 * time.Hour)
	expectCount(t, h, "do.chase", 0)

	// Overdue: the task moves to the escalation role and the chase step runs,
	// while the task itself stays open for its new owners.
	run = h.start("e-escalate-task", nil)
	h.passTime(time.Hour)
	expectCount(t, h, "do.chase", 1)
	task := h.taskFor(run.ID, "approve")
	if task.Role != "managers" {
		t.Fatalf("task role = %s, want managers", task.Role)
	}
	expectStatus(t, h.get(run.ID), StatusWaiting)
	if err := h.completeTask(task, ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	expectStatus(t, h.get(run.ID), StatusCompleted)
	expectCount(t, h, "do.approved", 2)
}

// ---------------------------------------------------------------------------
// Termination and shaping: cancel, transform, filter, stream_pipe
// ---------------------------------------------------------------------------

func TestEdgeCancelEndsTheRunAndClosesEverythingOutstanding(t *testing.T) {
	definition := def("e-cancel", "split",
		[]*Step{step("split", "do.split"), taskStep("approve", "clerks"), step("check", "do.check"),
			step("later", "do.later"), terminal("fine", "do.fine"), terminal("never", "do.never")},
		&Edge{Name: "out", Type: EdgeFanOut, From: "split", Targets: []string{"approve", "check"}},
		&Edge{Name: "abort", Type: EdgeCancel, From: "check", Guard: exprGuard{path: "result.abort", equal: true}},
		&Edge{Name: "continue", Type: EdgeDelayed, From: "check", To: "later", Timeout: time.Hour, Guard: exprGuard{path: "result.abort", equal: true, negate: true}},
		edge("fine", EdgeSimple, "later", "fine"),
		edge("approved", EdgeSimple, "approve", "never"))
	h := newEdgeHarnessFor(t, definition)
	h.runner.returns("do.check", map[string]any{"abort": true})

	run := h.start("e-cancel", nil)
	expectStatus(t, run, StatusCancelled)
	if open := h.openTasks(run.ID); len(open) != 0 {
		t.Fatalf("a cancelled run left %d tasks open", len(open))
	}
	if timers := h.timers(run.ID); len(timers) != 0 {
		t.Fatalf("a cancelled run left timers: %s", describeTimers(timers))
	}
	h.passTime(2 * time.Hour)
	expectCount(t, h, "do.later", 0)

	// Condition false: the run is not cancelled.
	h.runner.returns("do.check", map[string]any{"abort": false})
	run = h.start("e-cancel", nil)
	expectStatus(t, run, StatusWaiting)
}

func TestEdgeCancelInAChildProcessReleasesItsParent(t *testing.T) {
	child := def("e-cancel-child", "work",
		[]*Step{step("work", "do.child"), terminal("unreached", "do.unreached")},
		&Edge{Name: "stop", Type: EdgeCancel, From: "work", Guard: exprGuard{path: "result.stop", equal: true}},
		&Edge{Name: "go", Type: EdgeSimple, From: "work", To: "unreached", Guard: exprGuard{path: "result.stop", equal: true, negate: true}})
	parent := def("e-cancel-parent", "call",
		[]*Step{{Name: "call", Process: "e-cancel-child"}, terminal("ok", "do.ok"), terminal("handled", "do.handled")},
		edge("ok", EdgeSimple, "call", "ok"),
		edge("child-failed", EdgeError, "call", "handled"))
	h := newEdgeHarness(t, []*Definition{child, parent})
	h.runner.returns("do.child", map[string]any{"stop": true})

	run := h.start("e-cancel-parent", nil)
	final := h.get(run.ID)
	expectStatus(t, final, StatusCompleted)
	expectCount(t, h, "do.handled", 1)
	expectCount(t, h, "do.ok", 0)

	h.runner.returns("do.child", map[string]any{"stop": false})
	run = h.start("e-cancel-parent", nil)
	expectStatus(t, h.get(run.ID), StatusCompleted)
	expectCount(t, h, "do.ok", 1)
}

func TestEdgeTransformFilterAndStreamPipeShapeThePayload(t *testing.T) {
	double := shaperFunc(func(value any, _ Scope) (any, error) {
		return map[string]any{"doubled": field(value, "n").(float64) * 2}, nil
	})
	onlyBig := shaperFunc(func(value any, _ Scope) (any, error) {
		if field(value, "n").(float64) < 10 {
			return nil, ErrFiltered
		}
		return value, nil
	})
	onlySmall := shaperFunc(func(value any, _ Scope) (any, error) {
		if field(value, "n").(float64) >= 10 {
			return nil, ErrFiltered
		}
		return value, nil
	})
	definition := def("e-shape", "src",
		[]*Step{step("src", "do.src"), terminal("t", "do.t"), terminal("big", "do.big"), terminal("small", "do.small"), terminal("sink", "do.sink")},
		&Edge{Name: "transform", Type: EdgeTransform, From: "src", To: "t", Shaper: double},
		&Edge{Name: "big", Type: EdgeFilter, From: "src", To: "big", Shaper: onlyBig},
		&Edge{Name: "small", Type: EdgeFilter, From: "src", To: "small", Shaper: onlySmall},
		&Edge{Name: "pipe", Type: EdgeStreamPipe, From: "src", To: "sink"})
	h := newEdgeHarnessFor(t, definition)
	rows := make([]any, 500)
	for i := range rows {
		rows[i] = map[string]any{"row": float64(i)}
	}
	h.runner.on("do.src", func(call StepCall, _ int) (any, error) {
		return map[string]any{"n": field(call.Input, "n"), "rows": rows}, nil
	})

	expectStatus(t, h.start("e-shape", map[string]any{"n": 21}), StatusCompleted)
	if got := field(h.runner.callsTo("do.t")[0].Input, "doubled"); got != 42.0 {
		t.Fatalf("transform delivered %v", got)
	}
	expectCount(t, h, "do.big", 1)
	expectCount(t, h, "do.small", 0)
	piped, _ := field(h.runner.callsTo("do.sink")[0].Input, "rows").([]any)
	if len(piped) != 500 || field(piped[499], "row") != 499.0 {
		t.Fatalf("stream_pipe did not deliver the payload intact: %d rows", len(piped))
	}

	expectStatus(t, h.start("e-shape", map[string]any{"n": 3}), StatusCompleted)
	expectCount(t, h, "do.big", 1)
	expectCount(t, h, "do.small", 1)
}

func TestEdgeFilterThatRejectsEverythingIsADeadEnd(t *testing.T) {
	definition := def("e-filter-dead", "src",
		[]*Step{step("src", "do.src"), terminal("dst", "do.dst")},
		&Edge{Name: "only", Type: EdgeFilter, From: "src", To: "dst",
			Shaper: shaperFunc(func(any, Scope) (any, error) { return nil, ErrFiltered })})
	h := newEdgeHarnessFor(t, definition)
	expectStatus(t, h.start("e-filter-dead", nil), StatusFailed)
	expectCount(t, h, "do.dst", 0)
}

// ---------------------------------------------------------------------------
// Cross-cutting contracts
// ---------------------------------------------------------------------------

// TestEveryEdgeTypeHonoursItsCondition checks the one contract every edge type
// shares: when its condition says no, the edge does nothing at all — no frames,
// no parking, no timers, no cancellation, no compensation.
func TestEveryEdgeTypeHonoursItsCondition(t *testing.T) {
	for name := range edgeTypeNames {
		kind := name
		t.Run(string(kind), func(t *testing.T) {
			// loop_until's condition is its exit condition: "no traversal" is the
			// condition holding.
			guard := never
			if kind == EdgeLoopUntil {
				guard = always
			}
			steps := []*Step{
				{Name: "src", Intent: "do.src", Compensate: "undo.src"},
				terminal("t1", "do.t1"), terminal("t2", "do.t2"), terminal("escape", "do.escape"),
			}
			tested := &Edge{Name: "tested", Type: kind, From: "src", To: "t1", Guard: guard}
			var extra []*Edge
			switch kind {
			case EdgeFanIn, EdgeJoin, EdgeQuorum:
				steps = append(steps, terminal("other", "do.other"))
				tested = &Edge{Name: "tested", Type: kind, Sources: []string{"src", "other"}, To: "t1", Guard: guard, Strategy: "any"}
				extra = append(extra, edge("src-other", EdgeSimple, "src", "other"))
			case EdgeParallel, EdgeFanOut, EdgeRace, EdgeConditionalFork:
				tested.To, tested.Targets = "", []string{"t1", "t2"}
			case EdgeDynamicFanOut:
				tested.To, tested.Targets, tested.TargetsPath = "", []string{"t1", "t2"}, "targets"
			case EdgeThreshold:
				zero := 0.0
				tested.To = ""
				tested.Thresholds = []Threshold{{Name: "all", Min: &zero, Value: constValuer{value: 1}, Target: "t1"}}
			case EdgeWaitEvent:
				tested.Event, tested.Correlation = "ev", constValuer{value: "k"}
			case EdgeDelayed, EdgeTimeout, EdgeEscalation:
				tested.Timeout = time.Minute
			case EdgeRateLimited:
				tested.RateLimit, tested.Limit, tested.Window = "rl", 1, time.Minute
			case EdgeIterator, EdgeBatchIterator:
				tested.ItemsPath = "items"
			case EdgeCancel:
				tested.To = ""
			}
			escapeKind := EdgeSimple
			if kind.ErrorPath() {
				escapeKind = EdgeError
				// Every step needs a success path to compile; src fails, so it is
				// never taken.
				extra = append(extra, &Edge{Name: "success", Type: EdgeSimple, From: "src", To: "t2"})
			}
			edges := append([]*Edge{tested, {Name: "escape", Type: escapeKind, From: "src", To: "escape", Guard: always}}, extra...)
			// Whatever the tested edge cannot reach is reached (never, in practice)
			// from the escape step, so the definition compiles for every shape.
			reached := map[string]bool{}
			for _, target := range tested.allTargets() {
				reached[target] = true
			}
			for _, band := range tested.Thresholds {
				reached[band.Target] = true
			}
			for _, e := range extra {
				reached[e.To] = true
			}
			for _, target := range []string{"t1", "t2"} {
				if !reached[target] {
					edges = append(edges, &Edge{Name: "reach-" + target, Type: EdgeSimple, From: "escape", To: target, Guard: never})
				}
			}
			definition := def("guard-"+string(kind), "src", steps, edges...)
			h := newEdgeHarnessFor(t, definition)
			h.runner.returns("do.src", map[string]any{"items": []any{1, 2}, "targets": []any{"t1", "t2"}})
			if kind.ErrorPath() {
				h.runner.fails("do.src", "boom")
			}

			run := h.start(definition.Name, nil)
			if run.Status == StatusCancelled {
				t.Fatal("an edge with a false condition cancelled the run")
			}
			h.passTime(time.Hour)
			run = h.get(run.ID)
			expectStatus(t, run, StatusCompleted)
			if got := h.runner.count("do.t1") + h.runner.count("do.t2"); got != 0 {
				t.Fatalf("an edge with a false condition traversed: %v", h.runner.intents())
			}
			expectCount(t, h, "undo.src", 0)
			expectCount(t, h, "do.escape", 1)
			if timers := h.timers(run.ID); len(timers) != 0 {
				t.Fatalf("timers left: %s", describeTimers(timers))
			}
		})
	}
}

// TestEveryEdgeTypeHasBehaviouralCoverage keeps this file honest: adding an edge
// type without a behavioural test here fails the build's tests.
func TestEveryEdgeTypeHasBehaviouralCoverage(t *testing.T) {
	covered := map[EdgeType]string{
		EdgeSimple:          "TestEdgeSimplePassesTheResultAsTheNextInput",
		EdgeBranch:          "TestEdgeBranchTakesOnlyTheBranchWhoseConditionHolds",
		EdgeSwitch:          "TestEdgeSwitchTakesTheFirstMatchingCaseOrTheDefault",
		EdgeConditionalFork: "TestEdgeConditionalForkFiresEveryTargetOfEveryMatchingFork",
		EdgeThreshold:       "TestEdgeThresholdRoutesByBandAndCarriesTheBand",
		EdgeWeighted:        "TestEdgeWeightedChoosesExactlyOneEligibleSiblingByWeight",
		EdgePriority:        "TestEdgePriorityTakesTheLowestEligiblePriority",
		EdgeFanOut:          "TestEdgeFanOutRunsEveryTargetConcurrently",
		EdgeDynamicFanOut:   "TestEdgeDynamicFanOutDispatchesToRuntimeTargets",
		EdgeFanIn:           "TestEdgeFanInAllWaitsForEverySourceAndFiresOnce",
		EdgeJoin:            "TestEdgeJoinPartialSuccessCarriesFailuresForward",
		EdgeQuorum:          "TestEdgeQuorumDefaultsToAMajorityAndFailsWhenUnreachable",
		EdgeParallel:        "TestEdgeParallelFailFastDropsSiblingsThatHaveNotStarted",
		EdgeRace:            "TestEdgeRaceTheFirstToFinishWinsAndOnlyItContinues",
		EdgeIterator:        "TestEdgeIteratorRunsOncePerItemWithItsOwnStateKey",
		EdgeBatchIterator:   "TestEdgeBatchIteratorRunsOncePerBatch",
		EdgeLoopUntil:       "TestEdgeLoopUntilRepeatsUntilTheConditionHoldsThenExitsOnce",
		EdgeRetry:           "TestEdgeRetryRetriesTheTargetWithBackoff",
		EdgeTimeout:         "TestEdgeTimeoutTakesOnTimeoutWhenAParkedTargetOverruns",
		EdgeRateLimited:     "TestEdgeRateLimitedParksUntilTheLimiterAdmits",
		EdgeError:           "TestEdgeErrorAndFallbackFireOnlyOnFailure",
		EdgeFallback:        "TestEdgeErrorAndFallbackFireOnlyOnFailure",
		EdgeCompensate:      "TestEdgeCompensateRollsBackCommittedStepsInReverse",
		EdgeDelayed:         "TestEdgeDelayedParksForItsDurationAndCarriesThePayload",
		EdgeWaitEvent:       "TestEdgeWaitEventCorrelatesDeliversAndTimesOut",
		EdgeManual:          "TestEdgeManualIsReleasedOnlyByAnOperatorPerGate",
		EdgeEscalation:      "TestEdgeEscalationAfterATimeoutNotifiesReassignsAndContinues",
		EdgeCancel:          "TestEdgeCancelEndsTheRunAndClosesEverythingOutstanding",
		EdgeTransform:       "TestEdgeTransformFilterAndStreamPipeShapeThePayload",
		EdgeFilter:          "TestEdgeTransformFilterAndStreamPipeShapeThePayload",
		EdgeStreamPipe:      "TestEdgeTransformFilterAndStreamPipeShapeThePayload",
	}
	for kind := range edgeTypeNames {
		if covered[kind] == "" {
			t.Errorf("edge type %q has no behavioural test", kind)
		}
	}
}

// ---------------------------------------------------------------------------
// Deadlock and concurrency hunting
// ---------------------------------------------------------------------------

// TestConcurrentSignalsTicksAndAdvancesConverge hammers many parked runs from
// several goroutines at once — signals, timer ticks, operator releases and
// direct advances all racing on the same runs and the same store. Every run must
// reach a terminal state and every call must return.
func TestConcurrentSignalsTicksAndAdvancesConverge(t *testing.T) {
	definition := def("e-storm", "split",
		[]*Step{step("split", "do.split"), step("a", "do.a"), step("b", "do.b"), step("c", "do.c"),
			step("a2", "do.a2"), step("b2", "do.b2"), step("c2", "do.c2"), terminal("finish", "do.finish")},
		&Edge{Name: "out", Type: EdgeFanOut, From: "split", Targets: []string{"a", "b", "c"}},
		&Edge{Name: "wait", Type: EdgeWaitEvent, From: "a", To: "a2", Event: "go", Correlation: pathValuer{path: "run.id"}},
		&Edge{Name: "gate", Type: EdgeManual, From: "b", To: "b2"},
		&Edge{Name: "pause", Type: EdgeDelayed, From: "c", To: "c2", Timeout: time.Minute},
		&Edge{Name: "in", Type: EdgeFanIn, Sources: []string{"a2", "b2", "c2"}, To: "finish"})
	h := newEdgeHarnessFor(t, definition)

	const runs = 20
	ids := make([]string, 0, runs)
	for range runs {
		ids = append(ids, h.start("e-storm", nil).ID)
	}
	h.clock.Add(time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), edgeTestBound)
	defer cancel()
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var errs []error
	record := func(err error) {
		if err != nil && !strings.Contains(err.Error(), "being advanced") && !strings.Contains(err.Error(), "stayed busy") &&
			!strings.Contains(err.Error(), "no operator gate") && !strings.Contains(err.Error(), "no longer available") {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
		}
	}
	for _, id := range ids {
		wg.Add(3)
		go func() {
			defer wg.Done()
			for !h.get(id).Status.Terminal() && ctx.Err() == nil {
				_, err := h.engine.Signal(ctx, "go", id, nil)
				record(err)
				time.Sleep(time.Millisecond)
			}
		}()
		go func() {
			defer wg.Done()
			for !h.get(id).Status.Terminal() && ctx.Err() == nil {
				record(h.engine.AdvanceManual(ctx, id, "gate"))
				record(h.engine.Advance(ctx, id))
				time.Sleep(time.Millisecond)
			}
		}()
		go func() {
			defer wg.Done()
			for !h.get(id).Status.Terminal() && ctx.Err() == nil {
				// A timer whose handler lost the race for the lease is released and
				// retried a second later — on the engine's clock, so the clock moves.
				h.clock.Add(time.Second)
				_, err := h.engine.Tick(ctx, 10)
				record(err)
				time.Sleep(time.Millisecond)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * edgeTestBound):
		t.Fatal("concurrent drivers did not finish: probable deadlock")
	}
	for _, id := range ids {
		run := h.get(id)
		if run.Status != StatusCompleted {
			t.Errorf("run %s ended %s (%s), waiting=%+v", id, run.Status, run.Error, run.Waiting)
		}
	}
	if got := h.runner.count("do.finish"); got != runs {
		t.Errorf("finish ran %d times across %d runs — a join fired twice or never", got, runs)
	}
	for _, err := range errs {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("driver error: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Parks and faults the edge types rely on
// ---------------------------------------------------------------------------

// refusingLocker refuses a lock until opened.
type refusingLocker struct {
	mu   sync.Mutex
	open bool
}

func (l *refusingLocker) Acquire(context.Context, string, time.Duration) (string, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return "token", l.open, nil
}

func (l *refusingLocker) Release(context.Context, string, string) error { return nil }

func TestStepRateLimitAndLockParksKeepTheirFrame(t *testing.T) {
	locker := &refusingLocker{}
	definition := def("e-step-parks", "start",
		[]*Step{step("start", "do.start"),
			{Name: "limited", Intent: "do.limited", RateLimit: "api", Limit: 1, Window: time.Minute},
			{Name: "locked", Intent: "do.locked", Lock: "l", LockKey: constValuer{value: "k"}, LockWait: time.Minute},
			terminal("done", "do.done")},
		edge("a", EdgeSimple, "start", "limited"),
		edge("b", EdgeSimple, "limited", "locked"),
		edge("c", EdgeSimple, "locked", "done"))
	h := newEdgeHarnessFor(t, definition, func(o *Options) { o.Locker = locker })
	h.limiter.set(false)

	// Refused by its rate limit: the step must come back, not vanish and let the
	// run "complete" without it.
	run := h.start("e-step-parks", nil)
	expectStatus(t, run, StatusWaiting)
	expectCount(t, h, "do.limited", 0)
	h.limiter.set(true)
	h.passTime(time.Minute)
	expectCount(t, h, "do.limited", 1)

	// Then refused by its lock: same.
	expectStatus(t, h.get(run.ID), StatusWaiting)
	expectCount(t, h, "do.locked", 0)
	locker.mu.Lock()
	locker.open = true
	locker.mu.Unlock()
	h.passTime(time.Minute)
	expectCount(t, h, "do.locked", 1)
	expectStatus(t, h.get(run.ID), StatusCompleted)
}

func TestAParkedChildProcessResumesItsParentWhenItFinishes(t *testing.T) {
	child := def("e-child", "ask",
		[]*Step{step("ask", "do.child-ask"), terminal("answer", "do.child-answer")},
		&Edge{Name: "await", Type: EdgeWaitEvent, From: "ask", To: "answer", Event: "reply", Correlation: constValuer{value: "c-1"}})
	parent := def("e-parent", "call",
		[]*Step{{Name: "call", Process: "e-child"}, step("after", "do.after"), terminal("done", "do.done")},
		edge("ok", EdgeSimple, "call", "after"), edge("fin", EdgeSimple, "after", "done"))
	h := newEdgeHarness(t, []*Definition{child, parent})
	h.runner.returns("do.child-answer", map[string]any{"answer": "yes"})

	run := h.start("e-parent", nil)
	expectStatus(t, run, StatusWaiting)
	if woken := h.signal("reply", "c-1", nil); len(woken) != 1 {
		t.Fatalf("woken = %v", woken)
	}
	expectStatus(t, h.get(run.ID), StatusCompleted)
	calls := h.runner.callsTo("do.after")
	if len(calls) != 1 || field(calls[0].Input, "output.answer") != "yes" || field(calls[0].Input, "status") != "completed" {
		t.Fatalf("the parent did not continue with the child's result: %v", calls)
	}
}

func TestATimeoutEdgeOverAChildProcessCancelsTheChild(t *testing.T) {
	child := def("e-slow-child", "ask",
		[]*Step{step("ask", "do.child-ask"), terminal("answer", "do.child-answer")},
		&Edge{Name: "await", Type: EdgeWaitEvent, From: "ask", To: "answer", Event: "reply", Correlation: constValuer{value: "c-2"}})
	parent := def("e-bounded-parent", "start",
		[]*Step{step("start", "do.start"), {Name: "call", Process: "e-slow-child"}, terminal("done", "do.done"), terminal("late", "do.late")},
		&Edge{Name: "bounded", Type: EdgeTimeout, From: "start", To: "call", Timeout: time.Minute, OnTimeout: "late"},
		edge("ok", EdgeSimple, "call", "done"))
	h := newEdgeHarness(t, []*Definition{child, parent})

	run := h.start("e-bounded-parent", nil)
	expectStatus(t, run, StatusWaiting)
	h.passTime(time.Minute)
	expectStatus(t, h.get(run.ID), StatusCompleted)
	expectCount(t, h, "do.late", 1)
	children, err := h.store.ListRuns(context.Background(), RunFilter{Process: "e-slow-child"})
	if err != nil || len(children) != 1 {
		t.Fatalf("children = %v, %v", children, err)
	}
	expectStatus(t, children[0], StatusCancelled)
	// The abandoned child's event arriving later wakes nothing.
	if woken := h.signal("reply", "c-2", nil); len(woken) != 0 {
		t.Fatalf("a cancelled child was woken: %v", woken)
	}
	expectCount(t, h, "do.done", 0)
}

func TestAConditionThatCannotBeEvaluatedFailsTheRunInsteadOfWedgingIt(t *testing.T) {
	broken := guardErr{}
	definition := def("e-bad-guard", "a",
		[]*Step{step("a", "do.a"), terminal("b", "do.b")},
		&Edge{Name: "broken", Type: EdgeBranch, From: "a", To: "b", Guard: broken})
	h := newEdgeHarnessFor(t, definition)

	run := h.start("e-bad-guard", nil)
	expectStatus(t, run, StatusFailed)
	if !strings.Contains(run.Error, "broken") {
		t.Fatalf("error = %q", run.Error)
	}
	// Before, the advance returned the error without saving: the run stayed
	// "running" and every later advance re-executed step a.
	h.advance(run.ID)
	h.advance(run.ID)
	expectCount(t, h, "do.a", 1)
}

type guardErr struct{}

func (guardErr) Eval(Scope) (bool, error) { return false, errors.New("unknown identifier") }
func (guardErr) Source() string           { return "nonsense ==" }
