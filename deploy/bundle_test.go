package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/signing"
)

// bundleFiles is appSource split over three files.
func bundleFiles(version string) []platform.BundleFile {
	return []platform.BundleFile{
		{Path: "20_route.bcl", Content: "route \"hello\" {\n  method GET\n  path \"/hello\"\n  intent \"hello\"\n}\n"},
		{Path: "00_app.bcl", Content: fmt.Sprintf("name \"hello\"\nversion %q\n", version)},
		{Path: "10_intent.bcl", Content: fmt.Sprintf("intent \"hello\" {\n  response \"msg\"\n  node \"msg\" {\n    uses \"constant\"\n    provides [msg]\n    config {\n      value %q\n    }\n  }\n}\n", "hello from "+version)},
	}
}

func newBundleManager(store Store) *Manager {
	m := newManager(store)
	m.ValidateBundle = func(ctx context.Context, b platform.Bundle) platform.ValidationReport {
		return platform.ValidateBundle(ctx, b, ".", platform.DefaultLoadOptions())
	}
	return m
}

// TestLegacyRevisionStillVerifies pins a revision signed (HMAC and Ed25519,
// approved by a reviewer) by the code before bundles existed. Its checksum,
// signing payload and approval payload must not change: a store full of such
// revisions has to keep activating.
func TestLegacyRevisionStillVerifies(t *testing.T) {
	raw, err := os.ReadFile("testdata/legacy_revision.json")
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
	if r.IsBundle() {
		t.Fatal("fixture should have no files")
	}
	if string(SigningPayload(r)) != fx.SigningPayload || string(ApprovalPayload(r)) != fx.ApprovalPayload {
		t.Fatalf("payloads changed:\n%s\n%s", SigningPayload(r), ApprovalPayload(r))
	}
	if !strings.HasPrefix(fx.SigningPayload, "ref-revision/v1|") || !strings.HasPrefix(fx.ApprovalPayload, "ref-revision-approval/v1|") {
		t.Fatalf("fixture is not a v1 revision: %s", fx.SigningPayload)
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
			t.Errorf("%s: legacy revision no longer verifies: %v", name, err)
		}
	}
	// And it is still tamper-evident.
	forged := *r
	forged.Source += "\n# evil"
	if err := (&Manager{App: "hello", Secret: []byte(fx.Secret)}).Verify(&forged); !errors.Is(err, ErrTampered) {
		t.Fatalf("forged legacy revision: %v", err)
	}
}

func TestBundleRevisionLifecycle(t *testing.T) {
	ctx := context.Background()
	key, _ := signing.GenerateEd25519("bundle-key")
	keys, _ := signing.NewKeySet(key)
	for name, configure := range map[string]func(*Manager){
		"hmac": func(*Manager) {},
		"key":  func(m *Manager) { m.Secret, m.Signer = nil, keys },
		"both": func(m *Manager) { m.Signer = keys },
	} {
		t.Run(name, func(t *testing.T) {
			m := newBundleManager(NewMemoryStore())
			configure(m)
			files := bundleFiles("v1")
			r, err := m.ProposeBundle(ctx, files, "alice", "first")
			if err != nil {
				t.Fatal(err)
			}
			if !r.IsBundle() || len(r.Files) != 3 || r.Files[0].Path != "00_app.bcl" {
				t.Fatalf("files not sorted/kept: %+v", r.Files)
			}
			if r.Source != string(platform.Bundle(r.Files).Source()) {
				t.Fatal("Source is not the files joined")
			}
			if r.Checksum != BundleChecksum(r.Files) || r.Checksum == sourceChecksum([]byte(r.Source)) {
				t.Fatal("bundle checksum should be the record hash, not the source hash")
			}
			if !strings.HasPrefix(string(SigningPayload(r)), "ref-revision/v2|") ||
				!strings.HasPrefix(string(ApprovalPayload(r)), "ref-revision-approval/v2|") {
				t.Fatalf("payloads: %s / %s", SigningPayload(r), ApprovalPayload(r))
			}
			// The caller's slice was not aliased or reordered.
			if files[0].Path != "20_route.bcl" {
				t.Fatal("input reordered")
			}
			if _, err := m.Approve(ctx, r.ID, "bob", ""); err != nil {
				t.Fatal(err)
			}
			act, err := m.Activate(ctx, r.ID, "bob")
			if err != nil || act.Status != StatusActive {
				t.Fatalf("activate: %v", err)
			}

			// v2 changes only the app file and the intent file.
			r2, err := m.ProposeBundle(ctx, bundleFiles("v2"), "alice", "second")
			if err != nil {
				t.Fatal(err)
			}
			want := []FileChange{{"00_app.bcl", "modified"}, {"10_intent.bcl", "modified"}}
			if fmt.Sprint(r2.ChangedFiles) != fmt.Sprint(want) {
				t.Fatalf("changed files: %v; want %v", r2.ChangedFiles, want)
			}
			// v3 drops the route file and adds another.
			v3 := bundleFiles("v2")[1:]
			v3 = append(v3, platform.BundleFile{Path: "30_extra.bcl", Content: "# nothing\n"})
			r3, err := m.ProposeBundle(ctx, v3, "alice", "third")
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(r3.ChangedFiles) != fmt.Sprint([]FileChange{{"20_route.bcl", "removed"}, {"30_extra.bcl", "added"}}) &&
				fmt.Sprint(r3.ChangedFiles) != fmt.Sprint([]FileChange{{"00_app.bcl", "modified"}, {"10_intent.bcl", "modified"}, {"20_route.bcl", "removed"}, {"30_extra.bcl", "added"}}) {
				t.Fatalf("changed files: %v", r3.ChangedFiles)
			}
		})
	}
}

