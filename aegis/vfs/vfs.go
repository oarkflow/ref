package vfs

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemFS is a thread-safe, purely in-memory virtual filesystem implementing
// fs.FS, fs.ReadFileFS, fs.ReadDirFS, fs.StatFS, and fs.SubFS.
// It is designed for hosting decrypted application bundles without leaving
// any traces on the host operating system's disk.
type MemFS struct {
	mu      sync.RWMutex
	files   map[string]*memFile
	dirs    map[string]map[string]bool
	modTime time.Time
}

type memFile struct {
	name    string
	data    []byte
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

// New creates an empty in-memory filesystem.
func New() *MemFS {
	m := &MemFS{
		files:   make(map[string]*memFile),
		dirs:    make(map[string]map[string]bool),
		modTime: time.Now(),
	}
	// Root directory "."
	m.dirs["."] = make(map[string]bool)
	m.files["."] = &memFile{
		name:    ".",
		mode:    fs.ModeDir | 0o755,
		modTime: m.modTime,
		isDir:   true,
	}
	return m
}

// FromZip builds an in-memory virtual filesystem from raw ZIP byte data.
// No files are extracted to disk; all contents remain resident exclusively in RAM.
func FromZip(zipBytes []byte) (*MemFS, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("aegis/vfs: invalid zip container: %w", err)
	}

	m := New()

	for _, f := range zr.File {
		cleanPath := cleanVFSPath(f.Name)
		if cleanPath == "" || cleanPath == "." {
			continue
		}

		// Security: prevent path traversal out of the virtual root
		if strings.HasPrefix(cleanPath, "../") || cleanPath == ".." || strings.Contains(cleanPath, "/../") {
			return nil, fmt.Errorf("aegis/vfs: illegal path traversal in container: %q", f.Name)
		}

		info := f.FileInfo()
		if info.IsDir() {
			m.MkdirAll(cleanPath, info.Mode())
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("aegis/vfs: read zip entry %q: %w", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("aegis/vfs: decompress zip entry %q: %w", f.Name, err)
		}

		m.WriteFile(cleanPath, data, info.Mode())
	}

	return m, nil
}

// MkdirAll creates directory and all parents in the virtual filesystem.
func (m *MemFS) MkdirAll(dirPath string, mode fs.FileMode) {
	m.mu.Lock()
	defer m.mu.Unlock()

	clean := cleanVFSPath(dirPath)
	if clean == "" || clean == "." {
		return
	}

	parts := strings.Split(clean, "/")
	current := ""
	for i, part := range parts {
		parent := "."
		if current != "" {
			parent = current
		}
		if i == 0 {
			current = part
		} else {
			current = current + "/" + part
		}

		if m.dirs[parent] == nil {
			m.dirs[parent] = make(map[string]bool)
		}
		m.dirs[parent][part] = true

		if m.dirs[current] == nil {
			m.dirs[current] = make(map[string]bool)
		}
		if m.files[current] == nil {
			m.files[current] = &memFile{
				name:    part,
				mode:    mode | fs.ModeDir,
				modTime: m.modTime,
				isDir:   true,
			}
		}
	}
}

// WriteFile writes a file at the specified virtual path.
func (m *MemFS) WriteFile(filePath string, data []byte, mode fs.FileMode) {
	clean := cleanVFSPath(filePath)
	if clean == "" || clean == "." {
		return
	}

	dir := path.Dir(clean)
	base := path.Base(clean)

	m.MkdirAll(dir, 0o755)

	m.mu.Lock()
	defer m.mu.Unlock()

	parent := dir
	if parent == "" {
		parent = "."
	}
	if m.dirs[parent] == nil {
		m.dirs[parent] = make(map[string]bool)
	}
	m.dirs[parent][base] = true

	// Copy data so caller cannot mutate our internal buffer
	buf := make([]byte, len(data))
	copy(buf, data)

	m.files[clean] = &memFile{
		name:    base,
		data:    buf,
		mode:    mode,
		modTime: time.Now(),
		isDir:   false,
	}
}

// Open opens a named file or directory for reading.
func (m *MemFS) Open(name string) (fs.File, error) {
	clean := cleanVFSPath(name)
	m.mu.RLock()
	defer m.mu.RUnlock()

	file, ok := m.files[clean]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}

	if file.isDir {
		childNames := make([]string, 0, len(m.dirs[clean]))
		for c := range m.dirs[clean] {
			childNames = append(childNames, c)
		}
		sort.Strings(childNames)

		entries := make([]fs.DirEntry, len(childNames))
		for i, c := range childNames {
			childPath := c
			if clean != "." {
				childPath = clean + "/" + c
			}
			cf := m.files[childPath]
			if cf == nil {
				// Directory node without explicit file
				entries[i] = &memDirEntry{
					name:  c,
					isDir: true,
					mode:  fs.ModeDir | 0o755,
				}
			} else {
				entries[i] = &memDirEntry{
					name:    cf.name,
					isDir:   cf.isDir,
					mode:    cf.mode,
					size:    int64(len(cf.data)),
					modTime: cf.modTime,
				}
			}
		}

		return &memDirHandle{
			info:    file.fileInfo(),
			entries: entries,
		}, nil
	}

	return &memFileHandle{
		info:   file.fileInfo(),
		reader: bytes.NewReader(file.data),
	}, nil
}

