package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/bcl"

	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/studio"
	"github.com/oarkflow/ref/studio/model"
)

// maxHistory bounds a draft's undo depth.
const maxHistory = 200

// Store holds drafts. MemoryStore keeps them in memory; SQLStore persists
// them. Get and List return the live *Draft (with its subscribers), so a store
// that persists keeps every draft it has loaded in memory too.
type Store interface {
	Create(d *Draft) error
	Get(id string) (*Draft, bool)
	Delete(id string) bool
	// List returns every draft, oldest first.
	List() []*Draft
	// Save persists the draft's current state after an edit. Memory stores
	// have nothing to do.
	Save(d *Draft) error
}

// MemoryStore keeps drafts in memory.
type MemoryStore struct {
	mu     sync.RWMutex
	drafts map[string]*Draft
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{drafts: map[string]*Draft{}} }

func (m *MemoryStore) Create(d *Draft) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.drafts[d.id]; ok {
		return fmt.Errorf("draft %s exists", d.id)
	}
	m.drafts[d.id] = d
	return nil
}

func (m *MemoryStore) Get(id string) (*Draft, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.drafts[id]
	return d, ok
}

// Save implements Store: a draft in memory is already saved.
func (m *MemoryStore) Save(*Draft) error { return nil }

func (m *MemoryStore) Delete(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.drafts[id]
	delete(m.drafts, id)
	return ok
}

func (m *MemoryStore) List() []*Draft {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return sortDrafts(m.drafts)
}

// sortDrafts lists drafts oldest first.
func sortDrafts(all map[string]*Draft) []*Draft {
	out := make([]*Draft, 0, len(all))
	for _, d := range all {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].created.Equal(out[j].created) {
			return out[i].created.Before(out[j].created)
		}
		return out[i].id < out[j].id
	})
	return out
}

// entry is one file of a draft. An immutable value: edits build new entries.
// A file the structural editor cannot open (it parses as BCL, but the
// statement scanner does not agree) is kept as raw text and can only be
// replaced whole.
type entry struct {
	file *model.File
	raw  string
	why  string // why file is nil
	sum  *entrySum
}

// entrySum memoises an entry's content hash. Entries are shared between a
// draft's snapshots, so each distinct content is hashed once, not once per
// undo step.
type entrySum struct {
	once sync.Once
	v    string
}

func newEntry(file *model.File, raw, why string) entry {
	return entry{file: file, raw: raw, why: why, sum: &entrySum{}}
}

func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// hash is the SHA-256 of the entry's text.
func (e entry) hash() string {
	if e.sum == nil {
		return hashText(e.text())
	}
	e.sum.once.Do(func() { e.sum.v = hashText(e.text()) })
	return e.sum.v
}

func (e entry) text() string {
	if e.file != nil {
		return e.file.Text()
	}
	return e.raw
}

// snapshot is a draft's complete state. Snapshots are never mutated once
// they have been stored; copy with maps.Clone before changing.
type snapshot map[string]entry

// openEntry builds an entry for content. parseErr is non-nil when content is
// not valid BCL at all.
func openEntry(name, content string) (e entry, parseErr error) {
	if isAssetPath(name) {
		return newEntry(nil, content, "an asset is not BCL"), nil
	}
	f, err := model.Open(name, []byte(content))
	if err == nil {
		return newEntry(f, "", ""), nil
	}
	if _, perr := bcl.ParseFile(name, []byte(content)); perr != nil {
		return newEntry(nil, content, perr.Error()), perr
	}
	return newEntry(nil, content, err.Error()), nil
}

// isAssetPath reports whether a snapshot key is an asset (a page template or
// static file, "templates/pages/x.html") rather than a BCL file. Bundle file
// names are flat, so a slash is unambiguous.
func isAssetPath(name string) bool { return strings.Contains(name, "/") }

// bundle is the snapshot's BCL files. Assets are not part of it.
func (s snapshot) bundle() platform.Bundle {
	b := make(platform.Bundle, 0, len(s))
	for name, e := range s {
		if isAssetPath(name) {
			continue
		}
		b = append(b, platform.BundleFile{Path: name, Content: e.text()})
	}
	sort.Slice(b, func(i, j int) bool { return b[i].Path < b[j].Path })
	return b
}

