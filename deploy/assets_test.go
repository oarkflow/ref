package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oarkflow/fh"

	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/signing"
)

func testAssets(tag string) []platform.BundleFile {
	return []platform.BundleFile{
		{Path: "templates/pages/todos/list.html", Content: "<h1>" + tag + " list</h1>\n"},
		{Path: "static/css/extra.css", Content: "body{color:" + tag + "}\n"},
	}
}

// TestV2RevisionStillVerifies pins a bundle revision (no assets) signed by the
// code before assets existed: HMAC and Ed25519, approved by a reviewer. Its
// checksum, payloads and signatures must not change.
func TestV2RevisionStillVerifies(t *testing.T) {
	raw, err := os.ReadFile("testdata/v2_revision.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Secret          string          `json:"secret"`
		JWKS            json.RawMessage `json:"jwks"`
		Revision        Revision        `json:"revision"`
		SigningPayload  string          `json:"signing_payload"`
		ApprovalPayload string          `json:"approval_payload"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	r := &fx.Revision
	if !r.IsBundle() || r.HasAssets() || len(r.Files) != 3 {
		t.Fatalf("fixture should be a v2 bundle: %+v", r.Files)
	}
	if string(SigningPayload(r)) != fx.SigningPayload || string(ApprovalPayload(r)) != fx.ApprovalPayload {
		t.Fatalf("payloads changed:\n%s\n%s", SigningPayload(r), ApprovalPayload(r))
	}
	if !strings.HasPrefix(fx.SigningPayload, "ref-revision/v2|") || !strings.HasPrefix(fx.ApprovalPayload, "ref-revision-approval/v2|") {
		t.Fatalf("fixture is not v2: %s", fx.SigningPayload)
	}
	if BundleChecksum(r.Files) != r.Checksum {
		t.Fatal("v2 checksum changed")
	}
	public, err := signing.ParseJWKS(fx.JWKS)
	if err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]*Manager{
		"hmac":  {App: "hello", Secret: []byte(fx.Secret), Approvals: 1},
		"key":   {App: "hello", Verifier: public, Approvals: 1},
		"both":  {App: "hello", Secret: []byte(fx.Secret), Verifier: public, Approvals: 1},
		"plain": {App: "hello", Approvals: 1},
	} {
		if err := m.VerifyActivation(r); err != nil {
			t.Errorf("%s: v2 revision no longer verifies: %v", name, err)
		}
	}
	// Attaching assets to it is caught, even with the checksum recomputed.
	forged := clone(r)
	forged.Assets = testAssets("evil")
	hm := &Manager{App: "hello", Secret: []byte(fx.Secret)}
	if err := hm.Verify(forged); !errors.Is(err, ErrTampered) {
		t.Errorf("assets attached to a v2 revision: %v", err)
	}
	forged.Checksum = AssetsChecksum(forged.Files, forged.Assets)
	if err := hm.Verify(forged); !errors.Is(err, ErrTampered) {
		t.Errorf("assets attached with a recomputed checksum: %v", err)
	}
}

func TestAssetsRevisionLifecycle(t *testing.T) {
	ctx := context.Background()
	key, _ := signing.GenerateEd25519("assets-key")
	keys, _ := signing.NewKeySet(key)
	for name, configure := range map[string]func(*Manager){
		"hmac": func(*Manager) {},
		"key":  func(m *Manager) { m.Secret, m.Signer = nil, keys },
		"both": func(m *Manager) { m.Signer = keys },
	} {
		t.Run(name, func(t *testing.T) {
			m := newBundleManager(NewMemoryStore())
			configure(m)
			in := testAssets("v1")
			r, err := m.ProposeBundleAssets(ctx, bundleFiles("v1"), in, "alice", "with assets")
			if err != nil {
				t.Fatal(err)
			}
			if !r.HasAssets() || len(r.Assets) != 2 || r.Assets[0].Path != "static/css/extra.css" {
				t.Fatalf("assets not sorted/kept: %+v", r.Assets)
			}
			if in[0].Path != "templates/pages/todos/list.html" {
				t.Fatal("input reordered")
			}
			if r.Checksum != AssetsChecksum(r.Files, r.Assets) || r.Checksum == BundleChecksum(r.Files) {
				t.Fatal("a v3 checksum must cover the assets")
			}
			if !strings.HasPrefix(string(SigningPayload(r)), "ref-revision/v3|") ||
				!strings.HasPrefix(string(ApprovalPayload(r)), "ref-revision-approval/v3|") {
				t.Fatalf("payloads: %s / %s", SigningPayload(r), ApprovalPayload(r))
			}
			if fmt.Sprint(r.ChangedAssets) != "[{static/css/extra.css added} {templates/pages/todos/list.html added}]" {
				t.Fatalf("changed assets: %v", r.ChangedAssets)
			}
			if _, err := m.Approve(ctx, r.ID, "bob", ""); err != nil {
				t.Fatal(err)
			}
			act, err := m.Activate(ctx, r.ID, "bob")
			if err != nil || act.Status != StatusActive {
				t.Fatalf("activate: %v", err)
			}
			// Only one asset changes in the next revision.
			next := testAssets("v1")
			next[0].Content = "<h1>changed</h1>\n"
			r2, err := m.ProposeBundleAssets(ctx, bundleFiles("v1"), next, "alice", "")
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(r2.ChangedAssets) != "[{templates/pages/todos/list.html modified}]" {
				t.Fatalf("changed assets: %v", r2.ChangedAssets)
			}
			// Dropping every asset is a v2 revision that lists them as removed.
			r3, err := m.ProposeBundle(ctx, bundleFiles("v1"), "alice", "")
			if err != nil {
				t.Fatal(err)
			}
			if r3.HasAssets() || !strings.HasPrefix(string(SigningPayload(r3)), "ref-revision/v2|") {
				t.Fatalf("no assets means v2: %s", SigningPayload(r3))
			}
			if fmt.Sprint(r3.ChangedAssets) != "[{static/css/extra.css removed} {templates/pages/todos/list.html removed}]" {
				t.Fatalf("changed assets: %v", r3.ChangedAssets)
			}
		})
	}
}

func TestProposeBundleAssetsRefusesBadAssets(t *testing.T) {
	ctx := context.Background()
	m := newBundleManager(NewMemoryStore())
	for name, assets := range map[string][]platform.BundleFile{
		"traversal": {{Path: "templates/../04_routes.bcl", Content: "x"}},
		"bcl":       {{Path: "templates/x.bcl", Content: "name \"x\""}},
		"root":      {{Path: "config/a.html", Content: "x"}},
		"exe":       {{Path: "static/a.exe", Content: "x"}},
		"binary":    {{Path: "static/a.txt", Content: "a\x00b"}},
	} {
		if _, err := m.ProposeBundleAssets(ctx, bundleFiles("v1"), assets, "alice", ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// With no assets it is exactly ProposeBundle.
	r, err := m.ProposeBundleAssets(ctx, bundleFiles("v1"), nil, "alice", "")
	if err != nil || r.HasAssets() || r.Checksum != BundleChecksum(r.Files) {
		t.Fatalf("no assets: %v", err)
	}
}

// TestAssetsTamperEvident: whatever someone with write access to the store
// changes in a signed v3 revision is caught, including edits that keep the
// joined source or the concatenated content identical, or that recompute the
// checksum.
func TestAssetsTamperEvident(t *testing.T) {
	ctx := context.Background()
	key, _ := signing.GenerateEd25519("assets-key")
	keys, _ := signing.NewKeySet(key)
	for name, configure := range map[string]func(*Manager){
		"hmac": func(*Manager) {},
		"key":  func(m *Manager) { m.Secret, m.Signer = nil, keys },
	} {
		t.Run(name, func(t *testing.T) {
			store := NewMemoryStore()
			m := newBundleManager(store)
			configure(m)
			m.Approvals = 0
			r, err := m.ProposeBundleAssets(ctx, bundleFiles("v1"), testAssets("v1"), "alice", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := m.VerifyActivation(r); err != nil {
				t.Fatalf("untouched revision: %v", err)
			}
			asset := func(rev *Revision, p string) int {
				for i, f := range rev.Assets {
					if f.Path == p {
						return i
					}
				}
				t.Fatalf("no asset %s", p)
				return -1
			}
			// refresh is what an attacker can recompute without the key.
			refresh := func(rev *Revision) {
				rev.Source = string(platform.JoinBundle(rev.Files))
				rev.Checksum = AssetsChecksum(rev.Files, rev.Assets)
			}
			sortAssets := func(rev *Revision) {
				if a, err := platform.NewAssets(rev.Assets); err == nil {
					rev.Assets = a
				}
			}
			tampers := map[string]func(*Revision){
				"edit an asset": func(rev *Revision) {
					rev.Assets[asset(rev, "templates/pages/todos/list.html")].Content += "<script>evil()</script>"
				},
				"rename an asset": func(rev *Revision) {
					rev.Assets[asset(rev, "templates/pages/todos/list.html")].Path = "templates/pages/todos/other.html"
				},
				"move bytes between assets": func(rev *Revision) {
					a, b := asset(rev, "static/css/extra.css"), asset(rev, "templates/pages/todos/list.html")
					rev.Assets[a].Content = strings.TrimSuffix(rev.Assets[a].Content, "\n")
					rev.Assets[b].Content = "\n" + rev.Assets[b].Content
				},
				"add an asset": func(rev *Revision) {
					rev.Assets = append(rev.Assets, platform.BundleFile{Path: "static/js/x.js", Content: "evil()"})
				},
				"remove an asset": func(rev *Revision) {
					rev.Assets = rev.Assets[:len(rev.Assets)-1]
				},
				"strip every asset": func(rev *Revision) { rev.Assets = nil },
				"move a BCL file into an asset": func(rev *Revision) {
					// Same bytes overall, different sections.
					f := rev.Files[len(rev.Files)-1]
					rev.Files = rev.Files[:len(rev.Files)-1]
					rev.Assets = append(rev.Assets, platform.BundleFile{Path: "static/" + strings.TrimSuffix(f.Path, ".bcl") + ".txt", Content: f.Content})
				},
				"move an asset into a BCL file": func(rev *Revision) {
					a := rev.Assets[0]
					rev.Assets = rev.Assets[1:]
					rev.Files = append(rev.Files, platform.BundleFile{Path: "99_moved.bcl", Content: a.Content})
				},
				"edit a BCL file": func(rev *Revision) { rev.Files[1].Content += "# evil\n" },
			}
			for tname, tamper := range tampers {
				for _, fix := range []bool{false, true} {
					forged := clone(r)
					tamper(forged)
					if fix {
						sortFiles(forged)
						sortAssets(forged)
						refresh(forged)
					}
					if err := m.Verify(forged); !errors.Is(err, ErrTampered) {
						t.Errorf("%s (fix=%v): %v", tname, fix, err)
					}
					if err := store.Update(ctx, forged); err != nil {
						t.Fatal(err)
					}
					if _, err := m.Activate(ctx, r.ID, "mallory"); !errors.Is(err, ErrTampered) {
						t.Errorf("%s (fix=%v): activated: %v", tname, fix, err)
					}
				}
			}
			// Stripping the assets and pretending to be a v2 revision.
			down := clone(r)
			down.Assets = nil
			down.Checksum = BundleChecksum(down.Files)
			if err := m.Verify(down); !errors.Is(err, ErrTampered) {
				t.Errorf("downgrade to v2: %v", err)
			}
			// Assets without files.
			orphan := clone(r)
			orphan.Files, orphan.Source = nil, "name \"x\"\n"
			orphan.Checksum = sourceChecksum([]byte(orphan.Source))
			if err := m.Verify(orphan); !errors.Is(err, ErrTampered) {
				t.Errorf("assets without files: %v", err)
			}
			// An out-of-order asset list and an invalid asset path.
			unsorted := clone(r)
			unsorted.Assets[0], unsorted.Assets[1] = unsorted.Assets[1], unsorted.Assets[0]
			unsorted.Checksum = AssetsChecksum(unsorted.Files, unsorted.Assets)
			if err := m.Verify(unsorted); !errors.Is(err, ErrTampered) {
				t.Errorf("unsorted assets: %v", err)
			}
			badPath := clone(r)
			badPath.Assets = []platform.BundleFile{{Path: "templates/../etc/x.html", Content: "x"}}
			badPath.Checksum = AssetsChecksum(badPath.Files, badPath.Assets)
			if err := m.Verify(badPath); !errors.Is(err, ErrTampered) {
				t.Errorf("invalid asset path: %v", err)
			}
			if err := store.Update(ctx, r); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Activate(ctx, r.ID, "bob"); err != nil {
				t.Fatalf("activate the real revision: %v", err)
			}
		})
	}
}

func TestAssetsChecksumSeparatesSections(t *testing.T) {
	files := []platform.BundleFile{{Path: "a.bcl", Content: "x"}}
	assets := []platform.BundleFile{{Path: "static/a.txt", Content: "y"}}
	sums := map[string]bool{}
	for _, c := range []string{
		BundleChecksum(files),
		AssetsChecksum(files, nil),
		AssetsChecksum(files, assets),
		AssetsChecksum(nil, assets),
		AssetsChecksum(append(append([]platform.BundleFile{}, files...), platform.BundleFile{Path: "b.bcl", Content: "y"}), nil),
		AssetsChecksum(files, []platform.BundleFile{{Path: "static/a.txt", Content: "x"}}),
		AssetsChecksum([]platform.BundleFile{{Path: "a.bcl", Content: "y"}}, []platform.BundleFile{{Path: "static/a.txt", Content: "x"}}),
	} {
		sums[c] = true
	}
	if len(sums) != 7 {
		t.Fatalf("expected 7 distinct checksums, got %d", len(sums))
	}
	// A v3 checksum with no assets is not the v2 one, so a stripped revision
	// can never be mistaken for a v2 one.
	if AssetsChecksum(files, nil) == BundleChecksum(files) {
		t.Fatal("v3 and v2 checksums must differ")
	}
}

func TestRevisionAssetsFS(t *testing.T) {
	m := newBundleManager(NewMemoryStore())
	r, err := m.ProposeBundleAssets(context.Background(), bundleFiles("v1"), testAssets("v1"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "templates/pages/todos"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "templates/pages/todos/list.html"), []byte("disk list"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "templates/pages/todos/detail.html"), []byte("disk detail"), 0o644)
	got := r.AssetsFS(os.DirFS(dir))
	for p, want := range map[string]string{
		"templates/pages/todos/list.html":   "<h1>v1 list</h1>\n",
		"templates/pages/todos/detail.html": "disk detail",
		"static/css/extra.css":              "body{color:v1}\n",
	} {
		b, err := fs.ReadFile(got, p)
		if err != nil || string(b) != want {
			t.Errorf("%s: %q %v", p, b, err)
		}
	}
	plain, _ := m.ProposeBundle(context.Background(), bundleFiles("v2"), "alice", "")
	base := os.DirFS(dir)
	if plain.AssetsFS(base) != base {
		t.Error("a revision without assets should hand back the base unchanged")
	}
}

func TestMemoryStoreClonesAssetsDeeply(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	r := &Revision{ID: "r1", App: "a", Assets: []platform.BundleFile{{Path: "static/a.txt", Content: "one"}}}
	if err := s.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Assets[0].Content = "changed after create"
	got, _ := s.Get(ctx, "r1")
	if got.Assets[0].Content != "one" {
		t.Fatal("Create kept an alias")
	}
	got.Assets[0].Content = "changed after get"
	again, _ := s.Get(ctx, "r1")
	if again.Assets[0].Content != "one" {
		t.Fatal("Get returned an alias")
	}
}

func TestSQLStoreAssetsAndMigration(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// A table as the bundle-era version created it: a files column, no assets
	// column, and a v2 bundle revision in it.
	if _, err := db.Exec(`CREATE TABLE ref_revisions (
		id TEXT PRIMARY KEY, app TEXT NOT NULL, seq BIGINT NOT NULL, status TEXT NOT NULL, doc TEXT NOT NULL,
		files TEXT NULL,
		UNIQUE (app, seq))`); err != nil {
		t.Fatal(err)
	}
	old := newBundleManager(NewMemoryStore())
	oldRev, err := old.ProposeBundle(ctx, bundleFiles("old"), "alice", "before assets")
	if err != nil {
		t.Fatal(err)
	}
	stripped := *oldRev
	stripped.Files = nil
	doc, _ := json.Marshal(&stripped)
	files, _ := json.Marshal(oldRev.Files)
	if _, err := db.Exec(`INSERT INTO ref_revisions (id, app, seq, status, doc, files) VALUES (?, ?, ?, ?, ?, ?)`,
		oldRev.ID, oldRev.App, oldRev.Seq, oldRev.Status, string(doc), string(files)); err != nil {
		t.Fatal(err)
	}

	s, err := NewSQLStore(db, "sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate twice: %v", err)
	}
	got, err := s.Get(ctx, oldRev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasAssets() || !got.IsBundle() {
		t.Fatalf("an old v2 row should read back as a v2 bundle: %+v", got)
	}
	if err := newBundleManager(s).Verify(got); err != nil {
		t.Fatalf("an old v2 row no longer verifies: %v", err)
	}

	m := newBundleManager(s)
	r, err := m.ProposeBundleAssets(ctx, bundleFiles("v1"), testAssets("v1"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := s.Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(fetched.Assets) != fmt.Sprint(r.Assets) || len(fetched.Assets) != 2 || len(fetched.Files) != 3 {
		t.Fatalf("assets did not round-trip: %+v", fetched.Assets)
	}
	if err := m.Verify(fetched); err != nil {
		t.Fatalf("stored v3 revision: %v", err)
	}
	var stored string
	if err := db.QueryRow(`SELECT doc FROM ref_revisions WHERE id = ?`, r.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, `"assets"`) || strings.Contains(stored, `"files"`) {
		t.Fatal("files and assets should be stored in their columns, not repeated in the document")
	}
	if _, err := m.Approve(ctx, r.ID, "bob", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Activate(ctx, r.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, "hello", 0)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	if list[0].ID != r.ID || len(list[0].Assets) != 2 || list[1].HasAssets() {
		t.Fatalf("list: %+v", list)
	}
	if list[0].Status != StatusActive {
		t.Fatalf("status after Update: %s", list[0].Status)
	}

	// A fresh table (created by this version) has both columns.
	db2, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	s2, _ := NewSQLStore(db2, "sqlite", "")
	if err := s2.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s2.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	m2 := newBundleManager(s2)
	if _, err := m2.ProposeBundleAssets(ctx, bundleFiles("v1"), testAssets("v1"), "alice", ""); err != nil {
		t.Fatal(err)
	}
}

func TestAdminAssetProposals(t *testing.T) {
	m := newBundleManager(NewMemoryStore())
	const alice, bob = "alice-token-0123456789", "bob-token-0123456789ab"
	admin := httptest.NewServer((&Admin{Manager: m, MaxSource: 8192, Tokens: map[string]string{alice: "alice", bob: "bob"}}).Handler())
	defer admin.Close()
	c := adminClient{t, admin.URL}
	toMap := func(fs []platform.BundleFile) map[string]string {
		out := map[string]string{}
		for _, f := range fs {
			out[f.Path] = f.Content
		}
		return out
	}
	files, assets := toMap(bundleFiles("v1")), toMap(testAssets("v1"))

	status, body := c.do("POST", "/validate", alice, map[string]any{"files": files, "assets": assets})
	if status != 200 || body["valid"] != true {
		t.Fatalf("validate with assets: %d %v", status, body)
	}
	// Bad assets are 422, on validate and on propose.
	for name, bad := range map[string]map[string]string{
		"traversal": {"templates/../a.html": "x"},
		"bcl":       {"templates/a.bcl": "name \"x\""},
		"root":      {"other/a.html": "x"},
	} {
		for _, path := range []string{"/validate", "/revisions"} {
			if status, _ := c.do("POST", path, alice, map[string]any{"files": files, "assets": bad}); status != 422 {
				t.Errorf("%s %s: %d", name, path, status)
			}
		}
	}
	// Assets go with files, not with source.
	if status, _ := c.do("POST", "/revisions", alice, map[string]any{"source": appSource("v1"), "assets": assets}); status != 400 {
		t.Errorf("source with assets: %d", status)
	}
	// The size limit covers files and assets together.
	big := map[string]string{"static/big.txt": strings.Repeat("x", 9000)}
	if status, _ := c.do("POST", "/revisions", alice, map[string]any{"files": files, "assets": big}); status != 413 {
		t.Errorf("oversize assets: %d", status)
	}

	status, body = c.do("POST", "/revisions", alice, map[string]any{"files": files, "assets": assets, "message": "one"})
	if status != 201 || fmt.Sprint(body["assets"]) != "[static/css/extra.css templates/pages/todos/list.html]" ||
		fmt.Sprint(body["changed_assets"]) != "[map[path:static/css/extra.css status:added] map[path:templates/pages/todos/list.html status:added]]" {
		t.Fatalf("propose with assets: %d %v", status, body)
	}
	v1 := fmt.Sprint(body["id"])
	if status, _ := c.do("POST", "/revisions/"+v1+"/approve", bob, map[string]any{}); status != 200 {
		t.Fatalf("approve: %d", status)
	}
	if status, _ := c.do("POST", "/revisions/"+v1+"/activate", bob, map[string]any{}); status != 200 {
		t.Fatalf("activate: %d", status)
	}
	changed := toMap(testAssets("v1"))
	changed["templates/pages/todos/list.html"] = "<h1>changed</h1>\n"
	status, body = c.do("POST", "/revisions", alice, map[string]any{"files": files, "assets": changed, "message": "two"})
	if status != 201 || fmt.Sprint(body["changed_assets"]) != "[map[path:templates/pages/todos/list.html status:modified]]" {
		t.Fatalf("second: %d %v", status, body)
	}
	// GET returns the assets with their content; a files-only proposal lists none.
	status, body = c.do("GET", "/revisions/"+v1, alice, nil)
	as, _ := body["assets"].([]any)
	if status != 200 || len(as) != 2 {
		t.Fatalf("get: %d %v", status, body)
	}
	first, _ := as[0].(map[string]any)
	if first["path"] != "static/css/extra.css" || !strings.Contains(fmt.Sprint(first["content"]), "color") {
		t.Fatalf("asset entry: %v", first)
	}
	status, body = c.do("POST", "/revisions", alice, map[string]any{"files": files, "message": "no assets"})
	if status != 201 || body["assets"] != nil {
		t.Fatalf("files-only: %d %v", status, body)
	}
}

// TestSupervisorPerRevisionHooks: NewAppFor and MountFor see the revision
// being built (so a host can read its assets), and Closed fires when a
// superseded generation has drained.
func TestSupervisorPerRevisionHooks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newBundleManager(NewMemoryStore())
	activate := func(files []platform.BundleFile, assets []platform.BundleFile) *Revision {
		t.Helper()
		r, err := m.ProposeBundleAssets(ctx, files, assets, "alice", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Approve(ctx, r.ID, "bob", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Activate(ctx, r.ID, "bob"); err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1 := activate(bundleFiles("v1"), testAssets("v1"))

	var mu sync.Mutex
	var newApp, mounted, closed []int64
	sup := &Supervisor{Manager: m, Poll: 50 * time.Millisecond, Grace: 100 * time.Millisecond, Drain: time.Second,
		Build: func(ctx context.Context, src []byte) (*platform.Platform, error) {
			return platform.Compile(ctx, src, ".", platform.DefaultLoadOptions())
		},
		NewAppFor: func(rev *Revision) *fh.App {
			mu.Lock()
			newApp = append(newApp, rev.Seq)
			mu.Unlock()
			return fh.New(fh.WithStartupBannerDisabled(true))
		},
		MountFor: func(app *fh.App, p *platform.Platform, rev *Revision) error {
			mu.Lock()
			mounted = append(mounted, rev.Seq)
			mu.Unlock()
			if len(rev.Assets) == 0 {
				return errors.New("MountFor should see the revision's assets")
			}
			return p.Mount(app)
		},
		Closed: func(rev *Revision) {
			mu.Lock()
			closed = append(closed, rev.Seq)
			mu.Unlock()
		}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- sup.Serve(ctx, ln) }()
	app := "http://" + ln.Addr().String()
	waitFor(t, func() bool { s, _ := get(t, app+"/hello"); return s == "hello from v1" })

	r2 := activate(bundleFiles("v2"), testAssets("v2"))
	sup.Notify()
	waitFor(t, func() bool { s, _ := get(t, app+"/hello"); return s == "hello from v2" })
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(closed) == 1
	})
	mu.Lock()
	sawApp, sawMount, sawClosed := fmt.Sprint(newApp), fmt.Sprint(mounted), fmt.Sprint(closed)
	mu.Unlock()
	if sawApp != fmt.Sprint([]int64{r1.Seq, r2.Seq}) || sawMount != sawApp {
		t.Errorf("hooks saw %v / %v", sawApp, sawMount)
	}
	if sawClosed != fmt.Sprint([]int64{r1.Seq}) {
		t.Errorf("closed %v; want revision %d", sawClosed, r1.Seq)
	}
	// Shutting down drains the last generation, which calls Closed too (it
	// takes mu, so it must not be held here).
	cancel()
	<-served
}
