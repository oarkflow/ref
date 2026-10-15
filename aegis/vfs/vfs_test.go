package vfs_test

import (
	"archive/zip"
	"bytes"
	"io"
	"io/fs"
	"testing"

	"github.com/oarkflow/ref/aegis/vfs"
)

func TestMemFS_Basic(t *testing.T) {
	mem := vfs.New()
	mem.WriteFile("config/app.bcl", []byte("app { name = \"test\" }"), 0o644)
	mem.WriteFile("templates/index.html", []byte("<h1>Hello</h1>"), 0o644)

	// Stat
	fi, err := mem.Stat("config/app.bcl")
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if fi.Name() != "app.bcl" || fi.IsDir() {
		t.Errorf("unexpected FileInfo: %+v", fi)
	}

	// ReadFile
	data, err := mem.ReadFile("config/app.bcl")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "app { name = \"test\" }" {
		t.Errorf("unexpected content: %s", string(data))
	}

	// ReadDir
	entries, err := mem.ReadDir("config")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "app.bcl" {
		t.Errorf("unexpected entries: %+v", entries)
	}

	// Open & Read
	f, err := mem.Open("templates/index.html")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer f.Close()

	buf, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("io.ReadAll failed: %v", err)
	}
	if string(buf) != "<h1>Hello</h1>" {
		t.Errorf("unexpected content from Open: %s", string(buf))
	}

	// Sub
	tmplFS, err := mem.Sub("templates")
	if err != nil {
		t.Fatalf("Sub failed: %v", err)
	}
	subData, err := fs.ReadFile(tmplFS, "index.html")
	if err != nil {
		t.Fatalf("fs.ReadFile on Sub failed: %v", err)
	}
	if string(subData) != "<h1>Hello</h1>" {
		t.Errorf("unexpected sub data: %s", string(subData))
	}

	// ListAll
	all := mem.ListAll()
	if len(all) != 2 {
		t.Errorf("expected 2 files, got %d (%v)", len(all), all)
	}

	// Wipe
	mem.Wipe()
	if len(mem.ListAll()) != 0 {
		t.Errorf("expected 0 files after wipe")
	}
}

func TestMemFS_FromZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	w, err := zw.Create("config/rules.bcl")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("rule \"r1\" {}"))

	w2, err := zw.Create("static/main.css")
	if err != nil {
		t.Fatal(err)
	}
	w2.Write([]byte("body { background: black; }"))

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	mem, err := vfs.FromZip(buf.Bytes())
	if err != nil {
		t.Fatalf("FromZip failed: %v", err)
	}

	rData, err := mem.ReadFile("config/rules.bcl")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(rData) != "rule \"r1\" {}" {
		t.Errorf("unexpected content: %s", string(rData))
	}

	sData, err := mem.ReadFile("static/main.css")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(sData) != "body { background: black; }" {
		t.Errorf("unexpected content: %s", string(sData))
	}
}

func TestMemFS_TraversalProtection(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	w, err := zw.Create("../../etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("root:x:0:0:..."))

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = vfs.FromZip(buf.Bytes())
	if err == nil {
		t.Fatal("expected traversal attack to be rejected, but it succeeded")
	}
}

func TestSubIsViewAndWipeReachesIt(t *testing.T) {
	m := vfs.New()
	m.WriteFile("a/b.txt", []byte("secret"), 0o644)
	sub, err := m.Sub("a")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := fs.ReadFile(sub, "b.txt"); err != nil || string(b) != "secret" {
		t.Fatalf("read via sub: %q %v", b, err)
	}
	m.Wipe()
	if _, err := fs.ReadFile(sub, "b.txt"); err == nil {
		t.Fatal("sub view still readable after Wipe")
	}
}
