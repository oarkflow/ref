package platform

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const documentApp = `
name "docs"

resource "jwt" {
  kind "auth.jwt"
  config {
    secret env.required("DOC_JWT_SECRET")
  }
}

intent "invoice.pdf" {
  response "pdf"
  node "invoice" {
    uses "constant"
    provides [invoice]
    config {
      value {
        number "INV-7"
        customer "Acme (Nepal) Pvt. Ltd."
        lines [
          { name "Consulting", amount 1200 },
          { name "Travel", amount 300 }
        ]
      }
    }
  }
  node "pdf" {
    uses "document.pdf"
    requires [invoice]
    provides [pdf]
    config {
      title "Invoice {{invoice.number}}"
      subtitle "For {{invoice.customer}}"
      footer "Thank you"
      filename "invoice-{{invoice.number}}"
      blocks [
        { kind "fields", fields [ { label "Customer", value "{{invoice.customer}}" } ] },
        { kind "table", rows "invoice.lines", columns [ { label "Item", value "{{row.name}}" }, { label "Amount", value "{{row.amount}}" } ] },
        { kind "notice", text "Pay within 30 days" }
      ]
    }
  }
}

intent "invoice.b64" {
  response "pdf"
  node "pdf" {
    uses "document.pdf"
    provides [pdf]
    config {
      title "Hello"
      output "base64"
    }
  }
}

route "pdf" {
  method GET
  path "/api/invoice.pdf"
  intent "invoice.pdf"
  auth "jwt"
}
route "b64" {
  method GET
  path "/api/invoice.json"
  intent "invoice.b64"
  auth "jwt"
}
`

func TestDocumentPDF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(documentApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{"DOC_JWT_SECRET": "doc-test-secret-0123456789abcdef-xyz"})
	tok := h.token("jwt", "u1", nil, nil)

	req, _ := http.NewRequest("GET", h.base+"/api/invoice.pdf", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	pdf, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/pdf" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), `filename="invoice-INV-7.pdf"`) || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("pdf: %d %v", resp.StatusCode, resp.Header)
	}
	text := pdfText(t, pdf)
	for _, want := range []string{"(Invoice INV-7)", `Acme \(Nepal\) Pvt. Ltd.`, "(Consulting)", "(1200)", "(Travel)", "(Pay within 30 days)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("pdf text missing %q", want)
		}
	}

	status, body := h.call("GET", "/api/invoice.json", tok, nil)
	raw, err := base64.StdEncoding.DecodeString(Stringify(dig(body, "content_base64")))
	if status != 200 || err != nil || !bytes.HasPrefix(raw, []byte("%PDF-")) || dig(body, "filename") != "document.pdf" || len(Stringify(dig(body, "sha256"))) != 64 {
		t.Fatalf("base64: %d %v", status, dig(body, "filename"))
	}
}
