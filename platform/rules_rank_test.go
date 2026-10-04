package platform

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/oarkflow/bcl"
)

const rankingSource = `
bcl { version "1.0" }
decision_schema "r" { effects [allow] default allow strategy first_match }
ranking "r" {
  selection best
  priority_path "provider.priority"
  score "rate" { metric "provider.rate" weight 400 normalize [0.8, 1] }
  score "quality" { metric "provider.quality" weight 100 normalize [0, 100] }
  score "cost" { metric "provider.cost" weight -50 normalize [0, 0.2] }
  score "raw" { metric "provider.bonus" weight 3 }
}
`

func compileRanking(t testing.TB, source string) *bcl.DecisionProgram {
	t.Helper()
	path := filepath.Join(t.TempDir(), "r.bcl")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	program, err := bcl.CompileDecisionFile(path, &bcl.Options{AllowTime: true})
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func candidate(id string, facts map[string]any) any { return map[string]any{"id": id, "facts": facts} }

// A score block with bounds is worth weight * clamp((x-min)/(max-min), 0, 1),
// whatever the metric's unit; one without bounds is weight * x.
func TestRankingNormalizesScores(t *testing.T) {
	program := compileRanking(t, rankingSource)
	score := func(facts map[string]any) float64 {
		res, err := evaluateDecision(program, "r", map[string]any{"candidates": []any{candidate("x", facts)}}, false)
		if err != nil || res.Rank == nil {
			t.Fatalf("rank: %v %v", res, err)
		}
		return res.Rank.Score
	}
	cases := []struct {
		name  string
		facts map[string]any
		want  float64
	}{
		{"all at best", map[string]any{"priority": 1000, "rate": 1.0, "quality": 100, "cost": 0.0, "bonus": 2}, 1000 + 400 + 100 + 0 + 6},
		{"all at worst", map[string]any{"priority": 0, "rate": 0.8, "quality": 0, "cost": 0.2, "bonus": 0}, -50},
		{"middle", map[string]any{"priority": 0, "rate": 0.9, "quality": 50, "cost": 0.1, "bonus": 0}, 200 + 50 - 25},
		{"below the lower bound clamps", map[string]any{"rate": 0.1, "quality": -40, "cost": 0.0}, 0},
		{"above the upper bound clamps", map[string]any{"rate": 7, "quality": 900, "cost": 5}, 400 + 100 - 50},
		{"a missing metric adds nothing", map[string]any{"priority": 5}, 5},
	}
	for _, c := range cases {
		if got := score(c.facts); got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("%s: score %v, want %v", c.name, got, c.want)
		}
	}
}

// The chain is the stable order by score. Scoring each candidate once must give
// exactly the order of choosing the best of the remaining candidates again and
// again, including for equal scores, which keep their arrival order.
func TestRankingOrderMatchesRepeatedSelection(t *testing.T) {
	program := compileRanking(t, rankingSource)
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 300; round++ {
		n := 1 + rng.Intn(9)
		type item struct {
			id    string
			facts map[string]any
		}
		items := make([]item, n)
		for i := range items {
			// Few distinct values, so equal scores are common.
			items[i] = item{fmt.Sprintf("p%d", i), map[string]any{
				"priority": []int{0, 10000, 50000}[rng.Intn(3)], "rate": []float64{0.8, 0.9, 1}[rng.Intn(3)],
				"quality": []int{40, 80}[rng.Intn(2)], "cost": []float64{0.01, 0.05}[rng.Intn(2)], "bonus": rng.Intn(2),
			}}
		}
		// Reference: select the first best of what is left, n times.
		var want []string
		rest := slices.Clone(items)
		for len(rest) > 0 {
			cs := make([]any, len(rest))
			for i, it := range rest {
				cs[i] = candidate(it.id, it.facts)
			}
			res, err := evaluateDecision(program, "r", map[string]any{"candidates": cs}, false)
			if err != nil || res.Rank == nil {
				t.Fatalf("reference: %v %v", res, err)
			}
			want = append(want, res.Rank.ID)
			rest = slices.DeleteFunc(rest, func(it item) bool { return it.id == res.Rank.ID })
		}
		// Under test: score each alone, stable sort by score.
		type scored struct {
			id    string
			score float64
		}
		var got []scored
		for _, it := range items {
			res, err := evaluateDecision(program, "r", map[string]any{"candidates": []any{candidate(it.id, it.facts)}}, false)
			if err != nil || res.Rank == nil {
				t.Fatalf("single: %v %v", res, err)
			}
			got = append(got, scored{it.id, res.Rank.Score})
		}
		slices.SortStableFunc(got, func(a, b scored) int {
			switch {
			case a.score > b.score:
				return -1
			case a.score < b.score:
				return 1
			}
			return 0
		})
		ids := make([]string, len(got))
		for i, g := range got {
			ids[i] = g.id
		}
		if !slices.Equal(ids, want) {
			t.Fatalf("round %d: scoring each once gives %v, repeated selection gives %v (%v)", round, ids, want, items)
		}
	}
}

