package platform

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Reports are three nodes in a row:
//
//	database.query  ->  report.export  ->  response (a download)
//	                                   ->  storage.put | notify.send (output "base64")
//
// database.query (or any node that yields a list of rows) is the report
// definition; report.export turns the rows into a file; what happens to the
// file is the next node's business. A scheduled run is a `schedule` block that
// starts the intent. Nothing here is specific to one report, so a new report
// is a new intent, not new code.

const maxReportRows = 1_000_000

func registerReportActions(r *Registry) {
	mustAction(r, "report.export", reportExportAction, ActionInfo{
		Family: "report", Summary: "Render rows as csv, json, html or xlsx, as a file download or as {filename, content_type, content_base64, size, rows}.",
		Kind: "pure", Provides: "{filename, content_type, content_base64, size, rows}",
		Config: []ConfigField{
			{Name: "rows_fact", Type: "fact", Required: true, Summary: "A list of objects"},
			{Name: "format", Type: "string", Default: "csv", Summary: "csv, json, html or xlsx"},
			{Name: "format_fact", Type: "fact", Summary: "Take the format from a fact instead"},
			{Name: "columns", Type: "list", Summary: `"key" or "key:Heading"; default is the first row's keys, sorted`},
			{Name: "title", Type: "template"},
			{Name: "filename", Type: "template", Default: "report", Summary: "Without extension"},
			{Name: "max_rows", Type: "int", Default: "100000"},
			{Name: "output", Type: "string", Default: "download", Summary: "download (the route sends the file) or base64 (publish the fields, to store or send them)"},
		},
	})
}

type reportColumn struct{ key, title string }

var reportExportAction = ActionFactoryFunc(func(_ BuildContext, spec NodeSpec) (Action, error) {
	rowsFact, err := requiredString(spec.Config, "rows_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	format := strings.ToLower(configString(spec.Config, "format", "csv"))
	formatFact := configString(spec.Config, "format_fact", "")
	if formatFact == "" && !validReportFormat(format) {
		return nil, fmt.Errorf("node %q: format %q is not one of csv, json, html, xlsx", spec.Name, format)
	}
	var columns []reportColumn
	for _, c := range configStrings(spec.Config, "columns") {
		key, title, _ := strings.Cut(c, ":")
		if key = strings.TrimSpace(key); key == "" {
			return nil, fmt.Errorf("node %q: an empty column", spec.Name)
		}
		if title = strings.TrimSpace(title); title == "" {
			title = key
		}
		columns = append(columns, reportColumn{key, title})
	}
	title, err := configTemplate(spec.Config, "title", "")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	name, err := configTemplate(spec.Config, "filename", "report")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	output := configString(spec.Config, "output", "download")
	if output != "download" && output != "base64" {
		return nil, fmt.Errorf("node %q: output %q is not download or base64", spec.Name, output)
	}
	limit, err := configInt(spec.Config, "max_rows", 100000)
	if err != nil || limit < 1 || limit > maxReportRows {
		return nil, fmt.Errorf("node %q: max_rows must be between 1 and %d", spec.Name, maxReportRows)
	}

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		raw, found := resolvePath(ctx.Inputs, rowsFact)
		if !found {
			return ActionResult{}, invalidInput("required value %q is missing", rowsFact)
		}
		rows, err := reportRows(raw)
		if err != nil {
			return ActionResult{}, invalidInput("%q: %v", rowsFact, err)
		}
		if len(rows) > limit {
			return ActionResult{}, invalidInput("the report has %d rows, over this node's max_rows of %d", len(rows), limit)
		}
		f := format
		if formatFact != "" {
			v, _ := resolvePath(ctx.Inputs, formatFact)
			if f = strings.ToLower(Stringify(v)); !validReportFormat(f) {
				return ActionResult{}, invalidInput("format must be one of csv, json, html, xlsx")
			}
		}
		cols := columns
		if len(cols) == 0 {
			cols = inferColumns(rows)
		}
		env := actionEnv(ctx)
		heading := ""
		if title != nil {
			if heading, err = title.Render(env); err != nil {
				return ActionResult{}, invalidInput("title: %v", err)
			}
		}
		base, err := name.Render(env)
		if err != nil {
			return ActionResult{}, invalidInput("filename: %v", err)
		}
		var body []byte
		var contentType string
		switch f {
		case "csv":
			body, err = renderCSV(rows, cols)
			contentType = "text/csv; charset=utf-8"
		case "json":
			body, err = renderReportJSON(rows, cols)
			contentType = "application/json"
		case "html":
			body = renderHTML(rows, cols, heading)
			contentType = "text/html; charset=utf-8"
		case "xlsx":
			body, err = renderXLSX(rows, cols, heading)
			contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		}
		if err != nil {
			return ActionResult{}, fmt.Errorf("report export: %w", err)
		}
		filename := safeFilename(base) + "." + f
		if output == "download" {
			return singleOutput(spec, RawResponse{ContentType: contentType, Filename: filename, Body: body}), nil
		}
		return singleOutput(spec, map[string]any{
			"filename":       filename,
			"content_type":   contentType,
			"content_base64": base64.StdEncoding.EncodeToString(body),
			"size":           len(body),
			"rows":           len(rows),
		}), nil
	}), nil
})

