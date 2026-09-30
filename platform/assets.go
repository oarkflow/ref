package platform

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Asset limits. Assets are page templates and small static files that travel
// with a revision, not an object store.
const (
	MaxAssetFiles     = 1024
	MaxAssetFileBytes = 1 << 20
	MaxAssetBytes     = 8 << 20
)

// AssetRoots are the only top-level directories an asset may live under:
// templates are rendered by the host's template engine, static files are
// served as they are.
var AssetRoots = []string{"templates", "static"}

// assetExts is the extension allowlist. Assets are text; BCL is a bundle
// file, never an asset.
var assetExts = map[string]bool{".html": true, ".css": true, ".js": true, ".json": true, ".txt": true, ".svg": true}

// Assets is the non-BCL part of an application that a revision carries: page
// templates and static files, sorted by path. NewAssets builds a valid one.
// Each path is slash-separated and relative to the application's resources
// directory ("templates/pages/todos/list.html"), so an Assets value overlays
// the same tree a host serves from disk.
type Assets []BundleFile

// NewAssets validates files and returns them as sorted Assets (the input is
// not modified). No files is valid and yields nil. A path must be clean and
// relative, under "templates/" or "static/", have an allowlisted extension
// (.html .css .js .json .txt .svg), appear once, and not also be a directory
// of another asset; content must be UTF-8 text without NUL bytes.
func NewAssets(files []BundleFile) (Assets, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if len(files) > MaxAssetFiles {
		return nil, fmt.Errorf("ref/platform: a revision holds at most %d assets (got %d)", MaxAssetFiles, len(files))
	}
	a := make(Assets, len(files))
	copy(a, files)
	sort.Slice(a, func(i, j int) bool { return a[i].Path < a[j].Path })
	total := 0
	for i, f := range a {
		if err := validAssetPath(f.Path); err != nil {
			return nil, err
		}
		if i > 0 {
			prev := a[i-1].Path
			if prev == f.Path {
				return nil, fmt.Errorf("ref/platform: asset path %q appears twice", f.Path)
			}
			if strings.HasPrefix(f.Path, prev+"/") {
				return nil, fmt.Errorf("ref/platform: asset path %q is a file, so %q cannot be inside it", prev, f.Path)
			}
		}
		if len(f.Content) > MaxAssetFileBytes {
			return nil, fmt.Errorf("ref/platform: asset %q is %d bytes; the limit is %d", f.Path, len(f.Content), MaxAssetFileBytes)
		}
		if !utf8.ValidString(f.Content) || strings.IndexByte(f.Content, 0) >= 0 {
			return nil, fmt.Errorf("ref/platform: asset %q is not UTF-8 text", f.Path)
		}
		total += len(f.Content)
		if total > MaxAssetBytes {
			return nil, fmt.Errorf("ref/platform: a revision's assets total at most %d bytes", MaxAssetBytes)
		}
	}
	return a, nil
}

