package money

import (
	"fmt"
	"math/big"
	"strings"
)

// RoundingMode names how an inexact result becomes a whole number of minor
// units.
type RoundingMode string

const (
	// HalfEven rounds to the nearest, ties to the even neighbour (banker's
	// rounding). It is the default: over many operations it does not drift.
	HalfEven RoundingMode = "half_even"
	// HalfUp rounds to the nearest, ties away from zero (school rounding).
	HalfUp RoundingMode = "half_up"
	// HalfDown rounds to the nearest, ties toward zero.
	HalfDown RoundingMode = "half_down"
	// Down truncates toward zero.
	Down RoundingMode = "down"
	// Up rounds away from zero.
	Up RoundingMode = "up"
	// Floor rounds toward negative infinity.
	Floor RoundingMode = "floor"
	// Ceiling rounds toward positive infinity.
	Ceiling RoundingMode = "ceiling"
)

// ParseRoundingMode reads a mode name; empty is HalfEven. "bankers" and
// "truncate" are accepted as aliases.
func ParseRoundingMode(s string) (RoundingMode, error) {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(s, "-", "_"))) {
	case "", "half_even", "bankers":
		return HalfEven, nil
	case "half_up":
		return HalfUp, nil
	case "half_down":
		return HalfDown, nil
	case "down", "truncate":
		return Down, nil
	case "up":
		return Up, nil
	case "floor":
		return Floor, nil
	case "ceiling", "ceil":
		return Ceiling, nil
	}
	return "", fmt.Errorf("money: unknown rounding mode %q (half_even, half_up, half_down, down, up, floor, ceiling)", s)
}

// Round rounds an exact rational to an integer, reporting whether it was
// already an integer.
func Round(r *big.Rat, mode RoundingMode) (*big.Int, bool, error) {
	num, den := r.Num(), r.Denom()
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int)) // truncated toward zero
	if rem.Sign() == 0 {
		return q, true, nil
	}
	negative := r.Sign() < 0
	// Compare 2|rem| with den to know whether the discarded part is below,
	// at or above one half.
	twice := new(big.Int).Abs(rem)
	twice.Lsh(twice, 1)
	half := twice.Cmp(den)

	away := false
	switch mode {
	case "", HalfEven:
		away = half > 0 || (half == 0 && q.Bit(0) == 1)
	case HalfUp:
		away = half >= 0
	case HalfDown:
		away = half > 0
	case Down:
		away = false
	case Up:
		away = true
	case Floor:
		away = negative
	case Ceiling:
		away = !negative
	default:
		return nil, false, fmt.Errorf("money: unknown rounding mode %q", mode)
	}
	if away {
		if negative {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return q, false, nil
}
