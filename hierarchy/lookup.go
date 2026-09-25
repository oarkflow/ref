package hierarchy

import (
	"fmt"
	"sort"
	"strings"
)

// LookupEntry is one reference-data value (a code in a code table) defined at
// some point in the hierarchy.
//
// An entry with an empty NodeID is global. An entry on a node applies to that
// node and everything below it. Workspace and Department narrow an entry to
// one workspace or department; empty means "any". When several entries define
// the same Set+Code for a request, the most specific one wins (see Resolve),
// so a district can relabel or disable a code the state defined, and a
// department can override its district.
type LookupEntry struct {
	Set        string         `json:"set"`
	Code       string         `json:"code"`
	Label      string         `json:"label,omitempty"`
	NodeID     string         `json:"node_id,omitempty"`
	Workspace  string         `json:"workspace,omitempty"`
	Department string         `json:"department,omitempty"`
	Sort       int            `json:"sort,omitempty"`
	Disabled   bool           `json:"disabled,omitempty"`
	Attrs      map[string]any `json:"attrs,omitempty"`
}

// Key identifies the exact definition slot an entry occupies. Two entries with
// the same key are the same definition (an upsert replaces the other).
func (e LookupEntry) Key() string {
	return strings.Join([]string{e.Set, e.Code, e.NodeID, e.Workspace, e.Department}, "\x00")
}

// LookupItem is a resolved value.
type LookupItem struct {
	Code  string         `json:"code"`
	Label string         `json:"label"`
	Sort  int            `json:"sort,omitempty"`
	Attrs map[string]any `json:"attrs,omitempty"`
	// Source is the node that supplied the winning definition ("" = global).
	Source     string `json:"source,omitempty"`
	Workspace  string `json:"workspace,omitempty"`
	Department string `json:"department,omitempty"`
}

// LookupQuery selects the context a lookup set is resolved for.
type LookupQuery struct {
	Set        string
	NodeID     string // "" resolves global definitions only
	Workspace  string
	Department string
	// IncludeDisabled keeps disabled winners in the result (flagged via
	// Attrs["disabled"]=true), for admin screens that edit overrides.
	IncludeDisabled bool
}

// Lookups is an immutable collection of lookup entries.
type Lookups struct {
	bySet map[string][]LookupEntry
}

// NewLookups validates and indexes entries. A later entry with the same Key
// replaces an earlier one.
func NewLookups(entries []LookupEntry) (*Lookups, error) {
	l := &Lookups{bySet: map[string][]LookupEntry{}}
	index := map[string]int{}
	for i, e := range entries {
		if strings.TrimSpace(e.Set) == "" || strings.TrimSpace(e.Code) == "" {
			return nil, fmt.Errorf("hierarchy: lookup entry %d needs a set and a code", i)
		}
		key := e.Key()
		if at, ok := index[key]; ok {
			list := l.bySet[e.Set]
			list[at] = e
			continue
		}
		index[key] = len(l.bySet[e.Set])
		l.bySet[e.Set] = append(l.bySet[e.Set], e)
	}
	return l, nil
}

// Entries returns every entry, grouped by set in set-name order.
func (l *Lookups) Entries() []LookupEntry {
	if l == nil {
		return nil
	}
	sets := l.Sets()
	var out []LookupEntry
	for _, s := range sets {
		out = append(out, l.bySet[s]...)
	}
	return out
}

// Sets returns the defined set names, sorted.
func (l *Lookups) Sets() []string {
	if l == nil {
		return nil
	}
	out := make([]string, 0, len(l.bySet))
	for s := range l.bySet {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Upsert returns a new collection with e added or replacing the entry at the
// same Key.
func (l *Lookups) Upsert(e LookupEntry) (*Lookups, error) {
	return NewLookups(append(l.Entries(), e))
}

// Delete returns a new collection without the entry at key.
func (l *Lookups) Delete(key string) (*Lookups, bool) {
	var kept []LookupEntry
	found := false
	for _, e := range l.Entries() {
		if e.Key() == key {
			found = true
			continue
		}
		kept = append(kept, e)
	}
	next, _ := NewLookups(kept)
	return next, found
}

// Resolve returns the effective values of a set for the query's context.
//
// Candidate entries are those that are global or defined on the query node or
// one of its ancestors, and whose workspace/department are empty or equal to
// the query's. For each code the most specific candidate wins, ranked by:
//
//  1. node depth (a deeper node beats its ancestors; global ranks lowest),
//  2. a department match beats a workspace match, which beats neither.
//
// A winning entry that is Disabled removes the code for this context, which
// is how a lower level hides a value inherited from above. Results are sorted
// by Sort, then Code.
func (l *Lookups) Resolve(tree *Tree, q LookupQuery) []LookupItem {
	if l == nil {
		return nil
	}
	type candidate struct {
		entry LookupEntry
		score int
	}
	best := map[string]candidate{}
	for _, e := range l.bySet[q.Set] {
		depth := -1
		if e.NodeID != "" {
			if q.NodeID == "" || tree == nil || !tree.IsAncestorOrSelf(e.NodeID, q.NodeID) {
				continue
			}
			depth = tree.Depth(e.NodeID)
		}
		if e.Workspace != "" && e.Workspace != q.Workspace {
			continue
		}
		if e.Department != "" && e.Department != q.Department {
			continue
		}
		score := (depth+1)*4 + boolScore(e.Department != "")*2 + boolScore(e.Workspace != "")
		if cur, ok := best[e.Code]; !ok || score > cur.score {
			best[e.Code] = candidate{entry: e, score: score}
		}
	}
	out := make([]LookupItem, 0, len(best))
	for _, c := range best {
		e := c.entry
		if e.Disabled && !q.IncludeDisabled {
			continue
		}
		label := e.Label
		if label == "" {
			label = e.Code
		}
		item := LookupItem{Code: e.Code, Label: label, Sort: e.Sort, Attrs: e.Attrs, Source: e.NodeID, Workspace: e.Workspace, Department: e.Department}
		if e.Disabled {
			attrs := make(map[string]any, len(e.Attrs)+1)
			for k, v := range e.Attrs {
				attrs[k] = v
			}
			attrs["disabled"] = true
			item.Attrs = attrs
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sort != out[j].Sort {
			return out[i].Sort < out[j].Sort
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func boolScore(b bool) int {
	if b {
		return 1
	}
	return 0
}
