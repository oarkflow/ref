// Package document renders simple, well-formed PDF documents — certificates,
// receipts, letters, reports — with no dependencies.
//
// A Doc is a title plus a list of blocks (headings, paragraphs, label/value
// fields, tables, rules). Text is wrapped with the real Helvetica metrics,
// pages break automatically (a table repeats its header), and every page
// gets a footer with "Page n of N". Output is PDF 1.4 with the standard
// Helvetica fonts and Flate-compressed content streams, readable by every
// viewer.
//
// The standard fonts use WinAnsi encoding: Latin-1 text renders as is and
// characters outside it are replaced with "?". Scripts such as Devanagari
// need an embedded font, which this package deliberately does not do.
package document

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
	"time"
)

// Block kinds.
const (
	Heading   = "heading"
	Paragraph = "text"
	Fields    = "fields"
	Table     = "table"
	Rule      = "rule"
	Spacer    = "spacer"
	Notice    = "notice" // a boxed paragraph (verification codes, warnings)
)

// Doc is a document to render.
type Doc struct {
	Title    string
	Subtitle string
	// Footer is printed at the bottom left of every page.
	Footer string
	Author string
	Blocks []Block
	// Letter switches from A4 to US Letter.
	Letter bool
	// Created is stamped in the document info (default: now).
	Created time.Time
}

// Block is one element of the page flow.
type Block struct {
	Kind    string
	Text    string
	Fields  []Field
	Columns []string
	Rows    [][]string
}

// Field is a label/value pair.
type Field struct {
	Label string
	Value string
}

const (
	margin      = 50.0
	footerSpace = 36.0
)

type page struct{ ops bytes.Buffer }

type renderer struct {
	w, h  float64
	pages []*page
	y     float64
}

func (r *renderer) cur() *page { return r.pages[len(r.pages)-1] }

func (r *renderer) newPage() {
	r.pages = append(r.pages, &page{})
	r.y = r.h - margin
}

// ensure starts a new page when fewer than need points remain.
func (r *renderer) ensure(need float64) bool {
	if r.y-need < margin+footerSpace {
		r.newPage()
		return true
	}
	return false
}

func (r *renderer) text(x, y float64, size float64, bold bool, gray float64, s string) {
	font := "F1"
	if bold {
		font = "F2"
	}
	fmt.Fprintf(&r.cur().ops, "BT %.3f g /%s %.1f Tf %.2f %.2f Td (%s) Tj ET\n", gray, font, size, x, y, escape(s))
}

func (r *renderer) line(x1, y1, x2, y2, width, gray float64) {
	fmt.Fprintf(&r.cur().ops, "%.3f G %.2f w %.2f %.2f m %.2f %.2f l S\n", gray, width, x1, y1, x2, y2)
}

func (r *renderer) rect(x, y, w, h, fillGray, strokeGray float64) {
	fmt.Fprintf(&r.cur().ops, "%.3f g %.3f G 0.8 w %.2f %.2f %.2f %.2f re B\n", fillGray, strokeGray, x, y, w, h)
}

// paragraph writes wrapped text and advances.
func (r *renderer) paragraph(x, width, size float64, bold bool, gray float64, s string) {
	lead := size * 1.35
	for _, ln := range wrap(s, width, size, bold) {
		r.ensure(lead)
		r.y -= lead
		r.text(x, r.y+size*0.3, size, bold, gray, ln)
	}
}

