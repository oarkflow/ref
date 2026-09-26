package money

import (
	"fmt"
	"math/big"
	"sort"
)

// Allocate splits an amount by ratios so the parts always sum exactly to the
// whole. Each part first gets the floor of its exact share; the minor units
// left over go one each to the parts with the largest fractional remainders
// (the largest-remainder, or Hamilton, method), ties to the earlier part.
//
// Ratios may be any non-negative decimals (weights 1:2:3, percentages 50,
// 30, 20, or fractions 0.5, 0.25, 0.25); they need not sum to anything in
// particular, but at least one must be positive. A negative amount is split
// as its absolute value and every part negated, so a refund mirrors the
// charge it reverses.
func (m Money) Allocate(ratios ...*big.Rat) ([]Money, error) {
	return m.AllocateUnit(1, ratios...)
}

// AllocateInts is Allocate with whole-number ratios.
func (m Money) AllocateInts(ratios ...int64) ([]Money, error) {
	rats := make([]*big.Rat, len(ratios))
	for i, r := range ratios {
		rats[i] = new(big.Rat).SetInt64(r)
	}
	return m.Allocate(rats...)
}

// Split divides an amount into n parts as equal as possible: the parts differ
// by at most one minor unit and the earlier parts carry the extra units.
func (m Money) Split(n int) ([]Money, error) {
	if n < 1 {
		return nil, fmt.Errorf("money: split needs at least one part")
	}
	ratios := make([]*big.Rat, n)
	for i := range ratios {
		ratios[i] = big.NewRat(1, 1)
	}
	return m.Allocate(ratios...)
}

// AllocateUnit allocates in multiples of a minimum unit, given in minor units
// — 5 to split in 0.05 steps, 100 to split NPR in whole rupees. The amount is
// divided into whole units by the largest-remainder method; whatever is
// smaller than one unit (the amount's own remainder modulo the unit) goes to
// the part with the largest ratio, ties to the earlier part, so the parts
// still sum exactly to the whole.
func (m Money) AllocateUnit(unit int64, ratios ...*big.Rat) ([]Money, error) {
	if unit < 1 {
		return nil, fmt.Errorf("money: allocation unit must be at least one minor unit")
	}
	if len(ratios) == 0 {
		return nil, fmt.Errorf("money: allocation needs at least one ratio")
	}
	total := new(big.Rat)
	largest := 0
	for i, r := range ratios {
		if r == nil || r.Sign() < 0 {
			return nil, fmt.Errorf("money: allocation ratios must be non-negative")
		}
		total.Add(total, r)
		if r.Cmp(ratios[largest]) > 0 {
			largest = i
		}
	}
	if total.Sign() == 0 {
		return nil, fmt.Errorf("money: at least one allocation ratio must be positive")
	}

	amount := new(big.Int).Abs(big.NewInt(m.Minor))
	units, leftover := new(big.Int).QuoRem(amount, big.NewInt(unit), new(big.Int))

	type share struct {
		index int
		base  *big.Int
		frac  *big.Rat
	}
	shares := make([]share, len(ratios))
	assigned := new(big.Int)
	for i, r := range ratios {
		exact := new(big.Rat).Mul(new(big.Rat).SetInt(units), r)
		exact.Quo(exact, total)
		base := new(big.Int).Quo(exact.Num(), exact.Denom()) // non-negative: floor
		frac := new(big.Rat).Sub(exact, new(big.Rat).SetInt(base))
		shares[i] = share{index: i, base: base, frac: frac}
		assigned.Add(assigned, base)
	}
	missing := new(big.Int).Sub(units, assigned).Int64()
	order := make([]int, len(shares))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return shares[order[a]].frac.Cmp(shares[order[b]].frac) > 0
	})
	for k := int64(0); k < missing; k++ {
		s := &shares[order[k]]
		s.base.Add(s.base, big.NewInt(1))
	}

	out := make([]Money, len(shares))
	for i, s := range shares {
		part := new(big.Int).Mul(s.base, big.NewInt(unit))
		if i == largest {
			part.Add(part, leftover)
		}
		if m.Minor < 0 {
			part.Neg(part)
		}
		if !part.IsInt64() {
			return nil, ErrOverflow
		}
		out[i] = Money{Minor: part.Int64(), Currency: m.Currency}
	}
	return out, nil
}
