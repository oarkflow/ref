// Package hierarchy models organisational trees — country → state → district →
// municipality, company → division → department, or any other nesting — and the
// reference data that is configured at one level and inherited by everything
// below it.
//
// A Tree is an immutable snapshot. Every query (ancestors, descendants, scope
// coverage) is a map or slice read with no locking, so a snapshot can be shared
// freely across goroutines. Mutations (Add, Update, Move, Remove) return a new
// Tree and leave the receiver untouched, which is what lets a server swap
// snapshots atomically while requests keep reading the old one.
package hierarchy

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Node is one organisational unit.
type Node struct {
	// ID uniquely identifies the unit. It must not contain the path separator
	// "/", because materialised paths are built from IDs.
	ID string `json:"id"`
	// ParentID is empty for a root.
	ParentID string `json:"parent_id,omitempty"`
	// Level names the unit's tier ("state", "district", ...). When the tree was
	// built with levels, it must be one of them.
	Level string `json:"level,omitempty"`
	// Code is an optional business code (e.g. an official LGD or FIPS code).
	Code string `json:"code,omitempty"`
	// Name is the display name.
	Name string `json:"name,omitempty"`
	// Attrs carries arbitrary per-unit data (population, head office, ...).
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Errors reported by tree construction and mutation. Use errors.Is.
var (
	ErrNotFound      = errors.New("hierarchy: node not found")
	ErrDuplicate     = errors.New("hierarchy: duplicate node id")
	ErrInvalidID     = errors.New("hierarchy: invalid node id")
	ErrMissingParent = errors.New("hierarchy: parent not found")
	ErrCycle         = errors.New("hierarchy: move would create a cycle")
	ErrLevel         = errors.New("hierarchy: level violates the configured level order")
	ErrHasChildren   = errors.New("hierarchy: node has children")
)

// Options controls level validation.
type Options struct {
	// Levels lists the tiers from the top down, e.g.
	// ["country", "state", "district", "municipality"]. Empty disables level
	// checks entirely.
	Levels []string
	// StrictLevels requires a child to sit exactly one level below its parent.
	// Without it a child only has to be somewhere below its parent, which
	// allows e.g. a municipality directly under a state for union territories.
	StrictLevels bool
	// RootLevels restricts which levels may be roots. Empty means only the
	// first level may be a root when Levels is set.
	RootLevels []string
}

type entry struct {
	node     Node
	depth    int
	path     []string // ancestor ids from the root down to and including this node
	children []string // sorted by name, then id
}

// Tree is an immutable hierarchy snapshot.
type Tree struct {
	opts       Options
	levelIndex map[string]int
	nodes      map[string]*entry
	roots      []string
}

// Build constructs a tree from nodes given in any order. It rejects duplicate
// ids, dangling parents, cycles and level violations.
func Build(nodes []Node, opts Options) (*Tree, error) {
	t := &Tree{opts: opts, nodes: make(map[string]*entry, len(nodes))}
	if len(opts.Levels) > 0 {
		t.levelIndex = make(map[string]int, len(opts.Levels))
		for i, level := range opts.Levels {
			level = strings.TrimSpace(level)
			if level == "" {
				return nil, fmt.Errorf("hierarchy: empty level name at position %d", i)
			}
			if _, dup := t.levelIndex[level]; dup {
				return nil, fmt.Errorf("hierarchy: level %q listed twice", level)
			}
			t.levelIndex[level] = i
		}
	}
	for _, n := range nodes {
		if err := validID(n.ID); err != nil {
			return nil, err
		}
		if _, dup := t.nodes[n.ID]; dup {
			return nil, fmt.Errorf("%w: %q", ErrDuplicate, n.ID)
		}
		if n.ParentID == n.ID {
			return nil, fmt.Errorf("%w: %q is its own parent", ErrCycle, n.ID)
		}
		t.nodes[n.ID] = &entry{node: n}
	}
	for id, e := range t.nodes {
		if e.node.ParentID == "" {
			t.roots = append(t.roots, id)
			continue
		}
		parent, ok := t.nodes[e.node.ParentID]
		if !ok {
			return nil, fmt.Errorf("%w: %q (parent of %q)", ErrMissingParent, e.node.ParentID, id)
		}
		parent.children = append(parent.children, id)
	}
	// Walk from the roots. Anything never reached sits on a cycle.
	var walk func(id string, depth int, path []string) error
	walk = func(id string, depth int, path []string) error {
		e := t.nodes[id]
		e.depth = depth
		e.path = append(slices.Clip(path), id)
		t.sortIDs(e.children)
		for _, child := range e.children {
			if err := walk(child, depth+1, e.path); err != nil {
				return err
			}
		}
		return nil
	}
	t.sortIDs(t.roots)
	for _, root := range t.roots {
		if err := walk(root, 0, nil); err != nil {
			return nil, err
		}
	}
	for id, e := range t.nodes {
		if e.path == nil {
			return nil, fmt.Errorf("%w: %q is part of a parent cycle", ErrCycle, id)
		}
	}
	for _, e := range t.nodes {
		if err := t.checkLevel(e.node); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func validID(id string) error {
	if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) || strings.Contains(id, "/") {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return nil
}

func (t *Tree) sortIDs(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		a, b := t.nodes[ids[i]].node, t.nodes[ids[j]].node
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
}

func (t *Tree) checkLevel(n Node) error {
	if t.levelIndex == nil {
		return nil
	}
	idx, ok := t.levelIndex[n.Level]
	if !ok {
		return fmt.Errorf("%w: node %q has unknown level %q (levels: %s)", ErrLevel, n.ID, n.Level, strings.Join(t.opts.Levels, ", "))
	}
	if n.ParentID == "" {
		allowed := t.opts.RootLevels
		if len(allowed) == 0 {
			allowed = t.opts.Levels[:1]
		}
		if !slices.Contains(allowed, n.Level) {
			return fmt.Errorf("%w: root %q is level %q but roots must be one of %s", ErrLevel, n.ID, n.Level, strings.Join(allowed, ", "))
		}
		return nil
	}
	parent := t.nodes[n.ParentID]
	pidx := t.levelIndex[parent.node.Level]
	if idx <= pidx || (t.opts.StrictLevels && idx != pidx+1) {
		return fmt.Errorf("%w: %q (%s) cannot sit under %q (%s)", ErrLevel, n.ID, n.Level, parent.node.ID, parent.node.Level)
	}
	return nil
}

// Options returns the options the tree was built with.
func (t *Tree) Options() Options { return t.opts }

// Len reports the number of nodes.
func (t *Tree) Len() int { return len(t.nodes) }

// Has reports whether id exists.
func (t *Tree) Has(id string) bool { _, ok := t.nodes[id]; return ok }

// Get returns a node.
func (t *Tree) Get(id string) (Node, bool) {
	e, ok := t.nodes[id]
	if !ok {
		return Node{}, false
	}
	return e.node, true
}

// Depth returns the node's distance from its root (a root is 0), or -1.
func (t *Tree) Depth(id string) int {
	if e, ok := t.nodes[id]; ok {
		return e.depth
	}
	return -1
}

// Roots returns root ids in display order.
func (t *Tree) Roots() []string { return slices.Clone(t.roots) }

// Children returns a node's direct children in display order.
func (t *Tree) Children(id string) []string {
	if e, ok := t.nodes[id]; ok {
		return slices.Clone(e.children)
	}
	return nil
}

// PathIDs returns the ids from the root down to and including id.
func (t *Tree) PathIDs(id string) []string {
	if e, ok := t.nodes[id]; ok {
		return slices.Clone(e.path)
	}
	return nil
}

// Ancestors returns the nodes above id, root first, excluding id itself.
func (t *Tree) Ancestors(id string) []Node {
	e, ok := t.nodes[id]
	if !ok {
		return nil
	}
	out := make([]Node, 0, len(e.path)-1)
	for _, aid := range e.path[:len(e.path)-1] {
		out = append(out, t.nodes[aid].node)
	}
	return out
}

// MaterializedPath renders the node's path as "/root/child/id/". Storing it on
// scoped rows lets SQL restrict a query to a subtree with one indexed
// `path LIKE '/root/child/%'` predicate instead of a large IN list.
func (t *Tree) MaterializedPath(id string) string {
	e, ok := t.nodes[id]
	if !ok {
		return ""
	}
	return "/" + strings.Join(e.path, "/") + "/"
}

// IsAncestorOrSelf reports whether ancestor is id or one of its ancestors.
func (t *Tree) IsAncestorOrSelf(ancestor, id string) bool {
	a, ok := t.nodes[ancestor]
	if !ok {
		return false
	}
	e, ok := t.nodes[id]
	if !ok || len(e.path) <= a.depth {
		return false
	}
	return e.path[a.depth] == ancestor
}

// Descendants returns every node below id in depth-first display order,
// optionally including id itself first.
func (t *Tree) Descendants(id string, includeSelf bool) []string {
	e, ok := t.nodes[id]
	if !ok {
		return nil
	}
	var out []string
	if includeSelf {
		out = append(out, id)
	}
	var walk func(*entry)
	walk = func(e *entry) {
		for _, child := range e.children {
			out = append(out, child)
			walk(t.nodes[child])
		}
	}
	walk(e)
	return out
}

// AtLevel returns the nodes of one level, optionally restricted to the subtree
// under within (empty means the whole tree), in display order.
func (t *Tree) AtLevel(level, within string) []string {
	var candidates []string
	if within == "" {
		for _, root := range t.roots {
			candidates = append(candidates, t.Descendants(root, true)...)
		}
	} else {
		candidates = t.Descendants(within, true)
	}
	out := candidates[:0:0]
	for _, id := range candidates {
		if t.nodes[id].node.Level == level {
			out = append(out, id)
		}
	}
	return out
}

// Covers reports whether target lies within any of the scope nodes' subtrees.
// Unknown scope nodes are ignored; an empty scope covers nothing.
func (t *Tree) Covers(scope []string, target string) bool {
	for _, s := range scope {
		if t.IsAncestorOrSelf(s, target) {
			return true
		}
	}
	return false
}

// NormalizeScope drops unknown ids and any id already covered by another scope
// node, so the result is the minimal set of disjoint subtree roots.
func (t *Tree) NormalizeScope(scope []string) []string {
	known := make([]string, 0, len(scope))
	for _, s := range scope {
		if t.Has(s) && !slices.Contains(known, s) {
			known = append(known, s)
		}
	}
	out := known[:0:0]
	for _, s := range known {
		covered := false
		for _, other := range known {
			if other != s && t.IsAncestorOrSelf(other, s) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		di, dj := t.nodes[out[i]].depth, t.nodes[out[j]].depth
		if di != dj {
			return di < dj
		}
		return out[i] < out[j]
	})
	return out
}

// ScopeIDs expands scope into every node id it covers (roots included).
func (t *Tree) ScopeIDs(scope []string) []string {
	var out []string
	for _, s := range t.NormalizeScope(scope) {
		out = append(out, t.Descendants(s, true)...)
	}
	return out
}

// Nodes returns every node in depth-first display order.
func (t *Tree) Nodes() []Node {
	out := make([]Node, 0, len(t.nodes))
	for _, root := range t.roots {
		for _, id := range t.Descendants(root, true) {
			out = append(out, t.nodes[id].node)
		}
	}
	return out
}

// Upsert returns a new tree with n added or replaced. Replacing a node may
// change its parent (a move); the usual cycle and level checks apply.
func (t *Tree) Upsert(n Node) (*Tree, error) {
	if err := validID(n.ID); err != nil {
		return nil, err
	}
	if n.ParentID != "" {
		if !t.Has(n.ParentID) {
			return nil, fmt.Errorf("%w: %q", ErrMissingParent, n.ParentID)
		}
		if t.Has(n.ID) && t.IsAncestorOrSelf(n.ID, n.ParentID) {
			return nil, fmt.Errorf("%w: %q cannot move under its own descendant %q", ErrCycle, n.ID, n.ParentID)
		}
	}
	nodes := t.Nodes()
	replaced := false
	for i := range nodes {
		if nodes[i].ID == n.ID {
			nodes[i] = n
			replaced = true
			break
		}
	}
	if !replaced {
		nodes = append(nodes, n)
	}
	return Build(nodes, t.opts)
}

// Move returns a new tree with id re-parented under newParent ("" makes it a
// root).
func (t *Tree) Move(id, newParent string) (*Tree, error) {
	n, ok := t.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	n.ParentID = newParent
	return t.Upsert(n)
}

// Remove returns a new tree without id. A node with children is refused unless
// cascade is set, in which case its whole subtree goes.
func (t *Tree) Remove(id string, cascade bool) (*Tree, []string, error) {
	e, ok := t.nodes[id]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if len(e.children) > 0 && !cascade {
		return nil, nil, fmt.Errorf("%w: %q has %d children", ErrHasChildren, id, len(e.children))
	}
	removed := t.Descendants(id, true)
	gone := make(map[string]bool, len(removed))
	for _, r := range removed {
		gone[r] = true
	}
	var nodes []Node
	for _, n := range t.Nodes() {
		if !gone[n.ID] {
			nodes = append(nodes, n)
		}
	}
	next, err := Build(nodes, t.opts)
	return next, removed, err
}

// TreeView is a nested rendering of a subtree, convenient as a JSON response.
type TreeView struct {
	Node
	Depth    int        `json:"depth"`
	Path     string     `json:"path"`
	Children []TreeView `json:"children,omitempty"`
}

// View renders the subtree under id down to maxDepth levels below it (a
// negative maxDepth means unlimited).
func (t *Tree) View(id string, maxDepth int) (TreeView, bool) {
	e, ok := t.nodes[id]
	if !ok {
		return TreeView{}, false
	}
	var build func(*entry, int) TreeView
	build = func(e *entry, remaining int) TreeView {
		v := TreeView{Node: e.node, Depth: e.depth, Path: t.MaterializedPath(e.node.ID)}
		if remaining == 0 {
			return v
		}
		for _, child := range e.children {
			v.Children = append(v.Children, build(t.nodes[child], remaining-1))
		}
		return v
	}
	return build(e, maxDepth), true
}