const rankApp = `
name "rank"

resource "policy" {
  kind "rules.engine"
  config {
    definition "route" {
      version "1"
      source <<BCL
        bcl { version "1.0" }
        decision_schema "ok" { effects [allow, deny] default deny strategy first_match }
        decision_table "ok" {
          default deny
          hit_policy first
          row "paused" { priority 10 when { provider.state != "active" } then { outcome { decision deny reason "paused" attributes { code "PAUSED" } } } }
          row "exclusive" { priority 9 when { provider.id == "solo" } then { outcome { decision allow attributes { priority 90000 exclusive true } } } }
          row "any" { priority 1 when { provider.id != "" } then { outcome { decision allow attributes { priority 1000 } } } }
        }
        decision_schema "best" { effects [allow] default allow strategy first_match }
        ranking "best" {
          selection best
          priority_path "provider.priority"
          score "quality" { metric "provider.quality" weight 100 normalize [0, 100] }
        }
BCL
    }
  }
}

intent "pick" {
  response "route"
  node "candidates" {
    uses "expression" kind pure requires [input] provides [candidates]
    config { expression "input.candidates" }
  }
  node "route" {
    uses "rules.rank" resource "policy" kind read requires [candidates] provides [route]
    config { definition "route" candidates_fact "candidates" eligibility "ok" ranking "best" limit 3 }
  }
}
route "pick" { method POST path "/pick" intent "pick" }
`

func chainOf(body any) []string {
	var ids []string
	if list, ok := dig(body, "chain").([]any); ok {
		for _, c := range list {
			ids = append(ids, c.(map[string]any)["id"].(string))
		}
	}
	return ids
}

func TestRulesRankAction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(rankApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, nil)
	pick := func(cands ...map[string]any) (int, any) {
		list := make([]any, len(cands))
		for i, c := range cands {
			list[i] = c
		}
		return h.call("POST", "/pick", "", map[string]any{"candidates": list})
	}
	row := func(id, state string, quality int) map[string]any {
		return map[string]any{"id": id, "state": state, "quality": quality}
	}
	// Best quality first; equal quality keeps arrival order; a paused one is rejected with its reason; limit cuts the chain.
	status, body := pick(row("a", "active", 50), row("b", "active", 90), row("c", "paused", 99), row("d", "active", 50), row("e", "active", 10), row("f", "active", 20))
	if status != 200 {
		t.Fatalf("pick: %d %v", status, body)
	}
	if got := chainOf(body); !slices.Equal(got, []string{"b", "a", "d"}) {
		t.Fatalf("chain %v body %v", got, body)
	}
	rejected, _ := dig(body, "rejected").([]any)
	if len(rejected) != 1 || !strings.Contains(rejected[0].(map[string]any)["reason"].(string), "paused") {
		t.Fatalf("rejected %v", rejected)
	}
	// An exclusive candidate drops everything of a lower priority.
	_, body = pick(row("a", "active", 99), row("solo", "active", 1), row("b", "active", 80))
	if got := chainOf(body); !slices.Equal(got, []string{"solo"}) {
		t.Fatalf("exclusive chain %v", got)
	}
	// Nothing eligible: an empty chain, flagged.
	_, body = pick(row("c", "paused", 99))
	if dig(body, "empty") != true || len(chainOf(body)) != 0 {
		t.Fatalf("empty %v", body)
	}
}