// ReadFile reads the named file and returns its content.
func (m *MemFS) ReadFile(name string) ([]byte, error) {
	clean := cleanVFSPath(name)
	m.mu.RLock()
	defer m.mu.RUnlock()

	file, ok := m.files[clean]
	if !ok {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrNotExist}
	}
	if file.isDir {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: errors.New("is a directory")}
	}

	out := make([]byte, len(file.data))
	copy(out, file.data)
	return out, nil
}

// ReadDir reads the named directory and returns a list of directory entries.
func (m *MemFS) ReadDir(name string) ([]fs.DirEntry, error) {
	clean := cleanVFSPath(name)
	m.mu.RLock()
	defer m.mu.RUnlock()

	file, ok := m.files[clean]
	if !ok || !file.isDir {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}

	childNames := make([]string, 0, len(m.dirs[clean]))
	for c := range m.dirs[clean] {
		childNames = append(childNames, c)
	}
	sort.Strings(childNames)

	entries := make([]fs.DirEntry, len(childNames))
	for i, c := range childNames {
		childPath := c
		if clean != "." {
			childPath = clean + "/" + c
		}
		cf := m.files[childPath]
		if cf == nil {
			entries[i] = &memDirEntry{
				name:  c,
				isDir: true,
				mode:  fs.ModeDir | 0o755,
			}
		} else {
			entries[i] = &memDirEntry{
				name:    cf.name,
				isDir:   cf.isDir,
				mode:    cf.mode,
				size:    int64(len(cf.data)),
				modTime: cf.modTime,
			}
		}
	}
	return entries, nil
}

// Stat returns a FileInfo describing the named file.
func (m *MemFS) Stat(name string) (fs.FileInfo, error) {
	clean := cleanVFSPath(name)
	m.mu.RLock()
	defer m.mu.RUnlock()

	file, ok := m.files[clean]
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return file.fileInfo(), nil
}

// Sub returns an fs.FS corresponding to the subtree rooted at dir.
func (m *MemFS) Sub(dir string) (fs.FS, error) {
	clean := cleanVFSPath(dir)
	if clean == "." || clean == "" {
		return m, nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	file, ok := m.files[clean]
	if !ok || !file.isDir {
		return nil, &fs.PathError{Op: "sub", Path: dir, Err: fs.ErrNotExist}
	}

	// A view over the parent, not a copy: a copy would hold decrypted bytes
	// that Wipe on the parent never reaches.
	return &subView{m: m, prefix: clean}, nil
}

// ListAll returns all file paths stored in the VFS.
func (m *MemFS) ListAll() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var list []string
	for k, f := range m.files {
		if !f.isDir && k != "." {
			list = append(list, k)
		}
	}
	sort.Strings(list)
	return list
}

