package platform

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/xml"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/ref/invocation"
	_ "modernc.org/sqlite"
)

func runAction(t *testing.T, name string, res Resource, spec NodeSpec, inputs map[string]any) (ActionResult, error) {
	t.Helper()
	reg := NewRegistry()
	factory := reg.actions[name]
	if factory == nil {
		t.Fatalf("action %s is not registered", name)
	}
	spec.Resource = "r"
	if len(spec.Provides) == 0 {
		spec.Provides = []string{"out"}
	}
	a, err := factory.Build(BuildContext{Resources: map[string]Resource{"r": res}}, spec)
	if err != nil {
		t.Fatalf("build %s: %v", name, err)
	}
	return a.Run(&ActionContext{
		Context: context.Background(), Inputs: inputs, Config: spec.Config, Now: time.Now(),
		Invocation: &invocation.Invocation{ID: "req-1", Intent: "test.intent"},
		Principal:  Principal{ID: "ada"}, TenantID: "acme",
	})
}

func TestLogWriteQueryAndRedact(t *testing.T) {
	sink := &memoryLog{buf: make([]LogEntry, 4)}
	write := func(level, msg string) ActionResult {
		res, err := runAction(t, "log.write", sink, NodeSpec{Name: "w", Config: map[string]any{
			"message": msg, "level": level,
			"fields": map[string]any{"user": "{{ input.user }}", "password": "{{ input.password }}"},
			"redact": []any{"Password"},
		}}, map[string]any{"input": map[string]any{"user": "ada", "password": "hunter2"}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if write("info", "login ok").Outputs["out"] != true {
		t.Fatal("write not acknowledged")
	}
	write("error", "payment failed")
	write("debug", "noise")

	res, err := runAction(t, "log.query", sink, NodeSpec{Name: "q", Config: map[string]any{
		"min_level_fact": "min", "contains_fact": "q",
	}}, map[string]any{"min": "warn", "q": "PAYMENT"})
	if err != nil {
		t.Fatal(err)
	}
	entries := res.Outputs["out"].(map[string]any)["entries"].([]LogEntry)
	if len(entries) != 1 || entries[0].Message != "payment failed" {
		t.Fatalf("entries %+v", entries)
	}
	e := entries[0]
	if e.Fields["password"] != "[redacted]" || e.Fields["user"] != "ada" || e.RequestID != "req-1" || e.Tenant != "acme" || e.Principal != "ada" {
		t.Fatalf("entry %+v", e)
	}
	// Another tenant sees nothing: queries are scoped to the caller's tenant.
	other, _ := sink.Query(context.Background(), LogQuery{Tenant: "globex"})
	if len(other) != 0 {
		t.Fatalf("cross-tenant entries: %v", other)
	}
	// The ring keeps only the newest `capacity` entries.
	write("info", "four")
	write("info", "five")
	all, _ := sink.Query(context.Background(), LogQuery{Limit: 10})
	if len(all) != 4 || all[0].Message != "five" {
		t.Fatalf("ring: %+v", all)
	}
}

func TestLogSinkNeverFailsRequest(t *testing.T) {
	res, err := runAction(t, "log.write", failingSink{}, NodeSpec{Name: "w", Config: map[string]any{"message": "x"}}, map[string]any{})
	if err != nil || res.Outputs["out"] != false {
		t.Fatalf("a broken sink must report false, not fail: %v %v", res.Outputs, err)
	}
}

type failingSink struct{}

func (failingSink) Write(context.Context, LogEntry) error { return io.ErrClosedPipe }
func (failingSink) Query(context.Context, LogQuery) ([]LogEntry, error) {
	return nil, ErrLogQueryUnsupported
}

func TestFileLogRotatesAndQueries(t *testing.T) {
	l, err := openFileLog(filepath.Join(t.TempDir(), "app.log"), 300)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < 10; i++ {
		_ = l.Write(context.Background(), LogEntry{Time: time.Now(), Level: "info", Message: strings.Repeat("m", 40) + string(rune('a'+i))})
	}
	got, _ := l.Query(context.Background(), LogQuery{Limit: 100})
	// Only the current file and one previous generation are kept, so old entries
	// are gone, the newest come first, and the order spans the rotation.
	if len(got) < 3 || len(got) >= 10 || got[0].Message[len(got[0].Message)-1] != 'j' {
		t.Fatalf("rotation: %d entries, first %q", len(got), got[0].Message)
	}
	if prev := got[1].Message; prev[len(prev)-1] != 'i' {
		t.Fatalf("order across rotation: %q", prev)
	}
}

func TestSQLLogEscapesWildcards(t *testing.T) {
	ctx := context.Background()
	res, closer, err := openDatabase(ctx, ResourceSpec{Name: "db", Config: map[string]any{
		"driver": "sqlite", "dsn": "file:" + filepath.Join(t.TempDir(), "l.db"), "max_open_connections": 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	l, err := openSQLLog(ctx, res.(*Database), "app_logs")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"100% done", "1000 done", "a_b", "axb"} {
		_ = l.Write(ctx, LogEntry{Time: time.Now(), Level: "info", Message: m, Tenant: "acme", Fields: map[string]any{"k": "v"}})
	}
	for q, want := range map[string]int{"100%": 1, "a_b": 1, "done": 2, "DONE": 2} {
		got, err := l.Query(ctx, LogQuery{Contains: q})
		if err != nil || len(got) != want {
			t.Fatalf("contains %q: %d entries (want %d), err %v", q, len(got), want, err)
		}
	}
	got, _ := l.Query(ctx, LogQuery{MinLevel: "error"})
	if len(got) != 0 {
		t.Fatalf("level filter: %v", got)
	}
}

func exportRows() []any {
	return []any{
		map[string]any{"name": "Ada", "amount": 12.5, "n": 3, "note": "=HYPERLINK(\"http://evil\")"},
		map[string]any{"name": "Bob <b>", "amount": -4.0, "n": 0, "note": "-5"},
	}
}

func export(t *testing.T, cfg map[string]any) map[string]any {
	t.Helper()
	cfg["rows_fact"] = "rows"
	cfg["output"] = "base64"
	res, err := runAction(t, "report.export", nil, NodeSpec{Name: "e", Config: cfg}, map[string]any{"rows": exportRows()})
	if err != nil {
		t.Fatal(err)
	}
	return res.Outputs["out"].(map[string]any)
}

func decoded(t *testing.T, out map[string]any) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(out["content_base64"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReportCSVNeutralizesFormulas(t *testing.T) {
	out := export(t, map[string]any{"columns": []any{"name:Customer", "amount", "note"}, "filename": "../../etc/passwd"})
	if out["filename"] != "etc_passwd.csv" || out["rows"] != 2 {
		t.Fatalf("out %v", out)
	}
	body := bytes.TrimPrefix(decoded(t, out), []byte("\xEF\xBB\xBF"))
	recs, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if recs[0][0] != "Customer" || recs[1][1] != "12.5" || recs[2][1] != "-4" {
		t.Fatalf("records %v", recs)
	}
	if !strings.HasPrefix(recs[1][2], "'=") {
		t.Fatalf("formula was not defused: %q", recs[1][2])
	}
	if recs[2][2] != "-5" {
		t.Fatalf("a plain negative number must be left alone: %q", recs[2][2])
	}
}

func TestReportXLSXRoundTrip(t *testing.T) {
	out := export(t, map[string]any{"format": "xlsx", "title": "Sales: Q1/2", "columns": []any{"name", "amount", "n"}})
	if out["content_type"] != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("content type %v", out["content_type"])
	}
	body := decoded(t, out)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		parts[f.Name] = string(b)
		// Every part must be well-formed XML, or Excel will repair or refuse it.
		d := xml.NewDecoder(bytes.NewReader(b))
		for {
			if _, err := d.Token(); err != nil {
				if err != io.EOF {
					t.Fatalf("%s is not well-formed: %v", f.Name, err)
				}
				break
			}
		}
	}
	sheet := parts["xl/worksheets/sheet1.xml"]
	for _, want := range []string{`<t xml:space="preserve">name</t>`, `<v>12.5</v>`, `<v>-4</v>`, `<v>3</v>`, "Bob &lt;b&gt;"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("sheet is missing %q in:\n%s", want, sheet)
		}
	}
	if !strings.Contains(parts["xl/workbook.xml"], `name="Sales Q12"`) {
		t.Errorf("sheet name: %s", parts["xl/workbook.xml"])
	}
	if columnName(0) != "A" || columnName(25) != "Z" || columnName(26) != "AA" || columnName(701) != "ZZ" || columnName(702) != "AAA" {
		t.Fatal("column naming")
	}
}

func TestReportHTMLAndJSONAndErrors(t *testing.T) {
	h := string(decoded(t, export(t, map[string]any{"format": "html", "title": "<script>"})))
	if strings.Contains(h, "<script>") || !strings.Contains(h, "Bob &lt;b&gt;") {
		t.Fatalf("html not escaped: %s", h)
	}
	j := string(decoded(t, export(t, map[string]any{"format": "json", "columns": []any{"name"}})))
	if !strings.Contains(j, `"count":2`) || strings.Contains(j, "amount") {
		t.Fatalf("json: %s", j)
	}
	// Unknown format is caught at load time, not on the first request.
	reg := NewRegistry()
	if _, err := reg.actions["report.export"].Build(BuildContext{}, NodeSpec{Name: "e", Config: map[string]any{"rows_fact": "r", "format": "pdf"}}); err == nil {
		t.Fatal("pdf accepted")
	}
	// A row cap protects memory.
	_, err := runAction(t, "report.export", nil, NodeSpec{Name: "e", Config: map[string]any{"rows_fact": "rows", "max_rows": 1}}, map[string]any{"rows": exportRows()})
	if err == nil {
		t.Fatal("max_rows not enforced")
	}
}

func TestReportDownloadIsARawResponse(t *testing.T) {
	res, err := runAction(t, "report.export", nil, NodeSpec{Name: "e", Config: map[string]any{"rows_fact": "rows", "filename": "q1"}}, map[string]any{"rows": exportRows()})
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := res.Outputs["out"].(RawResponse)
	if !ok || raw.Filename != "q1.csv" || !strings.HasPrefix(raw.ContentType, "text/csv") || len(raw.Body) == 0 {
		t.Fatalf("output %#v", res.Outputs["out"])
	}
}

type typedRow struct {
	Seq    int    `json:"seq"`
	Sender string `json:"sender"`
}

func TestReportAcceptsTypedRows(t *testing.T) {
	res, err := runAction(t, "report.export", nil, NodeSpec{Name: "e", Config: map[string]any{
		"rows_fact": "history.messages", "output": "base64", "columns": []any{"seq", "sender"}}},
		map[string]any{"history": map[string]any{"messages": []typedRow{{1, "ada"}, {2, "bob"}}}})
	if err != nil {
		t.Fatal(err)
	}
	body := string(bytes.TrimPrefix(decoded(t, res.Outputs["out"].(map[string]any)), []byte("\xEF\xBB\xBF")))
	if !strings.Contains(body, "1,ada") || !strings.Contains(body, "2,bob") {
		t.Fatalf("csv %q", body)
	}
}