func validAssetPath(p string) error {
	bad := func(why string) error { return fmt.Errorf("ref/platform: asset path %q %s", p, why) }
	switch {
	case p == "":
		return errors.New("ref/platform: asset path is empty")
	case !utf8.ValidString(p):
		return bad("is not valid UTF-8")
	case strings.Contains(p, `\`):
		return bad("must use forward slashes")
	case strings.HasPrefix(p, "/"):
		return bad("must be relative")
	case path.Clean(p) != p:
		return bad(`must be clean (no ".", "..", empty segments or trailing slash)`)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return bad("has a control character")
		}
	}
	root, rest, ok := strings.Cut(p, "/")
	if !ok || rest == "" {
		return bad(`must be inside "templates/" or "static/"`)
	}
	found := false
	for _, r := range AssetRoots {
		if root == r {
			found = true
		}
	}
	if !found {
		return bad(`must be inside "templates/" or "static/"`)
	}
	for _, seg := range strings.Split(rest, "/") {
		if strings.HasPrefix(seg, ".") {
			return bad("has a hidden path segment")
		}
	}
	if !assetExts[strings.ToLower(path.Ext(p))] {
		return bad("has an extension outside .html .css .js .json .txt .svg")
	}
	return nil
}

// Get returns the content of one asset.
func (a Assets) Get(p string) (string, bool) {
	i := sort.Search(len(a), func(i int) bool { return a[i].Path >= p })
	if i < len(a) && a[i].Path == p {
		return a[i].Content, true
	}
	return "", false
}

// Paths lists the asset paths in order.
func (a Assets) Paths() []string {
	out := make([]string, len(a))
	for i, f := range a {
		out[i] = f.Path
	}
	return out
}

// FS is a read-only file system holding just these assets.
func (a Assets) FS() fs.FS { return a.Overlay(nil) }

// Overlay layers the assets over base: a file the assets define wins, every
// other file (and directory listing) comes from base. A nil base yields only
// the assets. It is how a host reads a revision's templates while falling
// back, file by file, to the ones on disk.
func (a Assets) Overlay(base fs.FS) fs.FS {
	o := &overlayFS{base: base, files: make(map[string]string, len(a)), dirs: map[string]map[string]bool{}}
	for _, f := range a {
		o.files[f.Path] = f.Content
		for child, dir := f.Path, path.Dir(f.Path); ; child, dir = dir, path.Dir(dir) {
			if o.dirs[dir] == nil {
				o.dirs[dir] = map[string]bool{}
			}
			o.dirs[dir][path.Base(child)] = true
			if dir == "." {
				break
			}
		}
	}
	return o
}

type overlayFS struct {
	base  fs.FS
	files map[string]string
	// dirs maps a directory ("." for the root) to the names an asset puts
	// directly inside it.
	dirs map[string]map[string]bool
}

func (o *overlayFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if content, ok := o.files[name]; ok {
		return &memFile{name: path.Base(name), r: bytes.NewReader([]byte(content)), size: int64(len(content))}, nil
	}
	if _, ok := o.dirs[name]; ok {
		entries, err := o.readDir(name)
		if err != nil {
			return nil, err
		}
		return &memDir{name: path.Base(name), entries: entries}, nil
	}
	if o.base != nil {
		return o.base.Open(name)
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// ReadDir merges the assets' entries into base's listing of the same
// directory.
func (o *overlayFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	if _, ok := o.dirs[name]; !ok {
		if o.base != nil {
			return fs.ReadDir(o.base, name)
		}
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	return o.readDir(name)
}

func (o *overlayFS) readDir(name string) ([]fs.DirEntry, error) {
	byName := map[string]fs.DirEntry{}
	if o.base != nil {
		if entries, err := fs.ReadDir(o.base, name); err == nil {
			for _, e := range entries {
				byName[e.Name()] = e
			}
		}
	}
	for child := range o.dirs[name] {
		full := path.Join(name, child)
		if name == "." {
			full = child
		}
		if content, ok := o.files[full]; ok {
			byName[child] = fs.FileInfoToDirEntry(memInfo{name: child, size: int64(len(content))})
		} else {
			byName[child] = fs.FileInfoToDirEntry(memInfo{name: child, dir: true})
		}
	}
	out := make([]fs.DirEntry, 0, len(byName))
	for _, e := range byName {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// memInfo is the fs.FileInfo of an asset or a directory an asset implies.
type memInfo struct {
	name string
	size int64
	dir  bool
}

func (i memInfo) Name() string { return i.name }
func (i memInfo) Size() int64  { return i.size }
func (i memInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return i.dir }
func (i memInfo) Sys() any           { return nil }

type memFile struct {
	name string
	r    *bytes.Reader
	size int64
}

func (f *memFile) Stat() (fs.FileInfo, error)         { return memInfo{name: f.name, size: f.size}, nil }
func (f *memFile) Read(p []byte) (int, error)         { return f.r.Read(p) }
func (f *memFile) Seek(o int64, w int) (int64, error) { return f.r.Seek(o, w) }
func (f *memFile) Close() error                       { return nil }

type memDir struct {
	name    string
	entries []fs.DirEntry
	off     int
}

func (d *memDir) Stat() (fs.FileInfo, error) { return memInfo{name: d.name, dir: true}, nil }
func (d *memDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: errors.New("is a directory")}
}
func (d *memDir) Close() error { return nil }
func (d *memDir) ReadDir(n int) ([]fs.DirEntry, error) {
	rest := d.entries[d.off:]
	if n <= 0 {
		d.off = len(d.entries)
		return rest, nil
	}
	if len(rest) == 0 {
		return nil, io.EOF
	}
	if n > len(rest) {
		n = len(rest)
	}
	d.off += n
	return rest[:n], nil
}
