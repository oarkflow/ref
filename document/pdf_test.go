package document

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func sample(rows int) Doc {
	d := Doc{
		Title:    "Passport approval",
		Subtitle: "Certificate PPA-2026-0001 (issued 2026-03-01)",
		Footer:   "Verify at https://example.gov/verify/ABCD-1234 - code ABCD-1234",
		Blocks: []Block{
			{Kind: Fields, Fields: []Field{{"Full name", "Asha Rai"}, {"Date of birth", "1990-05-01"}, {"Address", strings.Repeat("Ward 4, Baneshwor, Kathmandu (Bagmati) ", 4)}}},
			{Kind: Notice, Text: "Verification code: ABCD-1234-EF56 — scan or visit the verification page"},
			{Kind: Heading, Text: "Line items"},
		},
	}
	t := Block{Kind: Table, Columns: []string{"#", "Item", "Amount (NPR)"}}
	for i := 0; i < rows; i++ {
		t.Rows = append(t.Rows, []string{strconv.Itoa(i + 1), fmt.Sprintf("Service fee (row %d)", i+1), "5000"})
	}
	d.Blocks = append(d.Blocks, t, Block{Kind: Rule}, Block{Kind: Paragraph, Text: "Parentheses (like these) and back\\slashes must be escaped. Café costs 5€."})
	return d
}

// structure checks the xref table points at every object and the streams
// inflate — what a strict reader does first.
func structure(t *testing.T, pdf []byte) (pages int, text string) {
	t.Helper()
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		t.Fatal("missing header or trailer")
	}
	m := regexp.MustCompile(`startxref\n(\d+)\n`).FindSubmatch(pdf)
	xref, _ := strconv.Atoi(string(m[1]))
	if !bytes.HasPrefix(pdf[xref:], []byte("xref\n")) {
		t.Fatal("startxref does not point at the xref table")
	}
	entries := regexp.MustCompile(`(\d{10}) 00000 n `).FindAllSubmatch(pdf[xref:], -1)
	for i, e := range entries {
		off, _ := strconv.Atoi(string(e[1]))
		want := fmt.Sprintf("%d 0 obj", i+1)
		if !bytes.HasPrefix(pdf[off:], []byte(want)) {
			t.Fatalf("xref entry %d points at %q", i+1, pdf[off:off+12])
		}
	}
	for _, s := range regexp.MustCompile(`(?s)stream\n(.*?)\nendstream`).FindAllSubmatch(pdf, -1) {
		zr, err := zlib.NewReader(bytes.NewReader(s[1]))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		text += string(raw)
	}
	pages = bytes.Count(pdf, []byte("/Type /Page "))
	kids := regexp.MustCompile(`/Kids \[([^\]]*)\]`).FindSubmatch(pdf)
	refs := regexp.MustCompile(`(\d+) 0 R`).FindAllSubmatch(kids[1], -1)
	if len(refs) != pages {
		t.Fatalf("/Kids lists %d pages, document has %d", len(refs), pages)
	}
	for _, ref := range refs {
		n, _ := strconv.Atoi(string(ref[1]))
		off, _ := strconv.Atoi(string(entries[n-1][1]))
		if !bytes.Contains(pdf[off:off+80], []byte("/Type /Page ")) {
			t.Fatalf("/Kids entry %d is not a page object", n)
		}
	}
	return pages, text
}

func TestRenderSinglePage(t *testing.T) {
	pdf, err := Render(sample(3))
	if err != nil {
		t.Fatal(err)
	}
	pages, text := structure(t, pdf)
	if pages != 1 {
		t.Fatalf("pages = %d", pages)
	}
	for _, want := range []string{"(Passport approval)", "(Asha Rai)", "(Page 1 of 1)", `\(like these\)`, `back\\slashes`, `\351`, `\200`} {
		if !strings.Contains(text, want) {
			t.Fatalf("content missing %q", want)
		}
	}
}

func TestRenderBreaksPagesAndRepeatsTableHeader(t *testing.T) {
	pdf, err := Render(sample(150))
	if err != nil {
		t.Fatal(err)
	}
	pages, text := structure(t, pdf)
	if pages < 3 {
		t.Fatalf("150 rows should span several pages, got %d", pages)
	}
	if got := strings.Count(text, "(Amount \\(NPR\\))"); got != pages {
		t.Fatalf("table header drawn %d times over %d pages", got, pages)
	}
	if !strings.Contains(text, fmt.Sprintf("(Page %d of %d)", pages, pages)) || !strings.Contains(text, "(Service fee \\(row 150\\))") {
		t.Fatal("last page missing footer or final row")
	}
}

func TestWrap(t *testing.T) {
	lines := wrap("the quick brown fox jumps over the lazy dog", 60, 10, false)
	for _, l := range lines {
		if textWidth(l, 10, false) > 60 {
			t.Fatalf("line too wide: %q", l)
		}
	}
	if strings.Join(lines, " ") != "the quick brown fox jumps over the lazy dog" {
		t.Fatalf("wrap lost words: %q", lines)
	}
	long := wrap(strings.Repeat("x", 200), 50, 10, false)
	if len(long) < 3 {
		t.Fatalf("long word not broken: %q", long)
	}
	if escape("नेपाल") != "?????" {
		t.Fatalf("non-Latin text: %q", escape("नेपाल"))
	}
}
