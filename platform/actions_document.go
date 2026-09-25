package platform

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/oarkflow/ref/document"
)

// document.pdf renders a PDF from a declarative layout whose text is
// templates over the node's facts:
//
//	node "pdf" {
//	  uses "document.pdf"
//	  requires [invoice]
//	  provides [pdf]
//	  config {
//	    title "Invoice {{invoice.number}}"
//	    subtitle "Issued {{invoice.date}}"
//	    footer "Acme Ltd - VAT 123456"
//	    filename "invoice-{{invoice.number}}.pdf"
//	    blocks [
//	      { kind "fields" fields [ { label "Customer" value "{{invoice.customer}}" } ] },
//	      { kind "table" rows "invoice.lines" columns [
//	          { label "Item" value "{{row.name}}" }, { label "Amount" value "{{row.amount}}" } ] },
//	      { kind "notice" text "Pay within 30 days" }
//	    ]
//	  }
//	}
//
// With output "download" (default) the node publishes a file the route sends
// as-is; with output "base64" it publishes {filename, content_type,
// content_base64, size, sha256} to store or attach.

type docBlockTmpl struct {
	kind    string
	text    *Template
	fields  [][2]*Template
	rows    string
	columns [][2]*Template // label (constant), value template
}

func registerDocumentActions(r *Registry) {
	mustAction(r, "document.pdf", ActionFactoryFunc(buildDocumentPDF), ActionInfo{
		Family: "data", Kind: "pure",
		Summary:  "Render a PDF (title, fields, paragraphs, tables, notices) from templates over the node's facts",
		Provides: "The PDF as a download, or {filename, content_base64, sha256} with output base64",
		Config: []ConfigField{
			{Name: "title", Type: "template"},
			{Name: "subtitle", Type: "template"},
			{Name: "footer", Type: "template"},
			{Name: "filename", Type: "template", Default: "document.pdf"},
			{Name: "output", Type: "string", Default: "download", Summary: "download | base64"},
			{Name: "letter", Type: "bool", Default: "false", Summary: "US Letter instead of A4"},
			{Name: "blocks", Type: "[]object", Summary: "{kind heading|text|notice|fields|table|rule|spacer, text, fields [{label value}], rows (fact path), columns [{label value}]}"},
		},
	})
}

func buildDocumentPDF(build BuildContext, spec NodeSpec) (Action, error) {
	if err := rejectUnknownConfig("document.pdf", spec.Config, "title", "subtitle", "footer", "filename", "output", "letter", "blocks"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	tmpl := func(key, fallback string) (*Template, error) {
		t, err := configTemplate(spec.Config, key, fallback)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", spec.Name, err)
		}
		return t, nil
	}
	title, err := tmpl("title", "")
	if err != nil {
		return nil, err
	}
	subtitle, err := tmpl("subtitle", "")
	if err != nil {
		return nil, err
	}
	footer, err := tmpl("footer", "")
	if err != nil {
		return nil, err
	}
	filename, err := tmpl("filename", "document.pdf")
	if err != nil {
		return nil, err
	}
	output := configString(spec.Config, "output", "download")
	if output != "download" && output != "base64" {
		return nil, fmt.Errorf("node %q: output must be download or base64", spec.Name)
	}
	letter := configBool(spec.Config, "letter", false)
	var blocks []docBlockTmpl
	for i, raw := range configBlocks(spec.Config, "blocks") {
		b := docBlockTmpl{kind: configString(raw, "kind", document.Paragraph)}
		where := fmt.Sprintf("node %q: blocks[%d]", spec.Name, i)
		switch b.kind {
		case document.Heading, document.Paragraph, document.Notice, document.Rule, document.Spacer, document.Fields, document.Table:
		default:
			return nil, fmt.Errorf("%s: unknown kind %q", where, b.kind)
		}
		if b.text, err = configTemplate(raw, "text", ""); err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
		for j, f := range configBlocks(raw, "fields") {
			label, err1 := configTemplate(f, "label", "")
			value, err2 := configTemplate(f, "value", "")
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("%s: fields[%d]: invalid template", where, j)
			}
			b.fields = append(b.fields, [2]*Template{label, value})
		}
		b.rows = configString(raw, "rows", "")
		for j, c := range configBlocks(raw, "columns") {
			label, err1 := configTemplate(c, "label", "")
			value, err2 := configTemplate(c, "value", "")
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("%s: columns[%d]: invalid template", where, j)
			}
			b.columns = append(b.columns, [2]*Template{label, value})
		}
		if b.kind == document.Table && (b.rows == "" || len(b.columns) == 0) {
			return nil, fmt.Errorf("%s: a table needs rows (a fact path) and columns", where)
		}
		blocks = append(blocks, b)
	}
	render := func(t *Template, env Env) string {
		if t == nil {
			return ""
		}
		s, err := t.Render(env)
		if err != nil {
			return ""
		}
		return s
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		doc := document.Doc{Title: render(title, env), Subtitle: render(subtitle, env), Footer: render(footer, env),
			Letter: letter, Created: ctx.Now}
		for _, b := range blocks {
			out := document.Block{Kind: b.kind, Text: render(b.text, env)}
			for _, f := range b.fields {
				out.Fields = append(out.Fields, document.Field{Label: render(f[0], env), Value: render(f[1], env)})
			}
			if b.kind == document.Table {
				for _, c := range b.columns {
					out.Columns = append(out.Columns, render(c[0], env))
				}
				raw, _ := resolvePath(env, b.rows)
				list, _ := requiredList(raw, b.rows)
				for _, item := range list {
					rowEnv := make(Env, len(env)+1)
					for k, v := range env {
						rowEnv[k] = v
					}
					rowEnv["row"] = item
					cells := make([]string, len(b.columns))
					for i, c := range b.columns {
						cells[i] = render(c[1], rowEnv)
					}
					out.Rows = append(out.Rows, cells)
				}
			}
			doc.Blocks = append(doc.Blocks, out)
		}
		pdf, err := document.Render(doc)
		if err != nil {
			return ActionResult{}, err
		}
		return singleOutput(spec, pdfOutput(pdf, render(filename, env), output)), nil
	}), nil
}