// Render produces the PDF bytes.
func Render(d Doc) ([]byte, error) {
	r := &renderer{w: 595.28, h: 841.89}
	if d.Letter {
		r.w, r.h = 612, 792
	}
	r.newPage()
	content := r.w - 2*margin
	if d.Title != "" {
		r.paragraph(margin, content, 20, true, 0, d.Title)
	}
	if d.Subtitle != "" {
		r.y -= 2
		r.paragraph(margin, content, 11, false, 0.35, d.Subtitle)
	}
	if d.Title != "" || d.Subtitle != "" {
		r.y -= 8
		r.line(margin, r.y, r.w-margin, r.y, 1.2, 0.2)
		r.y -= 6
	}
	for _, b := range d.Blocks {
		switch b.Kind {
		case Heading:
			r.ensure(40)
			r.y -= 10
			r.paragraph(margin, content, 13, true, 0.1, b.Text)
			r.y -= 2
		case Paragraph, "":
			r.y -= 4
			r.paragraph(margin, content, 10, false, 0, b.Text)
		case Spacer:
			r.y -= 12
		case Rule:
			r.ensure(12)
			r.y -= 8
			r.line(margin, r.y, r.w-margin, r.y, 0.6, 0.6)
		case Notice:
			lines := wrap(b.Text, content-24, 10.5, true)
			h := float64(len(lines))*10.5*1.35 + 16
			r.ensure(h + 8)
			r.y -= 8
			r.rect(margin, r.y-h, content, h, 0.95, 0.55)
			y := r.y - 8
			for _, ln := range lines {
				y -= 10.5 * 1.35
				r.text(margin+12, y+3, 10.5, true, 0.1, ln)
			}
			r.y -= h
		case Fields:
			labelW := content * 0.32
			for _, f := range b.Fields {
				labelLines := wrap(f.Label, labelW-10, 9, true)
				valueLines := wrap(orDash(f.Value), content-labelW, 10, false)
				n := max(len(labelLines), len(valueLines))
				rowH := float64(n)*13.5 + 5
				r.ensure(rowH)
				top := r.y
				for i, ln := range labelLines {
					r.text(margin, top-13.5*float64(i+1)+3, 9, true, 0.4, ln)
				}
				for i, ln := range valueLines {
					r.text(margin+labelW, top-13.5*float64(i+1)+3, 10, false, 0, ln)
				}
				r.y = top - rowH
				r.line(margin, r.y+2, r.w-margin, r.y+2, 0.3, 0.85)
			}
		case Table:
			r.table(b, content)
		default:
			return nil, fmt.Errorf("document: unknown block kind %q", b.Kind)
		}
	}
	return r.finish(d)
}

func (r *renderer) table(b Block, content float64) {
	n := len(b.Columns)
	if n == 0 {
		return
	}
	colW := content / float64(n)
	header := func() {
		r.ensure(24)
		r.y -= 4
		top := r.y
		for i, c := range b.Columns {
			r.text(margin+float64(i)*colW+2, top-12, 9, true, 0.25, truncate(c, colW-6, 9, true))
		}
		r.y = top - 17
		r.line(margin, r.y, r.w-margin, r.y, 0.8, 0.3)
	}
	header()
	for _, row := range b.Rows {
		cells := make([][]string, n)
		lines := 1
		for i := 0; i < n; i++ {
			v := ""
			if i < len(row) {
				v = row[i]
			}
			cells[i] = wrap(v, colW-6, 9.5, false)
			lines = max(lines, len(cells[i]))
		}
		rowH := float64(lines)*12.5 + 5
		if r.ensure(rowH) {
			header()
		}
		top := r.y
		for i, cl := range cells {
			for j, ln := range cl {
				r.text(margin+float64(i)*colW+2, top-12.5*float64(j+1)+2, 9.5, false, 0, ln)
			}
		}
		r.y = top - rowH
		r.line(margin, r.y+2, r.w-margin, r.y+2, 0.3, 0.85)
	}
}

