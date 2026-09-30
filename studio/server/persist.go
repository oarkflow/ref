package server

import (
	"fmt"
	"sort"
	"time"

	"github.com/oarkflow/ref/platform"
)

// draftRecord is a draft as a SQL store keeps it: metadata, plus the state
// and each undo/redo step as maps from file name to content hash. The
// contents themselves are stored once per distinct hash (draftBlobs), so 200
// undo steps of a 14-file bundle cost a few hundred small strings rather than
// 200 copies of the bundle.
type draftRecord struct {
	ID        string              `json:"id"`
	Owner     string              `json:"owner"`
	Name      string              `json:"name"`
	Base      string              `json:"base,omitempty"`
	Created   time.Time           `json:"created"`
	Updated   time.Time           `json:"updated"`
	Version   int64               `json:"version"`
	State     map[string]string   `json:"state"`
	Past      []map[string]string `json:"past,omitempty"`
	Future    []map[string]string `json:"future,omitempty"`
	BaseFiles map[string]string   `json:"base_files"`
}

// exported is what a store needs to write a draft.
type exported struct {
	rec draftRecord
	// needed is every content hash the record refers to.
	needed map[string]bool
	// fresh holds the contents of the hashes not in the caller's known set.
	fresh map[string]string
}

// export snapshots the draft for persistence. known is the set of hashes the
// store already holds for this draft; only the others are copied out.
func (d *Draft) export(known map[string]bool) exported {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := exported{needed: map[string]bool{}, fresh: map[string]string{}}
	hashSnap := func(s snapshot) map[string]string {
		m := make(map[string]string, len(s))
		for name, e := range s {
			h := e.hash()
			m[name] = h
			out.needed[h] = true
			if !known[h] {
				if _, ok := out.fresh[h]; !ok {
					out.fresh[h] = e.text()
				}
			}
		}
		return m
	}
	rec := draftRecord{ID: d.id, Owner: d.owner, Name: d.name, Base: d.base, Created: d.created, Updated: d.updated,
		Version: d.version, State: hashSnap(d.state), BaseFiles: make(map[string]string, len(d.baseFiles))}
	for _, s := range d.past {
		rec.Past = append(rec.Past, hashSnap(s))
	}
	for _, s := range d.future {
		rec.Future = append(rec.Future, hashSnap(s))
	}
	// Base assets share the map: their paths contain a slash, BCL names do not.
	for _, list := range [][]platform.BundleFile{d.baseFiles, d.baseAssets} {
		for _, f := range list {
			h := hashText(f.Content)
			rec.BaseFiles[f.Path] = h
			out.needed[h] = true
			if !known[h] {
				out.fresh[h] = f.Content
			}
		}
	}
	out.rec = rec
	return out
}

// restoreDraft rebuilds a draft from its record and the contents of its
// hashes.
func restoreDraft(rec draftRecord, blobs map[string]string) (*Draft, error) {
	type key struct{ name, hash string }
	cache := map[key]entry{}
	snap := func(m map[string]string) (snapshot, error) {
		s := make(snapshot, len(m))
		for name, h := range m {
			content, ok := blobs[h]
			if !ok {
				return nil, fmt.Errorf("draft %s: content %s of %s is missing", rec.ID, h, name)
			}
			e, ok := cache[key{name, h}]
			if !ok {
				e, _ = openEntry(name, content)
				cache[key{name, h}] = e
			}
			s[name] = e
		}
		return s, nil
	}
	state, err := snap(rec.State)
	if err != nil {
		return nil, err
	}
	d := &Draft{
		id: rec.ID, owner: rec.Owner, name: rec.Name, base: rec.Base, created: rec.Created, updated: rec.Updated,
		version: rec.Version, state: state, subs: map[int]chan event{},
	}
	for _, m := range rec.Past {
		s, err := snap(m)
		if err != nil {
			return nil, err
		}
		d.past = append(d.past, s)
	}
	for _, m := range rec.Future {
		s, err := snap(m)
		if err != nil {
			return nil, err
		}
		d.future = append(d.future, s)
	}
	for name, h := range rec.BaseFiles {
		content, ok := blobs[h]
		if !ok {
			return nil, fmt.Errorf("draft %s: base content %s of %s is missing", rec.ID, h, name)
		}
		f := platform.BundleFile{Path: name, Content: content}
		if isAssetPath(name) {
			d.baseAssets = append(d.baseAssets, f)
		} else {
			d.baseFiles = append(d.baseFiles, f)
		}
	}
	sort.Slice(d.baseFiles, func(i, j int) bool { return d.baseFiles[i].Path < d.baseFiles[j].Path })
	sort.Slice(d.baseAssets, func(i, j int) bool { return d.baseAssets[i].Path < d.baseAssets[j].Path })
	return d, nil
}