func pdfOutput(pdf []byte, filename, output string) any {
	if !strings.HasSuffix(strings.ToLower(filename), ".pdf") {
		filename += ".pdf"
	}
	if output == "base64" {
		sum := sha256.Sum256(pdf)
		return map[string]any{"filename": filename, "content_type": "application/pdf",
			"content_base64": base64.StdEncoding.EncodeToString(pdf), "size": len(pdf), "sha256": hex.EncodeToString(sum[:])}
	}
	return RawResponse{ContentType: "application/pdf", Filename: filename, Body: pdf}
}

// certificatePDF renders an issued pipeline certificate: its subject fields
// (labelled from the pipeline's inputs), validity, and the verification code
// and URL anyone can use to check it.
func (h *pipelineHandler) certificatePDF(ctx *ActionContext) (ActionResult, error) {
	c, e, actor, err := h.load(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	allowed := actor.ID != "" && actor.ID == c.CreatedBy
	for i := range e.C.Def.Stages {
		allowed = allowed || e.CanView(c, &e.C.Def.Stages[i], actor)
	}
	if !allowed {
		return ActionResult{}, permissionDenied("you may not view this case")
	}
	key := h.param(ctx, "number")
	idx := -1
	for i, cc := range c.Certificates {
		if (key == "" || cc.Number == key || cc.ID == key || cc.Code == key) && cc.Revoked == nil {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ActionResult{}, notFound("certificate", key)
	}
	cc := c.Certificates[idx]
	status := e.Verify(cc)
	labels := map[string]string{}
	for _, f := range e.C.Def.Forms {
		inputs, _ := e.C.FormInputs(f.Name)
		for _, in := range inputs {
			labels[f.Name+"."+in.Name] = orDefaultString(in.Label, in.Name)
		}
	}
	for _, st := range e.C.Def.Stages {
		for _, f := range st.Forms {
			inputs, _ := e.C.FormInputs(f.Name)
			for _, in := range inputs {
				labels[f.Name+"."+in.Name] = orDefaultString(in.Label, in.Name)
			}
		}
	}
	var fields []document.Field
	var paths []string
	if spec, ok := e.C.Certificate(cc.Name); ok {
		paths = spec.Fields
	}
	for _, path := range paths {
		if v, ok := cc.Subject[path]; ok {
			fields = append(fields, document.Field{Label: orDefaultString(labels[path], path), Value: Stringify(v)})
		}
	}
	validity := []document.Field{
		{Label: "Certificate number", Value: cc.Number},
		{Label: "Case", Value: cc.CaseNumber},
		{Label: "Issued", Value: cc.IssuedAt.UTC().Format("2 January 2006 15:04 MST")},
	}
	if cc.ExpiresAt != nil {
		validity = append(validity, document.Field{Label: "Valid until", Value: cc.ExpiresAt.UTC().Format("2 January 2006")})
	}
	verify := "Verification code: " + cc.Code
	if prefix := configString(h.spec.Config, "verify_url", ""); prefix != "" {
		verify += " - verify at " + strings.TrimRight(prefix, "/") + "/" + cc.Code
	}
	if !status.Valid {
		verify = "NOT VALID: " + status.Reason + ". " + verify
	}
	title := orDefaultString(cc.Title, orDefaultString(e.C.Def.Title, "Certificate"))
	doc := document.Doc{
		Title:    title,
		Subtitle: orDefaultString(e.C.Def.Title, c.Pipeline) + " - certificate " + cc.Number,
		Footer:   "Fingerprint " + cc.Hash[:16] + "  -  " + cc.Code,
		Created:  time.Now(),
		Blocks: []document.Block{
			{Kind: document.Fields, Fields: fields},
			{Kind: document.Heading, Text: "Validity"},
			{Kind: document.Fields, Fields: validity},
			{Kind: document.Notice, Text: verify},
		},
	}
	pdf, err := document.Render(doc)
	if err != nil {
		return ActionResult{}, err
	}
	return singleOutput(h.spec, pdfOutput(pdf, strings.ToLower(cc.Number), "download")), nil
}
