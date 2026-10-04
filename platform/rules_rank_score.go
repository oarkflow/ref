package platform

import (
	"sync"

	"github.com/oarkflow/bcl"
)

// Ranking scores with normalisation.
//
// A ranking's score is the sum of weight * metric over its score blocks, plus
// priority_path. The rules library reads a score's `normalize` bound but does not
// apply it, so a weight would multiply a raw metric (quality 0-100 against a
// delivery rate 0-1) and the weights could not be reasoned about. Here a score
// block that carries bounds
//
//	score "cost" { metric "provider.cost" weight -500 normalize [0, 0.2] }
//
// contributes weight * clamp((metric - min) / (max - min), 0, 1): the weight is
// what the metric is worth at its best, whatever its unit. A score block without
// bounds, priority_path and cost_path behave as the library defines them.

var optionCache sync.Map // *bcl.DecisionProgram -> *bcl.Options

// optionsFor returns the evaluation options of a program: the ranking scorers
// for the rankings that normalise, built once per compiled program.
func optionsFor(program *bcl.DecisionProgram) *bcl.Options {
	if cached, ok := optionCache.Load(program); ok {
		return cached.(*bcl.Options)
	}
	opts := &bcl.Options{AllowTime: true}
	for id, ranking := range program.Rankings {
		if ranking == nil || !rankingNormalizes(ranking) {
			continue
		}
		if opts.DecisionRankers == nil {
			opts.DecisionRankers = map[string]bcl.DecisionRankingScorer{}
		}
		opts.DecisionRankers[id] = normalizedScorer
	}
	actual, _ := optionCache.LoadOrStore(program, opts)
	return actual.(*bcl.Options)
}

func rankingNormalizes(r *bcl.RankingDefinition) bool {
	for _, s := range r.Scores {
		if _, _, ok := bounds(s); ok {
			return true
		}
	}
	return false
}

// bounds reads `normalize [min, max]`.
func bounds(s bcl.RankingScore) (lo, hi float64, ok bool) {
	if len(s.Normalize) == 0 {
		return 0, 0, false
	}
	pair, isList := s.Normalize[0].([]any)
	if !isList || len(pair) != 2 {
		return 0, 0, false
	}
	lo, okLo := numberOf(pair[0])
	hi, okHi := numberOf(pair[1])
	return lo, hi, okLo && okHi && hi > lo
}

func normalizedScorer(candidate bcl.DecisionCandidate, ranking *bcl.RankingDefinition, _ map[string]any) (float64, bool, error) {
	facts := candidate.Facts
	if p, ok := facts["provider"].(map[string]any); ok {
		facts = p
	}
	env := map[string]any{"provider": facts}
	score := 0.0
	if ranking.PriorityPath != "" {
		if v, ok := resolvePath(env, ranking.PriorityPath); ok {
			if n, ok := numberOf(v); ok {
				score += n
			}
		}
	}
	if ranking.CostPath != "" {
		if v, ok := resolvePath(env, ranking.CostPath); ok {
			if n, ok := numberOf(v); ok {
				score -= n
			}
		}
	}
	for _, s := range ranking.Scores {
		v, ok := resolvePath(env, s.Metric)
		if !ok {
			continue
		}
		n, ok := numberOf(v)
		if !ok {
			continue
		}
		if lo, hi, bounded := bounds(s); bounded {
			n = (n - lo) / (hi - lo)
			if n < 0 {
				n = 0
			} else if n > 1 {
				n = 1
			}
		}
		score += n * s.Weight
	}
	return score, true, nil
}
