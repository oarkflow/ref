package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRunScenarios runs the example end to end and checks the claims it
// prints: DataLoader batching, ProcessCache hits and Coalescer deduplication.
func TestRunScenarios(t *testing.T) {
	var out bytes.Buffer
	counts, err := run(&out)
	if err != nil {
		t.Fatal(err)
	}
	// Scenario 1: one settings query; the five user loads are batched.
	// Scenario 2: settings come from the ProcessCache; one users query.
	// Scenario 3: 100 concurrent requests coalesce into one settings query.
	// The users loader batches within a 5ms window, so a loaded scheduler
	// may split a batch; only "fewer queries than loads" is guaranteed.
	if counts.SettingsQueries != [3]int32{1, 0, 1} {
		t.Fatalf("settings queries per scenario = %v, want [1 0 1]\n%s", counts.SettingsQueries, out.String())
	}
	users := [3]int32{}
	for i := range users {
		users[i] = counts.Queries[i] - counts.SettingsQueries[i]
	}
	if users[0] < 1 || users[0] >= 5 || users[1] != 1 || users[2] < 1 || users[2] >= 100 {
		t.Fatalf("users queries per scenario = %v, want batching (1..4, 1, 1..99)\n%s", users, out.String())
	}
	for _, want := range []string{`"theme": "dark"`, `"name": "Alice"`, `"name": "Eve"`, `"name": "Grace"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %s:\n%s", want, out.String())
		}
	}
}
