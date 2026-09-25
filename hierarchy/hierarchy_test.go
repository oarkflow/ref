package hierarchy

import (
	"errors"
	"slices"
	"testing"
)

var govLevels = Options{Levels: []string{"country", "state", "district", "municipality"}}

func govTree(t *testing.T) *Tree {
	t.Helper()
	tree, err := Build([]Node{
		// Deliberately out of order: Build must not depend on input order.
		{ID: "ktm-metro", ParentID: "ktm", Level: "municipality", Name: "Kathmandu Metro"},
		{ID: "np", Level: "country", Name: "Nepal"},
		{ID: "bagmati", ParentID: "np", Level: "state", Name: "Bagmati"},
		{ID: "ktm", ParentID: "bagmati", Level: "district", Name: "Kathmandu"},
		{ID: "lalitpur", ParentID: "bagmati", Level: "district", Name: "Lalitpur"},
		{ID: "lalitpur-metro", ParentID: "lalitpur", Level: "municipality", Name: "Lalitpur Metro"},
		{ID: "koshi", ParentID: "np", Level: "state", Name: "Koshi"},
		{ID: "morang", ParentID: "koshi", Level: "district", Name: "Morang"},
	}, govLevels)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return tree
}

func TestTreeNavigation(t *testing.T) {
	tree := govTree(t)
	if tree.Len() != 8 {
		t.Fatalf("len = %d", tree.Len())
	}
	if got := tree.PathIDs("ktm-metro"); !slices.Equal(got, []string{"np", "bagmati", "ktm", "ktm-metro"}) {
		t.Fatalf("path = %v", got)
	}
	if got := tree.MaterializedPath("ktm"); got != "/np/bagmati/ktm/" {
		t.Fatalf("materialized path = %q", got)
	}
	anc := tree.Ancestors("ktm-metro")
	if len(anc) != 3 || anc[0].ID != "np" || anc[2].ID != "ktm" {
		t.Fatalf("ancestors = %+v", anc)
	}
	if got := tree.Children("bagmati"); !slices.Equal(got, []string{"ktm", "lalitpur"}) {
		t.Fatalf("children sorted by name = %v", got)
	}
	if got := tree.Descendants("bagmati", false); !slices.Equal(got, []string{"ktm", "ktm-metro", "lalitpur", "lalitpur-metro"}) {
		t.Fatalf("descendants = %v", got)
	}
	if got := tree.AtLevel("district", "np"); !slices.Equal(got, []string{"ktm", "lalitpur", "morang"}) {
		t.Fatalf("districts = %v", got)
	}
	if !tree.IsAncestorOrSelf("bagmati", "ktm-metro") || tree.IsAncestorOrSelf("koshi", "ktm-metro") || !tree.IsAncestorOrSelf("ktm", "ktm") {
		t.Fatal("IsAncestorOrSelf wrong")
	}
	if tree.IsAncestorOrSelf("ktm-metro", "ktm") {
		t.Fatal("descendant must not be an ancestor")
	}
	v, ok := tree.View("bagmati", 1)
	if !ok || len(v.Children) != 2 || len(v.Children[0].Children) != 0 {
		t.Fatalf("view depth 1 = %+v", v)
	}
}

func TestScopeCoverage(t *testing.T) {
	tree := govTree(t)
	scope := []string{"ktm", "bagmati", "unknown", "morang"}
	if got := tree.NormalizeScope(scope); !slices.Equal(got, []string{"bagmati", "morang"}) {
		t.Fatalf("normalized = %v", got)
	}
	if !tree.Covers(scope, "lalitpur-metro") || tree.Covers([]string{"ktm"}, "lalitpur") || tree.Covers(nil, "np") {
		t.Fatal("Covers wrong")
	}
	ids := tree.ScopeIDs([]string{"ktm", "morang"})
	if !slices.Equal(ids, []string{"ktm", "ktm-metro", "morang"}) {
		t.Fatalf("scope ids = %v", ids)
	}
}