func validReportFormat(f string) bool {
	switch f {
	case "csv", "json", "html", "xlsx":
		return true
	}
	return false
}

var unsafeFilename = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// safeFilename keeps a name from a template or a fact from escaping its
// directory or injecting a header.
func safeFilename(s string) string {
	s = strings.Trim(unsafeFilename.ReplaceAllString(strings.TrimSpace(s), "_"), "._")
	if s == "" {
		return "report"
	}
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}

func reportRows(raw any) ([]map[string]any, error) {
	switch v := raw.(type) {
	case []map[string]any:
		return v, nil
	case []any:
		rows := make([]map[string]any, 0, len(v))
		for i, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("row %d is %T, not an object", i, item)
			}
			rows = append(rows, m)
		}
		return rows, nil
	case nil:
		return nil, nil
	}
	// Any other slice (a node's typed result, such as chat.history's messages)
	// is read through its JSON form, which is what a column name refers to.
	if rv := reflect.ValueOf(raw); rv.Kind() == reflect.Slice {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("rows cannot be read: %w", err)
		}
		var rows []map[string]any
		if err := json.Unmarshal(encoded, &rows); err != nil {
			return nil, fmt.Errorf("expected a list of objects, got %T", raw)
		}
		return rows, nil
	}
	return nil, fmt.Errorf("expected a list of objects, got %T", raw)
}

func inferColumns(rows []map[string]any) []reportColumn {
	if len(rows) == 0 {
		return nil
	}
	keys := make([]string, 0, len(rows[0]))
	for k := range rows[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	cols := make([]reportColumn, len(keys))
	for i, k := range keys {
		cols[i] = reportColumn{k, k}
	}
	return cols
}

// cellText renders a value for a text format.
func cellText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}

var plainNumber = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

// neutralize defuses spreadsheet formula injection. A cell a spreadsheet would
// read as a formula (=, @, or +/- followed by anything but a number) is
// prefixed with an apostrophe, so a customer name of "=HYPERLINK(...)" is shown
// rather than run. Real numbers, including negatives, are left alone.
func neutralize(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '@', '\t', '\r':
		return "'" + s
	case '+', '-':
		if !plainNumber.MatchString(s) {
			return "'" + s
		}
	}
	return s
}