func TestProposeBundleRefusesBadFilesAndBadDocuments(t *testing.T) {
	ctx := context.Background()
	m := newBundleManager(NewMemoryStore())
	for name, files := range map[string][]platform.BundleFile{
		"none":       nil,
		"traversal":  {{Path: "../x.bcl", Content: "name \"x\""}},
		"not bcl":    {{Path: "x.txt", Content: "name \"x\""}},
		"duplicated": {{Path: "a.bcl", Content: ""}, {Path: "a.bcl", Content: ""}},
	} {
		if _, err := m.ProposeBundle(ctx, files, "alice", ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A document that fails validation reports the file it is in.
	bad := []platform.BundleFile{
		{Path: "00_app.bcl", Content: "name \"x\"\n"},
		{Path: "10_routes.bcl", Content: "\n\nroute \"r\" {\n  method GET\n  path \"/r\"\n  intent \"missing\"\n}\n"},
	}
	_, err := m.ProposeBundle(ctx, bad, "alice", "")
	var inv *InvalidError
	if !errors.As(err, &inv) {
		t.Fatalf("expected InvalidError: %v", err)
	}
	var located bool
	for _, d := range inv.Report.Diagnostics {
		if d.Span != nil && d.Span.File == "10_routes.bcl" && d.Span.Line >= 3 {
			located = true
		}
	}
	if !located {
		t.Fatalf("no diagnostic names 10_routes.bcl: %+v", inv.Report.Diagnostics)
	}
}

// TestBundleTamperEvident: whatever someone with write access to the store
// changes in a signed bundle revision is caught, including edits that keep
// the joined source identical or that also recompute the checksum.
func TestBundleTamperEvident(t *testing.T) {
	ctx := context.Background()
	key, _ := signing.GenerateEd25519("bundle-key")
	keys, _ := signing.NewKeySet(key)
	for name, configure := range map[string]func(*Manager){
		"hmac": func(*Manager) {},
		"key":  func(m *Manager) { m.Secret, m.Signer = nil, keys },
	} {
		t.Run(name, func(t *testing.T) {
			store := NewMemoryStore()
			m := newBundleManager(store)
			configure(m)
			m.Approvals = 0 // proposals are approved (and approval-signed) at once
			r, err := m.ProposeBundle(ctx, bundleFiles("v1"), "alice", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := m.VerifyActivation(r); err != nil {
				t.Fatalf("untouched revision: %v", err)
			}

			file := func(rev *Revision, path string) int {
				for i, f := range rev.Files {
					if f.Path == path {
						return i
					}
				}
				t.Fatalf("no file %s", path)
				return -1
			}
			// refresh recomputes what an attacker could recompute without the
			// key: Source from the files and the checksum from the content.
			refresh := func(rev *Revision) {
				rev.Source = string(platform.JoinBundle(rev.Files))
				rev.Checksum = BundleChecksum(rev.Files)
			}
			tampers := map[string]func(*Revision){
				"rename a file": func(rev *Revision) {
					rev.Files[file(rev, "20_route.bcl")].Path = "21_route.bcl"
				},
				"move bytes between files (same joined source)": func(rev *Revision) {
					// "…}\n" + "\n" + "intent…" and "…}" + "\n" + "\nintent…" join identically.
					a, b := file(rev, "00_app.bcl"), file(rev, "10_intent.bcl")
					rev.Files[a].Content = strings.TrimSuffix(rev.Files[a].Content, "\n")
					rev.Files[b].Content = "\n" + rev.Files[b].Content
				},
				"add a file": func(rev *Revision) {
					rev.Files = append(rev.Files, platform.BundleFile{Path: "99_x.bcl", Content: "# x\n"})
				},
				"remove a file": func(rev *Revision) {
					rev.Files = rev.Files[:len(rev.Files)-1]
				},
				"edit a file": func(rev *Revision) {
					rev.Files[file(rev, "10_intent.bcl")].Content += "# evil\n"
				},
			}
			for tname, tamper := range tampers {
				for _, fix := range []bool{false, true} {
					forged := clone(r)
					tamper(forged)
					if fix {
						// Sorted, as a careful attacker would leave them.
						sortFiles(forged)
						refresh(forged)
					}
					if tname == "move bytes between files (same joined source)" && string(platform.JoinBundle(forged.Files)) != r.Source {
						t.Fatalf("test bug: joined source changed")
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
			// Files edited but not Source, and Source edited but not Files.
			forged := clone(r)
			forged.Files[file(forged, "10_intent.bcl")].Content += "# evil\n"
			forged.Checksum = BundleChecksum(forged.Files)
			if err := m.Verify(forged); !errors.Is(err, ErrTampered) {
				t.Errorf("files-only edit: %v", err)
			}
			forged = clone(r)
			forged.Source += "# evil\n"
			if err := m.Verify(forged); !errors.Is(err, ErrTampered) {
				t.Errorf("source-only edit: %v", err)
			}
			// Downgrade: strip the files, keeping a Source that matches the
			// bundle's joined text — the v1 checksum rules reject it.
			legacy := clone(r)
			legacy.Files = nil
			if err := m.Verify(legacy); !errors.Is(err, ErrTampered) {
				t.Errorf("stripped files: %v", err)
			}
			legacy.Checksum = sourceChecksum([]byte(legacy.Source))
			if err := m.Verify(legacy); !errors.Is(err, ErrTampered) {
				t.Errorf("stripped files with a recomputed checksum: %v", err)
			}
			// Upgrade: attach files to a legacy revision.
			plain, err := newManager(NewMemoryStore()).Propose(ctx, []byte(appSource("v1")), "alice", "")
			if err != nil {
				t.Fatal(err)
			}
			plain.Files = []platform.BundleFile{{Path: "a.bcl", Content: plain.Source}}
			if err := newManager(NewMemoryStore()).Verify(plain); !errors.Is(err, ErrTampered) {
				t.Errorf("files attached to a legacy revision: %v", err)
			}
			// An untouched revision still activates after all that.
			if err := store.Update(ctx, r); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Activate(ctx, r.ID, "bob"); err != nil {
				t.Fatalf("activate the real revision: %v", err)
			}
		})
	}
}

func sortFiles(r *Revision) {
	b, err := platform.NewBundle(r.Files)
	if err == nil {
		r.Files = b
	}
}

// TestBundleChecksumSeparatesLayouts pins that the record hash tells apart
// layouts that join to the same text.
func TestBundleChecksumSeparatesLayouts(t *testing.T) {
	a := []platform.BundleFile{{Path: "a.bcl", Content: "x\n"}, {Path: "b.bcl", Content: "y"}}
	b := []platform.BundleFile{{Path: "a.bcl", Content: "x"}, {Path: "b.bcl", Content: "\ny"}}
	if string(platform.JoinBundle(a)) != string(platform.JoinBundle(b)) {
		t.Fatal("test bug: layouts should join identically")
	}
	c := []platform.BundleFile{{Path: "a.bcl", Content: "x\n"}, {Path: "c.bcl", Content: "y"}}
	one := []platform.BundleFile{{Path: "a.bcl", Content: "x\n\ny"}}
	sums := map[string]bool{}
	for _, f := range [][]platform.BundleFile{a, b, c, one} {
		sums[BundleChecksum(f)] = true
	}
	if len(sums) != 4 {
		t.Fatalf("expected 4 distinct checksums, got %d", len(sums))
	}
	// Content that looks like a record does not collide with a real one.
	x := []platform.BundleFile{{Path: "a.bcl", Content: "1\x00b.bcl\x001\x00z"}}
	y := []platform.BundleFile{{Path: "a.bcl", Content: "1"}, {Path: "b.bcl", Content: "z"}}
	if BundleChecksum(x) == BundleChecksum(y) {
		t.Fatal("record boundary collision")
	}
}

func TestMemoryStoreClonesFilesDeeply(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	r := &Revision{ID: "r1", App: "a", Files: []platform.BundleFile{{Path: "a.bcl", Content: "one"}}}
	if err := s.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Files[0].Content = "changed after create"
	got, _ := s.Get(ctx, "r1")
	if got.Files[0].Content != "one" {
		t.Fatal("Create kept an alias")
	}
	got.Files[0].Content = "changed after get"
	again, _ := s.Get(ctx, "r1")
	if again.Files[0].Content != "one" {
		t.Fatal("Get returned an alias")
	}
}

func TestSQLStoreBundleAndMigration(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// A table as the previous version created it: no files column, one
	// legacy revision in it.
	if _, err := db.Exec(`CREATE TABLE ref_revisions (
		id TEXT PRIMARY KEY, app TEXT NOT NULL, seq BIGINT NOT NULL, status TEXT NOT NULL, doc TEXT NOT NULL,
		UNIQUE (app, seq))`); err != nil {
		t.Fatal(err)
	}
	old := newManager(NewMemoryStore())
	oldRev, err := old.Propose(ctx, []byte(appSource("old")), "alice", "before bundles")
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := json.Marshal(oldRev)
	if _, err := db.Exec(`INSERT INTO ref_revisions (id, app, seq, status, doc) VALUES (?, ?, ?, ?, ?)`,
		oldRev.ID, oldRev.App, oldRev.Seq, oldRev.Status, string(doc)); err != nil {
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
	if got.IsBundle() {
		t.Fatalf("an old row should read back without files: %+v", got.Files)
	}
	if err := newManager(s).Verify(got); err != nil {
		t.Fatalf("an old row no longer verifies: %v", err)
	}

	// New bundle revisions round-trip through the column, including via
	// Update and List.
	m := newBundleManager(s)
	r, err := m.ProposeBundle(ctx, bundleFiles("v1"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := s.Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(fetched.Files) != fmt.Sprint(r.Files) || len(fetched.Files) != 3 {
		t.Fatalf("files did not round-trip: %+v", fetched.Files)
	}
	if err := m.Verify(fetched); err != nil {
		t.Fatalf("stored bundle revision: %v", err)
	}
	var stored string
	if err := db.QueryRow(`SELECT doc FROM ref_revisions WHERE id = ?`, r.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, `"files"`) {
		t.Fatal("files should be stored in their column, not repeated in the document")
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
	if list[0].ID != r.ID || len(list[0].Files) != 3 || list[1].IsBundle() {
		t.Fatalf("list: %+v", list)
	}
	if list[0].Status != StatusActive {
		t.Fatalf("status after Update: %s", list[0].Status)
	}
}

func TestAdminBundleProposals(t *testing.T) {
	m := newBundleManager(NewMemoryStore())
	const alice, bob = "alice-token-0123456789", "bob-token-0123456789ab"
	admin := httptest.NewServer((&Admin{Manager: m, MaxSource: 4096, Tokens: map[string]string{alice: "alice", bob: "bob"}}).Handler())
	defer admin.Close()
	c := adminClient{t, admin.URL}
	toMap := func(fs []platform.BundleFile) map[string]string {
		out := map[string]string{}
		for _, f := range fs {
			out[f.Path] = f.Content
		}
		return out
	}

	// /validate takes files; a bad document names its file.
	bad := toMap(bundleFiles("v1"))
	bad["20_route.bcl"] = "\n\nroute \"hello\" {\n  method GET\n  path \"/hello\"\n  intent \"missing\"\n}\n"
	status, body := c.do("POST", "/validate", alice, map[string]any{"files": bad})
	if status != 200 || body["valid"] != false || !strings.Contains(fmt.Sprint(body["diagnostics"]), "20_route.bcl") {
		t.Fatalf("validate bad files: %d %v", status, body)
	}
	status, body = c.do("POST", "/validate", alice, map[string]any{"files": toMap(bundleFiles("v1"))})
	if status != 200 || body["valid"] != true {
		t.Fatalf("validate good files: %d %v", status, body)
	}
	// source still works.
	if status, body = c.do("POST", "/validate", alice, map[string]any{"source": appSource("v1")}); status != 200 || body["valid"] != true {
		t.Fatalf("validate source: %d %v", status, body)
	}

	// Exactly one of source and files.
	for name, in := range map[string]map[string]any{
		"both":    {"source": appSource("v1"), "files": toMap(bundleFiles("v1"))},
		"neither": {"message": "x"},
	} {
		for _, path := range []string{"/validate", "/revisions"} {
			if status, _ := c.do("POST", path, alice, in); status != 400 {
				t.Errorf("%s %s: %d", name, path, status)
			}
		}
	}
	// Bad paths and oversize totals.
	for name, files := range map[string]map[string]string{
		"traversal": {"../a.bcl": "name \"x\""},
		"not bcl":   {"a.txt": "name \"x\""},
		"empty":     {},
	} {
		if status, _ := c.do("POST", "/revisions", alice, map[string]any{"files": files}); status != 422 {
			t.Errorf("%s: %d", name, status)
		}
	}
	big := map[string]string{"a.bcl": strings.Repeat("#", 3000), "b.bcl": strings.Repeat("#", 3000)}
	if status, _ := c.do("POST", "/revisions", alice, map[string]any{"files": big}); status != 413 {
		t.Errorf("oversize total: %d", status)
	}

	// Propose, approve, activate a bundle; a second one lists what changed.
	status, body = c.do("POST", "/revisions", alice, map[string]any{"files": toMap(bundleFiles("v1")), "message": "one"})
	if status != 201 || fmt.Sprint(body["files"]) != "[00_app.bcl 10_intent.bcl 20_route.bcl]" {
		t.Fatalf("propose bundle: %d %v", status, body)
	}
	v1 := fmt.Sprint(body["id"])
	if status, _ := c.do("POST", "/revisions/"+v1+"/approve", bob, map[string]any{}); status != 200 {
		t.Fatalf("approve: %d", status)
	}
	if status, _ := c.do("POST", "/revisions/"+v1+"/activate", bob, map[string]any{}); status != 200 {
		t.Fatalf("activate: %d", status)
	}
	v2files := toMap(bundleFiles("v1"))
	v2files["00_app.bcl"] = "name \"hello\"\nversion \"1.1\"\n"
	status, body = c.do("POST", "/revisions", alice, map[string]any{"files": v2files, "message": "two"})
	if status != 201 || fmt.Sprint(body["changed_files"]) != "[map[path:00_app.bcl status:modified]]" {
		t.Fatalf("propose second bundle: %d %v", status, body)
	}
	// GET returns the files (and, for compatibility, the source).
	status, body = c.do("GET", "/revisions/"+v1, alice, nil)
	fs, _ := body["files"].([]any)
	if status != 200 || len(fs) != 3 || body["source"] == "" {
		t.Fatalf("get: %d %v", status, body)
	}
	first, _ := fs[0].(map[string]any)
	if first["path"] != "00_app.bcl" || !strings.Contains(fmt.Sprint(first["content"]), "version") {
		t.Fatalf("file entry: %v", first)
	}
	// A legacy listing entry carries no files.
	status, body = c.do("POST", "/revisions", alice, map[string]any{"source": appSource("v9")})
	if status != 201 || body["files"] != nil {
		t.Fatalf("legacy propose: %d %v", status, body)
	}
}

func TestSupervisorServesBundleRevisions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := NewMemoryStore()
	m := newBundleManager(store)
	r1, err := m.ProposeBundle(ctx, bundleFiles("v1"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Approve(ctx, r1.ID, "bob", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Activate(ctx, r1.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	sup := &Supervisor{Manager: m, Poll: 50 * time.Millisecond, Grace: 200 * time.Millisecond, Drain: time.Second,
		Build: func(ctx context.Context, src []byte) (*platform.Platform, error) {
			return platform.Compile(ctx, src, ".", platform.DefaultLoadOptions())
		}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- sup.Serve(ctx, ln) }()
	app := "http://" + ln.Addr().String()
	waitFor(t, func() bool { s, _ := get(t, app+"/hello"); return s == "hello from v1" })

	// A bundle revision whose stored files were edited is refused, and the
	// running generation keeps serving.
	r2, err := m.ProposeBundle(ctx, bundleFiles("v2"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Approve(ctx, r2.ID, "bob", ""); err != nil {
		t.Fatal(err)
	}
	genuine, _ := store.Get(ctx, r2.ID)
	forged := clone(genuine)
	forged.Files[1].Content = strings.Replace(forged.Files[1].Content, "hello from v2", "evil", 1)
	forged.Source = string(platform.JoinBundle(forged.Files))
	forged.Checksum = BundleChecksum(forged.Files)
	_ = store.Update(ctx, forged)
	if _, err := m.Activate(ctx, r2.ID, "bob"); !errors.Is(err, ErrTampered) {
		t.Fatalf("activate forged bundle: %v", err)
	}
	// The genuine one swaps in.
	_ = store.Update(ctx, genuine)
	if _, err := m.Activate(ctx, r2.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	sup.Notify()
	waitFor(t, func() bool { s, _ := get(t, app+"/hello"); return s == "hello from v2" })
	cancel()
	<-served
}