func TestBuildRejectsBadTrees(t *testing.T) {
	cases := []struct {
		name  string
		nodes []Node
		opts  Options
		want  error
	}{
		{"duplicate", []Node{{ID: "a"}, {ID: "a"}}, Options{}, ErrDuplicate},
		{"missing parent", []Node{{ID: "a", ParentID: "x"}}, Options{}, ErrMissingParent},
		{"cycle", []Node{{ID: "r"}, {ID: "a", ParentID: "b"}, {ID: "b", ParentID: "a"}}, Options{}, ErrCycle},
		{"self parent", []Node{{ID: "a", ParentID: "a"}}, Options{}, ErrCycle},
		{"slash id", []Node{{ID: "a/b"}}, Options{}, ErrInvalidID},
		{"unknown level", []Node{{ID: "a", Level: "galaxy"}}, govLevels, ErrLevel},
		{"root not top level", []Node{{ID: "a", Level: "district"}}, govLevels, ErrLevel},
		{"child above parent", []Node{{ID: "a", Level: "country"}, {ID: "b", ParentID: "a", Level: "country"}}, govLevels, ErrLevel},
		{"strict skip", []Node{{ID: "a", Level: "country"}, {ID: "b", ParentID: "a", Level: "district"}},
			Options{Levels: govLevels.Levels, StrictLevels: true}, ErrLevel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Build(tc.nodes, tc.opts); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// Non-strict levels allow skipping a tier (e.g. a municipality directly
	// under a state).
	if _, err := Build([]Node{{ID: "a", Level: "country"}, {ID: "b", ParentID: "a", Level: "district"}}, govLevels); err != nil {
		t.Fatalf("non-strict skip should pass: %v", err)
	}
}

func TestMutationsAreCopyOnWrite(t *testing.T) {
	tree := govTree(t)
	moved, err := tree.Move("lalitpur", "koshi")
	if err != nil {
		t.Fatal(err)
	}
	if !moved.IsAncestorOrSelf("koshi", "lalitpur-metro") || moved.IsAncestorOrSelf("bagmati", "lalitpur-metro") {
		t.Fatal("move did not re-parent the subtree")
	}
	if !tree.IsAncestorOrSelf("bagmati", "lalitpur-metro") {
		t.Fatal("original tree must be unchanged")
	}
	if _, err := tree.Move("bagmati", "ktm"); !errors.Is(err, ErrCycle) {
		t.Fatalf("move under own descendant: %v", err)
	}
	if _, err := tree.Upsert(Node{ID: "x", ParentID: "ktm", Level: "state"}); !errors.Is(err, ErrLevel) {
		t.Fatalf("level check on upsert: %v", err)
	}
	added, err := tree.Upsert(Node{ID: "bhaktapur", ParentID: "bagmati", Level: "district", Name: "Bhaktapur"})
	if err != nil || !added.Has("bhaktapur") || tree.Has("bhaktapur") {
		t.Fatalf("upsert: %v", err)
	}
	if _, _, err := tree.Remove("bagmati", false); !errors.Is(err, ErrHasChildren) {
		t.Fatalf("remove with children: %v", err)
	}
	pruned, removed, err := tree.Remove("bagmati", true)
	if err != nil || len(removed) != 5 || pruned.Has("ktm-metro") || pruned.Len() != 3 {
		t.Fatalf("cascade remove: %v removed=%v", err, removed)
	}
}

func TestLookupInheritanceAndOverrides(t *testing.T) {
	tree := govTree(t)
	lookups, err := NewLookups([]LookupEntry{
		{Set: "service", Code: "birth", Label: "Birth registration", Sort: 1},
		{Set: "service", Code: "death", Label: "Death registration", Sort: 2},
		{Set: "service", Code: "tax", Label: "Property tax", Sort: 3},
		// Bagmati renames tax for everything below it.
		{Set: "service", Code: "tax", Label: "Integrated property tax", NodeID: "bagmati", Sort: 3},
		// Kathmandu district disables death registration (handled elsewhere).
		{Set: "service", Code: "death", NodeID: "ktm", Disabled: true},
		// Kathmandu Metro adds a local service.
		{Set: "service", Code: "parking", Label: "Parking permit", NodeID: "ktm-metro", Sort: 9},
		// Department-specific label inside Kathmandu Metro.
		{Set: "service", Code: "tax", Label: "Tax (revenue dept)", NodeID: "ktm-metro", Department: "revenue"},
		// Workspace-specific global code.
		{Set: "service", Code: "pilot", Label: "Pilot", Workspace: "sandbox"},
	})
	if err != nil {
		t.Fatal(err)
	}
	codes := func(items []LookupItem) (out []string) {
		for _, i := range items {
			out = append(out, i.Code+"="+i.Label)
		}
		return
	}
	got := codes(lookups.Resolve(tree, LookupQuery{Set: "service", NodeID: "ktm-metro"}))
	want := []string{"birth=Birth registration", "tax=Integrated property tax", "parking=Parking permit"}
	if !slices.Equal(got, want) {
		t.Fatalf("ktm-metro = %v, want %v", got, want)
	}
	got = codes(lookups.Resolve(tree, LookupQuery{Set: "service", NodeID: "ktm-metro", Department: "revenue"}))
	if !slices.Contains(got, "tax=Tax (revenue dept)") {
		t.Fatalf("department override missing: %v", got)
	}
	got = codes(lookups.Resolve(tree, LookupQuery{Set: "service", NodeID: "morang"}))
	want = []string{"birth=Birth registration", "death=Death registration", "tax=Property tax"}
	if !slices.Equal(got, want) {
		t.Fatalf("morang = %v, want %v", got, want)
	}
	got = codes(lookups.Resolve(tree, LookupQuery{Set: "service", NodeID: "morang", Workspace: "sandbox"}))
	if !slices.Contains(got, "pilot=Pilot") {
		t.Fatalf("workspace entry missing: %v", got)
	}
	all := lookups.Resolve(tree, LookupQuery{Set: "service", NodeID: "ktm", IncludeDisabled: true})
	foundDisabled := false
	for _, item := range all {
		if item.Code == "death" && item.Attrs["disabled"] == true && item.Source == "ktm" {
			foundDisabled = true
		}
	}
	if !foundDisabled {
		t.Fatalf("IncludeDisabled should surface the override: %+v", all)
	}
	// Upsert replaces the same definition slot rather than duplicating it.
	next, err := lookups.Upsert(LookupEntry{Set: "service", Code: "birth", Label: "Birth cert", Sort: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := codes(next.Resolve(tree, LookupQuery{Set: "service"})); got[0] != "birth=Birth cert" || len(next.Entries()) != len(lookups.Entries()) {
		t.Fatalf("upsert = %v", got)
	}
	if _, found := next.Delete(LookupEntry{Set: "service", Code: "pilot", Workspace: "sandbox"}.Key()); !found {
		t.Fatal("delete by key")
	}
}
