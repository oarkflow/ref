package server

import (
	"fmt"
	"strings"
)

// diffCells bounds the dynamic-programming table of a line diff. Beyond it
// the differing middle of two files is reported as one replaced block.
const diffCells = 1_500_000

const diffContext = 3

type dline struct {
	op   byte // ' ', '-' or '+'
	text string
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// lineScript is a shortest-ish edit script from a to b: the common prefix
// and suffix are peeled off, the middle is diffed by LCS.
func lineScript(a, b []string) []dline {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	sfx := 0
	for sfx < len(a)-p && sfx < len(b)-p && a[len(a)-1-sfx] == b[len(b)-1-sfx] {
		sfx++
	}
	am, bm := a[p:len(a)-sfx], b[p:len(b)-sfx]
	out := make([]dline, 0, len(a)+len(b))
	for _, l := range a[:p] {
		out = append(out, dline{' ', l})
	}
	n, m := len(am), len(bm)
	switch {
	case n == 0:
		for _, l := range bm {
			out = append(out, dline{'+', l})
		}
	case m == 0:
		for _, l := range am {
			out = append(out, dline{'-', l})
		}
	case n*m > diffCells:
		for _, l := range am {
			out = append(out, dline{'-', l})
		}
		for _, l := range bm {
			out = append(out, dline{'+', l})
		}
	default:
		w := m + 1
		t := make([]uint32, (n+1)*w)
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if am[i] == bm[j] {
					t[i*w+j] = t[(i+1)*w+j+1] + 1
				} else {
					t[i*w+j] = max(t[(i+1)*w+j], t[i*w+j+1])
				}
			}
		}
		i, j := 0, 0
		for i < n && j < m {
			switch {
			case am[i] == bm[j]:
				out = append(out, dline{' ', am[i]})
				i++
				j++
			case t[(i+1)*w+j] >= t[i*w+j+1]:
				out = append(out, dline{'-', am[i]})
				i++
			default:
				out = append(out, dline{'+', bm[j]})
				j++
			}
		}
		for ; i < n; i++ {
			out = append(out, dline{'-', am[i]})
		}
		for ; j < m; j++ {
			out = append(out, dline{'+', bm[j]})
		}
	}
	for _, l := range a[len(a)-sfx:] {
		out = append(out, dline{' ', l})
	}
	return out
}

// unifiedDiff renders the difference between old and new (nil = absent) as a
// unified diff for one file. Equal texts give "".
func unifiedDiff(name string, old, new *string) string {
	var a, b []string
	if old != nil {
		a = splitLines(*old)
	}
	if new != nil {
		b = splitLines(*new)
	}
	script := lineScript(a, b)

	// aIdx[k], bIdx[k]: lines of a and b consumed before script[k].
	aIdx := make([]int, len(script)+1)
	bIdx := make([]int, len(script)+1)
	for k, l := range script {
		aIdx[k+1], bIdx[k+1] = aIdx[k], bIdx[k]
		if l.op != '+' {
			aIdx[k+1]++
		}
		if l.op != '-' {
			bIdx[k+1]++
		}
	}
	var changes []int
	for k, l := range script {
		if l.op != ' ' {
			changes = append(changes, k)
		}
	}
	if len(changes) == 0 {
		return ""
	}
	var sb strings.Builder
	from, to := "a/"+name, "b/"+name
	if old == nil {
		from = "/dev/null"
	}
	if new == nil {
		to = "/dev/null"
	}
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", from, to)

	for i := 0; i < len(changes); {
		start := max(changes[i]-diffContext, 0)
		end := changes[i]
		j := i
		for j+1 < len(changes) && changes[j+1]-end <= 2*diffContext+1 {
			j++
			end = changes[j]
		}
		end = min(end+diffContext+1, len(script))
		aLen, bLen := aIdx[end]-aIdx[start], bIdx[end]-bIdx[start]
		aStart, bStart := aIdx[start]+1, bIdx[start]+1
		if aLen == 0 {
			aStart = aIdx[start]
		}
		if bLen == 0 {
			bStart = bIdx[start]
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", aStart, aLen, bStart, bLen)
		for _, l := range script[start:end] {
			sb.WriteByte(l.op)
			sb.WriteString(l.text)
			sb.WriteByte('\n')
		}
		i = j + 1
	}
	return sb.String()
}