// Wipe cryptographically zeroizes all decrypted file content in memory.
// Should be called on graceful shutdown to prevent cold-boot memory dumps.
func (m *MemFS) Wipe() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, f := range m.files {
		for i := range f.data {
			f.data[i] = 0
		}
		f.data = nil
	}
	m.files = make(map[string]*memFile)
	m.dirs = make(map[string]map[string]bool)
}

func cleanVFSPath(p string) string {
	p = strings.TrimPrefix(p, "/")
	p = path.Clean(p)
	if p == "" || p == "/" {
		return "."
	}
	return p
}

func (f *memFile) fileInfo() fs.FileInfo {
	return &memFileInfo{
		name:    f.name,
		size:    int64(len(f.data)),
		mode:    f.mode,
		modTime: f.modTime,
		isDir:   f.isDir,
	}
}

type memFileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

func (fi *memFileInfo) Name() string       { return fi.name }
func (fi *memFileInfo) Size() int64        { return fi.size }
func (fi *memFileInfo) Mode() fs.FileMode  { return fi.mode }
func (fi *memFileInfo) ModTime() time.Time { return fi.modTime }
func (fi *memFileInfo) IsDir() bool        { return fi.isDir }
func (fi *memFileInfo) Sys() any           { return nil }

type memDirEntry struct {
	name    string
	isDir   bool
	mode    fs.FileMode
	size    int64
	modTime time.Time
}

func (de *memDirEntry) Name() string      { return de.name }
func (de *memDirEntry) IsDir() bool       { return de.isDir }
func (de *memDirEntry) Type() fs.FileMode { return de.mode.Type() }
func (de *memDirEntry) Info() (fs.FileInfo, error) {
	return &memFileInfo{
		name:    de.name,
		size:    de.size,
		mode:    de.mode,
		modTime: de.modTime,
		isDir:   de.isDir,
	}, nil
}

type memFileHandle struct {
	info   fs.FileInfo
	reader *bytes.Reader
}

func (h *memFileHandle) Stat() (fs.FileInfo, error) { return h.info, nil }
func (h *memFileHandle) Read(p []byte) (int, error) { return h.reader.Read(p) }
func (h *memFileHandle) Seek(offset int64, whence int) (int64, error) {
	return h.reader.Seek(offset, whence)
}
func (h *memFileHandle) Close() error { return nil }

type memDirHandle struct {
	info    fs.FileInfo
	entries []fs.DirEntry
	offset  int
}

func (h *memDirHandle) Stat() (fs.FileInfo, error) { return h.info, nil }
func (h *memDirHandle) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: h.info.Name(), Err: errors.New("is a directory")}
}
func (h *memDirHandle) Close() error { return nil }

func (h *memDirHandle) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 {
		left := h.entries[h.offset:]
		h.offset = len(h.entries)
		return left, nil
	}
	if h.offset >= len(h.entries) {
		return nil, io.EOF
	}
	end := h.offset + n
	if end > len(h.entries) {
		end = len(h.entries)
	}
	res := h.entries[h.offset:end]
	h.offset = end
	return res, nil
}

// subView exposes the subtree of a MemFS without duplicating its contents.
type subView struct {
	m      *MemFS
	prefix string
}

func (v *subView) full(name string) string {
	c := cleanVFSPath(name)
	if c == "." {
		return v.prefix
	}
	return v.prefix + "/" + c
}

func (v *subView) Open(name string) (fs.File, error)          { return v.m.Open(v.full(name)) }
func (v *subView) ReadFile(name string) ([]byte, error)       { return v.m.ReadFile(v.full(name)) }
func (v *subView) ReadDir(name string) ([]fs.DirEntry, error) { return v.m.ReadDir(v.full(name)) }
func (v *subView) Stat(name string) (fs.FileInfo, error)      { return v.m.Stat(v.full(name)) }
func (v *subView) Sub(dir string) (fs.FS, error)              { return v.m.Sub(v.full(dir)) }