func renderCSV(rows []map[string]any, cols []reportColumn) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // BOM, so Excel reads UTF-8
	w := csv.NewWriter(&buf)
	head := make([]string, len(cols))
	for i, c := range cols {
		head[i] = neutralize(c.title)
	}
	if err := w.Write(head); err != nil {
		return nil, err
	}
	rec := make([]string, len(cols))
	for _, row := range rows {
		for i, c := range cols {
			rec[i] = neutralize(cellText(row[c.key]))
		}
		if err := w.Write(rec); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func renderReportJSON(rows []map[string]any, cols []reportColumn) ([]byte, error) {
	out := make([]map[string]any, len(rows))
	for i, row := range rows {
		o := make(map[string]any, len(cols))
		for _, c := range cols {
			o[c.key] = row[c.key]
		}
		out[i] = o
	}
	return json.Marshal(map[string]any{"rows": out, "count": len(out)})
}

func renderHTML(rows []map[string]any, cols []reportColumn, title string) []byte {
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=\"utf-8\"><title>" + html.EscapeString(title) + "</title>")
	b.WriteString("<style>body{font:14px system-ui;margin:2rem}table{border-collapse:collapse}th,td{border:1px solid #ccc;padding:.3rem .6rem;text-align:left}th{background:#f3f3f3}</style>")
	if title != "" {
		b.WriteString("<h1>" + html.EscapeString(title) + "</h1>")
	}
	b.WriteString("<table><thead><tr>")
	for _, c := range cols {
		b.WriteString("<th>" + html.EscapeString(c.title) + "</th>")
	}
	b.WriteString("</tr></thead><tbody>")
	for _, row := range rows {
		b.WriteString("<tr>")
		for _, c := range cols {
			b.WriteString("<td>" + html.EscapeString(cellText(row[c.key])) + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table>")
	return []byte(b.String())
}

// ---------------------------------------------------------------------------
// XLSX: the smallest valid workbook (one sheet, inline strings, a bold header)
// ---------------------------------------------------------------------------

const xlsxMaxRows = 1048576

func renderXLSX(rows []map[string]any, cols []reportColumn, title string) ([]byte, error) {
	if len(rows)+1 > xlsxMaxRows {
		return nil, fmt.Errorf("an xlsx sheet holds at most %d rows; export csv instead", xlsxMaxRows)
	}
	if len(cols) > 16384 {
		return nil, fmt.Errorf("an xlsx sheet holds at most 16384 columns")
	}
	var sheet bytes.Buffer
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	writeRow := func(n int, cells func(col int) (string, bool), bold bool) {
		fmt.Fprintf(&sheet, `<row r="%d">`, n)
		for i := range cols {
			ref := columnName(i) + strconv.Itoa(n)
			text, numeric := cells(i)
			style := ""
			if bold {
				style = ` s="1"`
			}
			if numeric {
				fmt.Fprintf(&sheet, `<c r="%s"%s><v>%s</v></c>`, ref, style, text)
			} else {
				fmt.Fprintf(&sheet, `<c r="%s"%s t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, xmlText(text))
			}
		}
		sheet.WriteString(`</row>`)
	}
	writeRow(1, func(i int) (string, bool) { return cols[i].title, false }, true)
	for r, row := range rows {
		writeRow(r+2, func(i int) (string, bool) {
			switch x := row[cols[i].key].(type) {
			case int, int32, int64, uint, uint32, uint64, float32:
				return fmt.Sprint(x), true
			case float64:
				if x == x && x-x == 0 { // finite
					return strconv.FormatFloat(x, 'f', -1, 64), true
				}
			}
			// Text cells are inert in a spreadsheet (they are not parsed as
			// formulas), so no neutralizing here; csv and html are different.
			return cellText(row[cols[i].key]), false
		}, false)
	}
	sheet.WriteString(`</sheetData></worksheet>`)

	files := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="` + xmlText(sheetName(title)) + `" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`},
		{"xl/styles.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs></styleSheet>`},
		{"xl/worksheets/sheet1.xml", sheet.String()},
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// columnName converts 0 to A, 25 to Z, 26 to AA.
func columnName(i int) string {
	var s []byte
	for i++; i > 0; i = (i - 1) / 26 {
		s = append([]byte{byte('A' + (i-1)%26)}, s...)
	}
	return string(s)
}

var badSheetChars = strings.NewReplacer("[", "", "]", "", ":", "", "*", "", "?", "", "/", "", `\`, "")

func sheetName(title string) string {
	n := strings.TrimSpace(badSheetChars.Replace(title))
	if n == "" {
		return "Report"
	}
	if utf8.RuneCountInString(n) > 31 {
		n = string([]rune(n)[:31])
	}
	return n
}

// xmlText escapes text for an XML body or attribute and drops the control
// characters XML 1.0 forbids, which would make Excel refuse the file.
func xmlText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r != 0xFFFE && r != 0xFFFF && !(r >= 0xD800 && r <= 0xDFFF)):
			switch r {
			case '&':
				b.WriteString("&amp;")
			case '<':
				b.WriteString("&lt;")
			case '>':
				b.WriteString("&gt;")
			case '"':
				b.WriteString("&quot;")
			default:
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