// assets is the snapshot's assets, sorted, not yet validated.
func (s snapshot) assets() platform.Assets {
	var a platform.Assets
	for name, e := range s {
		if isAssetPath(name) {
			a = append(a, platform.BundleFile{Path: name, Content: e.text()})
		}
	}
	sort.Slice(a, func(i, j int) bool { return a[i].Path < a[j].Path })
	return a
}

// names lists the BCL files.
func (s snapshot) names() []string {
	out := make([]string, 0, len(s))
	for n := range s {
		if !isAssetPath(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// changedFiles lists the files that differ between two snapshots.
func changedFiles(a, b snapshot) []string {
	seen := map[string]bool{}
	var out []string
	for n, e := range b {
		seen[n] = true
		if old, ok := a[n]; !ok || old.text() != e.text() {
			out = append(out, n)
		}
	}
	for n := range a {
		if !seen[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// event is something published on a draft's stream.
type event struct {
	Name string
	Data any
}

type changedEvent struct {
	Version int64    `json:"version"`
	Changed []string `json:"changed"`
}

// Draft is a working copy of an application's BCL bundle. It implements
// studio.Draft.
type Draft struct {
	id, owner string
	created   time.Time
	base      string          // revision id the draft started from, if any
	baseFiles platform.Bundle // what the draft started from, for diffs
	// baseAssets are the assets the draft started from (a revision's overrides).
	baseAssets platform.Assets

	mu      sync.Mutex
	name    string
	updated time.Time
	version int64
	state   snapshot
	past    []snapshot
	future  []snapshot
	// caches, valid for the version they were computed at
	diagVer  int64
	diag     []studio.Diagnostic
	diagOK   bool
	baseDoc  *platform.Document
	baseDone bool

	subMu   sync.Mutex
	subs    map[int]chan event
	nextSub int
	closed  bool
}

var _ studio.Draft = (*Draft)(nil)

func newDraftID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "drf_" + hex.EncodeToString(b[:])
}

func newDraft(owner, name, base string, bundle platform.Bundle, assets platform.Assets, now time.Time) *Draft {
	state := make(snapshot, len(bundle)+len(assets))
	for _, f := range bundle {
		e, _ := openEntry(f.Path, f.Content)
		state[f.Path] = e
	}
	for _, f := range assets {
		e, _ := openEntry(f.Path, f.Content)
		state[f.Path] = e
	}
	id := newDraftID()
	if name == "" {
		name = "draft-" + id[len(id)-6:]
	}
	return &Draft{
		id: id, owner: owner, name: name, base: base, created: now, updated: now,
		baseFiles: append(platform.Bundle(nil), bundle...), baseAssets: append(platform.Assets(nil), assets...),
		state: state, version: 1,
		subs: map[int]chan event{},
	}
}

// ID implements studio.Draft.
func (d *Draft) ID() string { return d.id }

// Version implements studio.Draft.
func (d *Draft) Version() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.version
}

// Bundle implements studio.Draft: a snapshot of the draft's files.
func (d *Draft) Bundle() platform.Bundle {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state.bundle()
}

// Assets implements studio.AssetDraft: a snapshot of the draft's page
// templates and static files. Every asset change bumps Version, so a preview
// built from the draft rebuilds.
func (d *Draft) Assets() platform.Assets {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state.assets()
}

var _ studio.AssetDraft = (*Draft)(nil)

// Subscribe implements studio.Draft: the draft's version after each change.
func (d *Draft) Subscribe() (<-chan int64, func()) {
	ch, cancel := d.subscribe()
	out := make(chan int64, 8)
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(out)
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if c, ok := ev.Data.(changedEvent); ok {
					select {
					case out <- c.Version:
					default:
					}
				}
			case <-stop:
				return
			}
		}
	}()
	return out, func() { once.Do(func() { cancel(); close(stop) }) }
}

// subscribe registers for all events.
func (d *Draft) subscribe() (<-chan event, func()) {
	d.subMu.Lock()
	defer d.subMu.Unlock()
	ch := make(chan event, 32)
	if d.closed {
		close(ch)
		return ch, func() {}
	}
	n := d.nextSub
	d.nextSub++
	d.subs[n] = ch
	return ch, func() {
		d.subMu.Lock()
		defer d.subMu.Unlock()
		if c, ok := d.subs[n]; ok {
			delete(d.subs, n)
			close(c)
		}
	}
}

// publish sends an event to every subscriber without blocking: a subscriber
// that falls behind misses events and resyncs from the version.
func (d *Draft) publish(name string, data any) {
	d.subMu.Lock()
	defer d.subMu.Unlock()
	for _, ch := range d.subs {
		select {
		case ch <- event{Name: name, Data: data}:
		default:
		}
	}
}

// close ends every subscription (the draft was deleted).
func (d *Draft) close() {
	d.subMu.Lock()
	defer d.subMu.Unlock()
	d.closed = true
	for n, ch := range d.subs {
		delete(d.subs, n)
		close(ch)
	}
}

// view is a consistent read of the draft.
type view struct {
	version int64
	state   snapshot
	name    string
	updated time.Time
}

func (d *Draft) view() view {
	d.mu.Lock()
	defer d.mu.Unlock()
	return view{version: d.version, state: d.state, name: d.name, updated: d.updated}
}

// dirty reports whether the draft differs from what it started from.
func (v view) dirty(base platform.Bundle, baseAssets platform.Assets) bool {
	if len(v.state) != len(base)+len(baseAssets) {
		return true
	}
	for _, f := range base {
		e, ok := v.state[f.Path]
		if !ok || e.text() != f.Content {
			return true
		}
	}
	for _, f := range baseAssets {
		e, ok := v.state[f.Path]
		if !ok || e.text() != f.Content {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Ops
// ---------------------------------------------------------------------------

// Op is one edit. Which fields apply depends on Op (docs/studio-api.md).
type Op struct {
	Op      string  `json:"op"`
	File    string  `json:"file"`
	Path    string  `json:"path,omitempty"`
	Value   *string `json:"value,omitempty"`
	Parent  string  `json:"parent,omitempty"`
	Type    string  `json:"type,omitempty"`
	ID      string  `json:"id,omitempty"`
	Body    string  `json:"body,omitempty"`
	NewID   string  `json:"newId,omitempty"`
	Index   *int    `json:"index,omitempty"`
	Content string  `json:"content,omitempty"`
	NewFile string  `json:"newFile,omitempty"`
}

// errStale reports an ifVersion that no longer matches.
func errStale(have int64) *apiError {
	return errf(http.StatusConflict, "stale", "the draft changed (it is at version %d)", have).with(map[string]any{"version": have})
}

func (s snapshot) editable(name string) (*model.File, error) {
	e, ok := s[name]
	if !ok {
		return nil, fmt.Errorf("no file %q in the draft", name)
	}
	if e.file == nil {
		return nil, fmt.Errorf("file %q cannot be edited structurally (%s); replace it as text instead", name, e.why)
	}
	return e.file, nil
}

// apply runs one op on s, which the caller has cloned.
func (s snapshot) apply(op Op) error {
	switch op.Op {
	case "setField":
		if op.Value == nil {
			return errors.New("setField needs a value")
		}
		f, err := s.editable(op.File)
		if err != nil {
			return err
		}
		nf, err := f.SetField(model.ParsePath(op.Path), *op.Value)
		return s.put(op.File, nf, err)
	case "removeField":
		f, err := s.editable(op.File)
		if err != nil {
			return err
		}
		nf, err := f.RemoveField(model.ParsePath(op.Path))
		return s.put(op.File, nf, err)
	case "removeBlock":
		f, err := s.editable(op.File)
		if err != nil {
			return err
		}
		nf, err := f.RemoveBlock(model.ParsePath(op.Path))
		return s.put(op.File, nf, err)
	case "addBlock":
		if op.Type == "" {
			return errors.New("addBlock needs a type")
		}
		f, err := s.editable(op.File)
		if err != nil {
			return err
		}
		nf, err := f.AddBlock(model.ParsePath(op.Parent), op.Type, op.ID, op.Body)
		return s.put(op.File, nf, err)
	case "renameBlock":
		if op.NewID == "" {
			return errors.New("renameBlock needs newId")
		}
		f, err := s.editable(op.File)
		if err != nil {
			return err
		}
		nf, err := f.RenameBlock(model.ParsePath(op.Path), op.NewID)
		return s.put(op.File, nf, err)
	case "moveBlock":
		if op.Index == nil {
			return errors.New("moveBlock needs an index")
		}
		f, err := s.editable(op.File)
		if err != nil {
			return err
		}
		nf, err := f.MoveBlock(model.ParsePath(op.Path), *op.Index)
		return s.put(op.File, nf, err)
	case "addFile":
		if err := checkFileName(op.File); err != nil {
			return err
		}
		if _, ok := s[op.File]; ok {
			return fmt.Errorf("file %q already exists", op.File)
		}
		e, perr := openEntry(op.File, op.Content)
		if perr != nil {
			return fmt.Errorf("the content of %q is not valid BCL: %v", op.File, perr)
		}
		s[op.File] = e
		return nil
	case "renameFile":
		e, ok := s[op.File]
		if !ok {
			return fmt.Errorf("no file %q in the draft", op.File)
		}
		if err := checkFileName(op.NewFile); err != nil {
			return err
		}
		if _, ok := s[op.NewFile]; ok {
			return fmt.Errorf("file %q already exists", op.NewFile)
		}
		delete(s, op.File)
		ne, _ := openEntry(op.NewFile, e.text())
		s[op.NewFile] = ne
		return nil
	case "removeFile":
		if isAssetPath(op.File) {
			return fmt.Errorf("%q is an asset; use removeAsset", op.File)
		}
		if _, ok := s[op.File]; !ok {
			return fmt.Errorf("no file %q in the draft", op.File)
		}
		if len(s.names()) == 1 {
			return errors.New("a draft needs at least one file")
		}
		delete(s, op.File)
		return nil
	case "putAsset":
		// Create or replace a template or static file. File is its path
		// ("templates/pages/todos/list.html"); the whole asset set is
		// validated when the batch commits.
		if !isAssetPath(op.File) {
			return fmt.Errorf("%q is not an asset path (assets live under templates/ or static/)", op.File)
		}
		e, _ := openEntry(op.File, op.Content)
		s[op.File] = e
		return nil
	case "removeAsset":
		if !isAssetPath(op.File) {
			return fmt.Errorf("%q is not an asset path", op.File)
		}
		if _, ok := s[op.File]; !ok {
			return fmt.Errorf("no asset %q in the draft", op.File)
		}
		delete(s, op.File)
		return nil
	case "renameAsset":
		if !isAssetPath(op.File) || !isAssetPath(op.NewFile) {
			return errors.New("renameAsset needs two asset paths")
		}
		e, ok := s[op.File]
		if !ok {
			return fmt.Errorf("no asset %q in the draft", op.File)
		}
		if _, ok := s[op.NewFile]; ok {
			return fmt.Errorf("asset %q already exists", op.NewFile)
		}
		delete(s, op.File)
		s[op.NewFile] = e
		return nil
	default:
		return fmt.Errorf("unknown op %q", op.Op)
	}
}

func (s snapshot) put(name string, f *model.File, err error) error {
	if err != nil {
		return err
	}
	s[name] = newEntry(f, "", "")
	return nil
}

func checkFileName(name string) error {
	_, err := platform.NewBundle([]platform.BundleFile{{Path: name, Content: ""}})
	if err != nil {
		return fmt.Errorf("invalid file name: %w", trimPrefixErr(err))
	}
	return nil
}

func trimPrefixErr(err error) error {
	const p = "ref/platform: "
	msg := err.Error()
	if len(msg) > len(p) && msg[:len(p)] == p {
		return errors.New(msg[len(p):])
	}
	return err
}

// opError is a failed op within a batch.
type opError struct {
	Index int
	Op    string
	Err   error
}

func (e *opError) Error() string { return fmt.Sprintf("op %d (%s): %v", e.Index, e.Op, e.Err) }
func (e *opError) Unwrap() error { return e.Err }

// change describes the outcome of a committed (or dry-run) edit.
type change struct {
	version int64
	applied int
	changed []string
	state   snapshot
}

// mutate runs fn on a clone of the current state and, if it succeeds and
// changes anything, commits it atomically as a new version. dry runs fn but
// commits nothing. ifVersion, when set, must equal the current version.
func (d *Draft) mutate(now time.Time, ifVersion *int64, dry bool, fn func(snapshot) (applied int, err error)) (change, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ifVersion != nil && *ifVersion != d.version {
		return change{}, errStale(d.version)
	}
	next := maps.Clone(d.state)
	applied, err := fn(next)
	if err != nil {
		return change{}, err
	}
	changed := changedFiles(d.state, next)
	if len(changed) > 0 {
		if _, err := platform.NewBundle(next.bundle()); err != nil {
			return change{}, errf(http.StatusUnprocessableEntity, "invalid_bundle", "%s", trimPrefixErr(err).Error())
		}
		for _, name := range changed {
			if isAssetPath(name) {
				if _, err := platform.NewAssets(next.assets()); err != nil {
					return change{}, errf(http.StatusUnprocessableEntity, "invalid_assets", "%s", trimPrefixErr(err).Error())
				}
				break
			}
		}
	}
	out := change{version: d.version, applied: applied, changed: changed, state: d.state}
	if len(changed) == 0 || dry {
		if dry {
			out.state = next
		}
		return out, nil
	}
	d.past = append(d.past, d.state)
	if len(d.past) > maxHistory {
		d.past = append(d.past[:0], d.past[len(d.past)-maxHistory:]...)
	}
	d.future = nil
	d.commit(next, now)
	out.version, out.state = d.version, next
	return out, nil
}

// commit installs next as the current state. The caller holds d.mu.
func (d *Draft) commit(next snapshot, now time.Time) {
	d.state = next
	d.version++
	d.updated = now
}

// applyOps applies ops atomically.
func (d *Draft) applyOps(now time.Time, ifVersion *int64, ops []Op, dry bool) (change, error) {
	return d.mutate(now, ifVersion, dry, func(s snapshot) (int, error) {
		for i, op := range ops {
			if err := s.apply(op); err != nil {
				return 0, &opError{Index: i, Op: op.Op, Err: err}
			}
		}
		return len(ops), nil
	})
}

// replaceFile replaces one file's text wholesale.
func (d *Draft) replaceFile(now time.Time, ifVersion *int64, name, content string) (change, error) {
	return d.mutate(now, ifVersion, false, func(s snapshot) (int, error) {
		if _, ok := s[name]; !ok {
			return 0, errf(http.StatusNotFound, "not_found", "no file %q in the draft", name)
		}
		e, perr := openEntry(name, content)
		if perr != nil {
			return 0, &parseError{err: perr}
		}
		s[name] = e
		return 1, nil
	})
}

// parseError wraps a BCL syntax error in a submitted file.
type parseError struct{ err error }

func (e *parseError) Error() string { return e.err.Error() }
func (e *parseError) Unwrap() error { return e.err }

// reformat reformats every editable file.
func (d *Draft) reformat(now time.Time, ifVersion *int64) (change, error) {
	return d.mutate(now, ifVersion, false, func(s snapshot) (int, error) {
		n := 0
		for name, e := range s {
			if e.file == nil {
				continue
			}
			nf, err := e.file.Reformat()
			if err != nil {
				return 0, fmt.Errorf("format %s: %w", name, err)
			}
			if !nf.Equal(e.file) {
				s[name] = newEntry(nf, "", "")
				n++
			}
		}
		return n, nil
	})
}

// step moves through the undo history. redo selects the direction.
func (d *Draft) step(now time.Time, redo bool) (change, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	from := d.state
	if redo {
		if len(d.future) == 0 {
			return change{}, errf(http.StatusConflict, "nothing_to_redo", "nothing to redo")
		}
		d.past = append(d.past, d.state)
		d.state = d.future[len(d.future)-1]
		d.future = d.future[:len(d.future)-1]
	} else {
		if len(d.past) == 0 {
			return change{}, errf(http.StatusConflict, "nothing_to_undo", "nothing to undo")
		}
		d.future = append(d.future, d.state)
		d.state = d.past[len(d.past)-1]
		d.past = d.past[:len(d.past)-1]
	}
	d.version++
	d.updated = now
	return change{version: d.version, applied: 1, changed: changedFiles(from, d.state), state: d.state}, nil
}