func (r *renderer) finish(d Doc) ([]byte, error) {
	total := len(r.pages)
	for i, p := range r.pages {
		saved := r.pages
		r.pages = []*page{p}
		r.line(margin, margin+18, r.w-margin, margin+18, 0.4, 0.75)
		if d.Footer != "" {
			r.text(margin, margin+4, 8, false, 0.4, truncate(d.Footer, r.w-2*margin-80, 8, false))
		}
		label := fmt.Sprintf("Page %d of %d", i+1, total)
		r.text(r.w-margin-textWidth(label, 8, false), margin+4, 8, false, 0.4, label)
		r.pages = saved
	}

	var out bytes.Buffer
	var offsets []int
	obj := func(body string) int {
		offsets = append(offsets, out.Len())
		n := len(offsets)
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", n, body)
		return n
	}
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	// Object numbers: 1 catalog, 2 pages, 3-4 fonts, 5 info, then per page:
	// content, page.
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	pageIDs := make([]int, total)
	for i := range pageIDs {
		pageIDs[i] = 8 + 2*i
	}
	kids := make([]string, total)
	for i, id := range pageIDs {
		kids[i] = fmt.Sprintf("%d 0 R", id)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), total))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	created := d.Created
	if created.IsZero() {
		created = time.Now()
	}
	obj(fmt.Sprintf("<< /Title (%s) /Author (%s) /Producer (ref document) /CreationDate (D:%s) >>",
		escape(d.Title), escape(d.Author), created.UTC().Format("20060102150405Z")))
	obj("<< >>") // 6: reserved, keeps page numbering simple
	for _, p := range r.pages {
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		if _, err := zw.Write(p.ops.Bytes()); err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
		contentID := len(offsets) + 1
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n<< /Length %d /Filter /FlateDecode >>\nstream\n", contentID, z.Len())
		out.Write(z.Bytes())
		out.WriteString("\nendstream\nendobj\n")
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>",
			r.w, r.h, contentID))
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R /Info 5 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return out.Bytes(), nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// winAnsi maps a rune to its WinAnsi byte ('?' when unrepresentable).
func winAnsi(r rune) byte {
	switch {
	case r >= 32 && r < 127, r >= 160 && r <= 255:
		return byte(r)
	case r == '–':
		return 0x96
	case r == '—':
		return 0x97
	case r == '‘':
		return 0x91
	case r == '’':
		return 0x92
	case r == '“':
		return 0x93
	case r == '”':
		return 0x94
	case r == '•':
		return 0x95
	case r == '€':
		return 0x80
	case r == '\t':
		return ' '
	}
	return '?'
}

func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		c := winAnsi(r)
		switch c {
		case '(', ')', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			if c < 32 || c > 126 {
				fmt.Fprintf(&b, "\\%03o", c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

func charWidth(c byte, bold bool) float64 {
	if c >= 32 && c <= 126 {
		if bold {
			return float64(helveticaBold[c-32])
		}
		return float64(helvetica[c-32])
	}
	return 556
}

func textWidth(s string, size float64, bold bool) float64 {
	w := 0.0
	for _, r := range s {
		w += charWidth(winAnsi(r), bold)
	}
	return w * size / 1000
}

// wrap breaks s into lines no wider than width, honouring newlines and
// breaking words longer than a line.
func wrap(s string, width, size float64, bold bool) []string {
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, w := range words {
			for textWidth(w, size, bold) > width {
				// Break an over-long word.
				cut := len([]rune(w))
				for cut > 1 && textWidth(string([]rune(w)[:cut]), size, bold) > width {
					cut--
				}
				if line != "" {
					out = append(out, line)
					line = ""
				}
				out = append(out, string([]rune(w)[:cut]))
				w = string([]rune(w)[cut:])
			}
			candidate := w
			if line != "" {
				candidate = line + " " + w
			}
			if textWidth(candidate, size, bold) <= width {
				line = candidate
				continue
			}
			out = append(out, line)
			line = w
		}
		out = append(out, line)
	}
	return out
}

func truncate(s string, width, size float64, bold bool) string {
	if textWidth(s, size, bold) <= width {
		return s
	}
	rs := []rune(s)
	for len(rs) > 0 && textWidth(string(rs)+"...", size, bold) > width {
		rs = rs[:len(rs)-1]
	}
	return string(rs) + "..."
}

// Helvetica and Helvetica-Bold advance widths for ASCII 32-126 (AFM units).
var helvetica = [95]int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
}

var helveticaBold = [95]int{
	278, 333, 474, 556, 556, 889, 722, 238, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 333, 333, 584, 584, 584, 611,
	975, 722, 722, 722, 722, 667, 611, 778, 722, 278, 556, 722, 611, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 333, 278, 333, 584, 556,
	333, 556, 611, 556, 611, 556, 333, 611, 611, 278, 278, 556, 278, 889, 611, 611,
	611, 611, 389, 556, 333, 611, 556, 778, 556, 556, 500, 389, 280, 389, 584,
}
