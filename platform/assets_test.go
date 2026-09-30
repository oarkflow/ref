package platform

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestNewAssetsValidation(t *testing.T) {
	ok := []BundleFile{
		{Path: "templates/pages/a.html", Content: "<p>a</p>"},
		{Path: "static/css/app.css", Content: "body{}"},
		{Path: "static/img/logo.svg", Content: "<svg/>"},
	}
	a, err := NewAssets(ok)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.Paths(), ","); got != "static/css/app.css,static/img/logo.svg,templates/pages/a.html" {
		t.Fatalf("not sorted: %s", got)
	}
	if ok[0].Path != "templates/pages/a.html" {
		t.Fatal("input reordered")
	}
	if a, err := NewAssets(nil); a != nil || err != nil {
		t.Fatalf("no assets is valid: %v %v", a, err)
	}
	tooBig := strings.Repeat("x", MaxAssetFileBytes+1)
	cases := map[string][]BundleFile{
		"empty path":     {{Path: "", Content: "x"}},
		"absolute":       {{Path: "/templates/a.html"}},
		"dotdot":         {{Path: "templates/../a.html"}},
		"unclean":        {{Path: "templates//a.html"}},
		"trailing slash": {{Path: "templates/a.html/"}},
		"backslash":      {{Path: `templates\a.html`}},
		"wrong root":     {{Path: "config/a.html"}},
		"root only":      {{Path: "templates"}},
		"root file":      {{Path: "templates/"}},
		"bcl":            {{Path: "templates/a.bcl", Content: "x 1"}},
		"bcl in root":    {{Path: "04_routes.bcl", Content: "x 1"}},
		"exe":            {{Path: "static/a.exe", Content: "x"}},
		"no ext":         {{Path: "templates/a"}},
		"hidden":         {{Path: "templates/.secret.html"}},
		"hidden dir":     {{Path: "static/.git/x.txt"}},
		"control char":   {{Path: "templates/a\n.html"}},
		"dup":            {{Path: "templates/a.html"}, {Path: "templates/a.html"}},
		"file and dir":   {{Path: "templates/a.html"}, {Path: "templates/a.html/b.html"}},
		"nul":            {{Path: "static/a.txt", Content: "a\x00b"}},
		"bad utf8":       {{Path: "static/a.txt", Content: "\xff\xfe"}},
		"file too big":   {{Path: "static/a.txt", Content: tooBig}},
		"upper root":     {{Path: "Templates/a.html"}},
	}
	for name, files := range cases {
		if _, err := NewAssets(files); err == nil {
			t.Errorf("%s: accepted %+v", name, files)
		}
	}
	// Extension check is case-insensitive.
	if _, err := NewAssets([]BundleFile{{Path: "static/A.CSS", Content: "x"}}); err != nil {
		t.Errorf("upper-case extension: %v", err)
	}
	// Too many files, too many bytes.
	var many []BundleFile
	for i := 0; i <= MaxAssetFiles; i++ {
		many = append(many, BundleFile{Path: "static/f" + strings.Repeat("a", 1) + itoa(i) + ".txt"})
	}
	if _, err := NewAssets(many); err == nil {
		t.Error("accepted too many assets")
	}
	var big []BundleFile
	for i := 0; i < 9; i++ {
		big = append(big, BundleFile{Path: "static/big" + itoa(i) + ".txt", Content: strings.Repeat("x", MaxAssetFileBytes)})
	}
	if _, err := NewAssets(big); err == nil {
		t.Error("accepted assets over the total limit")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func TestAssetsFSAndOverlay(t *testing.T) {
	a, err := NewAssets([]BundleFile{
		{Path: "templates/pages/todos/list.html", Content: "NEW LIST"},
		{Path: "templates/components/alert.html", Content: "NEW ALERT"},
		{Path: "static/css/extra.css", Content: "x{}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fstest.TestFS(a.FS(), a.Paths()...); err != nil {
		t.Fatalf("assets-only fs: %v", err)
	}
	base := fstest.MapFS{
		"templates/pages/todos/list.html":   {Data: []byte("OLD LIST")},
		"templates/pages/todos/detail.html": {Data: []byte("OLD DETAIL")},
		"templates/layouts/base.html":       {Data: []byte("BASE")},
		"static/js/app.js":                  {Data: []byte("js")},
	}
	o := a.Overlay(base)
	want := append(a.Paths(), "templates/pages/todos/detail.html", "templates/layouts/base.html", "static/js/app.js")
	if err := fstest.TestFS(o, want...); err != nil {
		t.Fatalf("overlay fs: %v", err)
	}
	read := func(p string) string {
		b, err := fs.ReadFile(o, p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		return string(b)
	}
	if read("templates/pages/todos/list.html") != "NEW LIST" {
		t.Error("asset should win over base")
	}
	if read("templates/pages/todos/detail.html") != "OLD DETAIL" || read("templates/layouts/base.html") != "BASE" {
		t.Error("untouched base files should fall through")
	}
	// A merged directory lists both.
	ents, err := fs.ReadDir(o, "templates/pages/todos")
	if err != nil || len(ents) != 2 {
		t.Fatalf("merged listing: %v %v", ents, err)
	}
	if _, err := fs.ReadFile(o, "templates/nope.html"); err == nil {
		t.Error("missing file should fail")
	}
	if _, err := fs.ReadFile(a.FS(), "templates/layouts/base.html"); err == nil {
		t.Error("assets-only fs must not see base files")
	}
	if _, err := o.Open("../x"); err == nil {
		t.Error("invalid path accepted")
	}
	if got, ok := a.Get("static/css/extra.css"); !ok || got != "x{}" {
		t.Errorf("Get: %q %v", got, ok)
	}
	if _, ok := a.Get("static/css/none.css"); ok {
		t.Error("Get found a missing asset")
	}
}
