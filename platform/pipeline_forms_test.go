package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pipeline form UX end to end: file uploads (multipart and base64 JSON)
// checked by magic bytes, size and count, checksummed and verified on
// download; info blocks and acknowledgements; and confirm-before-submit.

const formsApp = `
name "forms"

resource "db" {
  kind "database.sql"
  config {
    driver env("PF_DB_DRIVER", "sqlite")
    dsn env("PF_DSN")
  }
}

resource "jwt" {
  kind "auth.jwt"
  config {
    secret env.required("PF_JWT_SECRET")
  }
}

resource "files" {
  kind "storage.fs"
  config {
    dir env.required("PF_FILES")
  }
}

resource "cases" {
  kind "pipeline.cases"
  config {
    database "db"
    storage "files"
    signing_secret env.required("PF_SIGNING_SECRET")
  }
}

pipeline "permit" {
  subject ["applicant.name"]
  form "applicant" {
    input "name" {
      kind text
      required true
    }
  }
  form "documents" {
    input "photo" {
      label "Photo"
      kind file
      required true
      accept ["image/jpeg", "image/png"]
      max_bytes 1024
      pii true
    }
    input "evidence" {
      label "Evidence"
      kind file
      accept [".pdf", "image/*"]
      max_files 2
    }
  }
  stage "application" {
    public true
    confirm_submit true
    page {
      info "privacy" {
        title "Your data"
        body "We keep your documents for five years."
        style warning
        before "docs"
      }
      acknowledge "truthful" {
        title "Declaration"
        text "I declare that the information I have given is true."
      }
      group "details" {
        forms ["applicant"]
      }
      group "docs" {
        forms ["documents"]
      }
    }
  }
  stage "review" {
    roles ["officer"]
    page {
      group "all" {
        mode readonly
        forms ["applicant", "documents"]
      }
    }
    action "approve" {
      outcome approve
    }
  }
}

intent "start" {
  response "case"
  node "case" {
    uses "pipeline.start"
    resource "cases"
    kind effect
    requires [input]
    provides [case]
  }
}
intent "view" {
  response "view"
  node "view" {
    uses "pipeline.view"
    resource "cases"
    provides [view]
  }
}
intent "get" {
  response "case"
  node "case" {
    uses "pipeline.get"
    resource "cases"
    provides [case]
  }
}
intent "save" {
  response "view"
  node "view" {
    uses "pipeline.save"
    resource "cases"
    kind effect
    requires [input]
    provides [view]
  }
}
intent "act" {
  response "view"
  node "view" {
    uses "pipeline.act"
    resource "cases"
    kind effect
    requires [input]
    provides [view]
  }
}
intent "upload" {
  response "view"
  node "view" {
    uses "pipeline.upload"
    resource "cases"
    kind effect
    requires [input]
    provides [view]
  }
}
intent "erase" {
  response "r"
  node "r" {
    uses "pipeline.erase"
    resource "cases"
    kind effect
    requires [input]
    provides [r]
    config {
      roles ["dpo"]
    }
  }
}
intent "file" {
  response "file"
  node "file" {
    uses "pipeline.file"
    resource "cases"
    provides [file]
  }
}

route "start" { method POST path "/permits" intent "start" auth "jwt" status 201 }
route "view" { method GET path "/permits/:id" intent "view" auth "jwt" }
route "get" { method GET path "/permits/:id/history" intent "get" auth "jwt" }
route "save" { method PUT path "/permits/:id/stages/:stage" intent "save" auth "jwt" }
route "act" { method POST path "/permits/:id/stages/:stage/actions/:action" intent "act" auth "jwt" }
route "upload" { method POST path "/permits/:id/stages/:stage/files/:path" intent "upload" auth "jwt" max_body_bytes 1048576 }
route "erase" { method POST path "/erase" intent "erase" auth "jwt" }
route "file" { method GET path "/permits/:id/files/:path" intent "file" auth "jwt" }
`

func newFormsApp(t *testing.T) (*appHarness, string) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(formsApp), 0o600); err != nil {
		t.Fatal(err)
	}
	files := filepath.Join(dir, "files")
	env := map[string]string{
		"PF_DB_DRIVER":      "sqlite",
		"PF_DSN":            "file:" + filepath.Join(dir, "forms.db") + "?_pragma=busy_timeout(5000)",
		"PF_JWT_SECRET":     strings.Repeat("j", 40),
		"PF_SIGNING_SECRET": strings.Repeat("s", 40),
		"PF_FILES":          files,
	}
	if admin := os.Getenv("TEST_POSTGRES_DSN"); admin != "" {
		env["PF_DB_DRIVER"], env["PF_DSN"] = "pgx", freshPostgres(t, admin)
	}
	return newAppHarness(t, path, env), files
}

// Minimal files whose magic bytes identify them.
var (
	jpegBytes = append([]byte("\xFF\xD8\xFF\xE0\x00\x10JFIF\x00"), bytes.Repeat([]byte{0x42}, 64)...)
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x07}, 32)...)
	pdfBytes  = []byte("%PDF-1.7\n1 0 obj << >> endobj\n%%EOF\n")
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func cloneBytes(b []byte) []byte { return append([]byte(nil), b...) }

// uploadMultipart posts one file as multipart/form-data.
func (h *appHarness) uploadMultipart(path, token, filename, contentType string, content []byte) (int, any) {
	h.t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	header := make(map[string][]string)
	header["Content-Disposition"] = []string{`form-data; name="file"; filename="` + filename + `"`}
	header["Content-Type"] = []string{contentType}
	part, err := w.CreatePart(header)
	if err != nil {
		h.t.Fatal(err)
	}
	_, _ = part.Write(content)
	_ = w.Close()
	req, err := http.NewRequest("POST", h.base+path, &body)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded any
	_ = json.Unmarshal(raw, &decoded)
	return resp.StatusCode, decoded
}

// download fetches a file, returning the status, the body and its content type.
func (h *appHarness) download(path, token string) (int, []byte, string) {
	h.t.Helper()
	req, err := http.NewRequest("GET", h.base+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, resp.Header.Get("Content-Type")
}

func base64File(name string, content []byte) map[string]any {
	return map[string]any{"file": map[string]any{"filename": name, "content_base64": base64.StdEncoding.EncodeToString(content)}}
}

func firstDetail(body any) (string, string) {
	d := dig(body, "error", "details", 0)
	return Stringify(dig(d, "path")), Stringify(dig(d, "rule"))
}

func TestPipelineFormUploads(t *testing.T) {
	h, filesDir := newFormsApp(t)
	citizen := h.token("jwt", "citizen-1", nil, nil)
	stranger := h.token("jwt", "citizen-2", nil, nil)
	officer := h.token("jwt", "officer-1", []string{"officer"}, nil)

	status, body := h.call("POST", "/permits", citizen, map[string]any{"data": map[string]any{"applicant": map[string]any{"name": "Asha"}}})
	if status != 201 {
		t.Fatalf("start: %d %v", status, body)
	}
	id := Stringify(dig(body, "case", "id"))
	uploadPhoto := "/permits/" + id + "/stages/application/files/documents.photo"
	uploadEvidence := "/permits/" + id + "/stages/application/files/documents.evidence"

	// The view describes the file inputs' limits.
	if got := dig(body, "page", "groups", 1, "forms", 0, "inputs", 0, "max_bytes"); Stringify(got) != "1024" {
		t.Fatalf("photo max_bytes = %v", got)
	}

	// A text file named .jpg and declared image/jpeg is rejected by its bytes.
	status, body = h.call("POST", uploadPhoto, citizen, base64File("photo.jpg", []byte("#!/bin/sh\necho not a photo\n")))
	if path, rule := firstDetail(body); status != 422 || path != "documents.photo" || rule != "content_type" {
		t.Fatalf("sniffing: %d %v", status, body)
	}
	status, body = h.uploadMultipart(uploadPhoto, citizen, "photo.jpg", "image/jpeg", pdfBytes)
	if _, rule := firstDetail(body); status != 422 || rule != "content_type" {
		t.Fatalf("pdf posing as jpeg: %d %v", status, body)
	}
	// Over max_bytes.
	big := append(cloneBytes(jpegBytes), bytes.Repeat([]byte{1}, 2048)...)
	status, body = h.uploadMultipart(uploadPhoto, citizen, "big.jpg", "image/jpeg", big)
	if _, rule := firstDetail(body); status != 422 || rule != "max_bytes" {
		t.Fatalf("too large: %d %v", status, body)
	}

	// A valid photo over multipart, with its declared type ignored.
	status, body = h.uploadMultipart(uploadPhoto, citizen, "../../me.jpg", "application/octet-stream", jpegBytes)
	if status != 200 {
		t.Fatalf("upload: %d %v", status, body)
	}
	ref := dig(body, "files", 0).(map[string]any)
	if ref["sha256"] != sha256Hex(jpegBytes) || ref["content_type"] != "image/jpeg" || ref["name"] != "me.jpg" ||
		ref["uploaded_by"] != "citizen-1" || Stringify(ref["size"]) != Stringify(len(jpegBytes)) || ref["key"] == "" || ref["uploaded_at"] == "" {
		t.Fatalf("recorded file = %v", ref)
	}
	if v := dig(body, "page", "groups", 1, "forms", 0, "inputs", 0, "value", "sha256"); v != ref["sha256"] {
		t.Fatalf("case data holds %v", v)
	}

	// The same content again, anywhere in the case, is a duplicate.
	status, body = h.call("POST", uploadEvidence, citizen, base64File("again.jpg", jpegBytes))
	if path, rule := firstDetail(body); status != 422 || rule != "duplicate" || path != "documents.evidence" {
		t.Fatalf("duplicate: %d %v", status, body)
	}

	// A save cannot forge file metadata or point at another object.
	status, body = h.call("PUT", "/permits/"+id+"/stages/application", citizen, map[string]any{"data": map[string]any{
		"documents": map[string]any{"photo": map[string]any{"id": "file_forged", "key": "elsewhere", "sha256": sha256Hex(pngBytes)}}}})
	if _, rule := firstDetail(body); status != 422 || rule != "upload" {
		t.Fatalf("forged save: %d %v", status, body)
	}
	// Re-sending the recorded value unchanged is accepted.
	if status, body = h.call("PUT", "/permits/"+id+"/stages/application", citizen, map[string]any{"data": map[string]any{
		"documents": map[string]any{"photo": ref}}}); status != 200 {
		t.Fatalf("resave: %d %v", status, body)
	}

	// max_files: two files, then a third is refused.
	if status, body = h.call("POST", uploadEvidence, citizen, base64File("a.pdf", pdfBytes)); status != 200 {
		t.Fatalf("evidence 1: %d %v", status, body)
	}
	if status, body = h.uploadMultipart(uploadEvidence, citizen, "b.png", "image/png", pngBytes); status != 200 {
		t.Fatalf("evidence 2: %d %v", status, body)
	}
	if n := len(dig(body, "page", "groups", 1, "forms", 0, "inputs", 1, "value").([]any)); n != 2 {
		t.Fatalf("evidence holds %d files", n)
	}
	status, body = h.call("POST", uploadEvidence, citizen, base64File("c.pdf", append(cloneBytes(pdfBytes), '\n')))
	if _, rule := firstDetail(body); status != 422 || rule != "max_files" {
		t.Fatalf("max_files: %d %v", status, body)
	}

	// Downloads: the applicant and the officer get the exact bytes; someone
	// unrelated to the case gets nothing.
	status, raw, ctype := h.download("/permits/"+id+"/files/documents.photo", citizen)
	if status != 200 || !bytes.Equal(raw, jpegBytes) || ctype != "image/jpeg" {
		t.Fatalf("download: %d %q %q", status, ctype, raw)
	}
	if status, _, _ := h.download("/permits/"+id+"/files/documents.photo", stranger); status != 403 && status != 404 {
		t.Fatalf("stranger download: %d", status)
	}
	if status, _, _ := h.download("/permits/"+id+"/files/documents.photo", officer); status != 200 {
		t.Fatalf("officer download: %d", status)
	}

	// Tampering with the stored bytes is detected; the file is not served.
	stored := filepath.Join(filesDir, "pipeline", filepath.FromSlash(Stringify(ref["key"])))
	tampered := cloneBytes(jpegBytes)
	tampered[len(tampered)-1] ^= 0xff
	if err := os.WriteFile(stored, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	status, raw, _ = h.download("/permits/"+id+"/files/documents.photo", citizen)
	var failure any
	_ = json.Unmarshal(raw, &failure)
	if status != 500 || dig(failure, "error", "code") != "INTEGRITY_FAILED" {
		t.Fatalf("tampered download: %d %s", status, raw)
	}
	if bytes.Contains(raw, tampered) {
		t.Fatal("tampered content was served")
	}

	// Replacing the photo stores the new file and removes the old object.
	if status, body = h.uploadMultipart(uploadPhoto, citizen, "new.png", "image/png", pngBytes[:20]); status != 200 {
		t.Fatalf("replace: %d %v", status, body)
	}
	if _, err := os.Stat(stored); !os.IsNotExist(err) {
		t.Fatalf("replaced object still stored: %v", err)
	}
	photo := dig(body, "files", 0, "key")
	evidence := dig(body, "page", "groups", 1, "forms", 0, "inputs", 1, "value", 0, "key")
	if photo == nil || evidence == nil {
		t.Fatalf("keys: %v %v", photo, evidence)
	}
	if _, err := os.Stat(filepath.Join(filesDir, "pipeline", Stringify(photo))); err != nil {
		t.Fatalf("new photo not stored: %v", err)
	}

	// Erasure removes the stored object of a personal-data file with it;
	// other files stay.
	dpo := h.token("jwt", "dpo-1", []string{"dpo"}, nil)
	if status, body = h.call("POST", "/erase", dpo, map[string]any{"identifiers": map[string]any{"applicant.name": "Asha"}}); status != 200 ||
		dig(body, "receipts", 0, "outcome") != "anonymized" {
		t.Fatalf("erase: %d %v", status, body)
	}
	if _, err := os.Stat(filepath.Join(filesDir, "pipeline", Stringify(photo))); !os.IsNotExist(err) {
		t.Fatalf("erased photo still stored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filesDir, "pipeline", Stringify(evidence))); err != nil {
		t.Fatalf("evidence was removed: %v", err)
	}
}

func TestPipelineAcknowledgementAndConfirmSubmit(t *testing.T) {
	h, _ := newFormsApp(t)
	citizen := h.token("jwt", "citizen-1", nil, nil)
	officer := h.token("jwt", "officer-1", []string{"officer"}, nil)

	status, body := h.call("POST", "/permits", citizen, map[string]any{"data": map[string]any{"applicant": map[string]any{"name": "Asha"}}})
	if status != 201 {
		t.Fatalf("start: %d %v", status, body)
	}
	id := Stringify(dig(body, "case", "id"))
	base := "/permits/" + id
	submit := base + "/stages/application/actions/submit"

	// The page carries the info block and the acknowledgement with the hash
	// of its wording; the submit action is two-step.
	text := "I declare that the information I have given is true."
	if dig(body, "page", "info", 0, "name") != "privacy" || dig(body, "page", "info", 0, "style") != "warning" || dig(body, "page", "info", 0, "before") != "docs" {
		t.Fatalf("info = %v", dig(body, "page", "info"))
	}
	if dig(body, "page", "acknowledgements", 0, "sha256") != sha256Hex([]byte(text)) || dig(body, "page", "acknowledgements", 0, "text") != text {
		t.Fatalf("acknowledgements = %v", dig(body, "page", "acknowledgements"))
	}
	if dig(body, "actions", 0, "confirm_submit") != true {
		t.Fatalf("actions = %v", dig(body, "actions"))
	}
	if status, body = h.call("POST", base+"/stages/application/files/documents.photo", citizen, base64File("me.png", pngBytes)); status != 200 {
		t.Fatalf("upload: %d %v", status, body)
	}

	// The acknowledgement is required, and a stale wording is refused.
	status, body = h.call("POST", submit, citizen, map[string]any{})
	if path, rule := firstDetail(body); status != 422 || path != "acknowledgements.truthful" || rule != "acknowledgement" {
		t.Fatalf("no acknowledgement: %d %v", status, body)
	}
	status, body = h.call("POST", submit, citizen, map[string]any{"acknowledgements": map[string]any{"truthful": sha256Hex([]byte("older wording"))}})
	if _, rule := firstDetail(body); status != 422 || rule != "wording_changed" {
		t.Fatalf("stale wording: %d %v", status, body)
	}

	// First submit: validated, reviewed, nothing committed.
	acks := map[string]any{"truthful": sha256Hex([]byte(text))}
	request := map[string]any{"acknowledgements": acks, "data": map[string]any{"applicant": map[string]any{"name": "Asha Rai"}}}
	status, body = h.call("POST", submit, citizen, request)
	if status != 200 || dig(body, "confirmation_required") != true {
		t.Fatalf("review: %d %v", status, body)
	}
	token := Stringify(dig(body, "confirm_token"))
	fields := map[string]any{}
	for _, f := range dig(body, "review", "fields").([]any) {
		fields[Stringify(dig(f, "path"))] = dig(f, "value")
	}
	if fields["applicant.name"] != "Asha Rai" || dig(fields["documents.photo"], "name") != "me.png" || token == "" ||
		dig(body, "review", "acknowledgements", 0, "name") != "truthful" || dig(body, "review", "changes", 0) != "applicant.name" {
		t.Fatalf("review = %v", dig(body, "review"))
	}
	status, body = h.call("GET", base, citizen, nil)
	if dig(body, "case", "stage") != "application" || dig(body, "page", "groups", 0, "forms", 0, "inputs", 0, "value") != "Asha" {
		t.Fatalf("the review committed something: %d %v", status, body)
	}

	confirm := func(token string, data map[string]any) map[string]any {
		return map[string]any{"acknowledgements": acks, "data": data, "confirm_token": token}
	}
	named := func(name string) map[string]any {
		return map[string]any{"applicant": map[string]any{"name": name}}
	}

	// Different data than reviewed invalidates the token.
	status, body = h.call("POST", submit, citizen, confirm(token, named("Someone Else")))
	if status != 409 || dig(body, "error", "code") != "CONFIRMATION_INVALID" {
		t.Fatalf("changed data: %d %v", status, body)
	}
	// A forged token is refused.
	forged := token[:len(token)-2] + map[bool]string{true: "AA", false: "BB"}[!strings.HasSuffix(token, "AA")]
	if status, body = h.call("POST", submit, citizen, confirm(forged, named("Asha Rai"))); status != 409 {
		t.Fatalf("forged token: %d %v", status, body)
	}
	// A change to the case after the review invalidates it too.
	if status, body = h.call("PUT", base+"/stages/application", citizen, map[string]any{"data": named("Asha")}); status != 200 {
		t.Fatalf("save: %d %v", status, body)
	}
	status, body = h.call("POST", submit, citizen, confirm(token, named("Asha Rai")))
	if status != 409 || dig(body, "error", "code") != "CONFIRMATION_INVALID" || !strings.Contains(Stringify(dig(body, "error", "message")), "changed") {
		t.Fatalf("stale revision: %d %v", status, body)
	}

	// Review again, then commit with the token.
	status, body = h.call("POST", submit, citizen, request)
	if status != 200 {
		t.Fatalf("second review: %d %v", status, body)
	}
	token = Stringify(dig(body, "confirm_token"))
	status, body = h.call("POST", submit, citizen, confirm(token, named("Asha Rai")))
	if status != 200 || dig(body, "case", "stage") != "review" {
		t.Fatalf("commit: %d %v", status, body)
	}
	// The token is spent: the case moved on.
	if status, body = h.call("POST", submit, citizen, confirm(token, named("Asha Rai"))); status == 200 {
		t.Fatalf("replayed token: %d %v", status, body)
	}

	// The acceptance is recorded with who, when and the wording's hash.
	status, body = h.call("GET", base+"/history", officer, nil)
	rec := dig(body, "acknowledgements", 0)
	if status != 200 || dig(rec, "name") != "truthful" || dig(rec, "by") != "citizen-1" || dig(rec, "sha256") != sha256Hex([]byte(text)) ||
		dig(rec, "text") != text || dig(rec, "stage") != "application" || dig(rec, "at") == nil {
		t.Fatalf("acknowledgement record: %d %v", status, dig(body, "acknowledgements"))
	}

	// The officer's approve action is not two-step: it commits directly.
	if status, body = h.call("POST", base+"/stages/review/actions/approve", officer, map[string]any{}); status != 200 || dig(body, "case", "status") != "approved" {
		t.Fatalf("approve: %d %v", status, body)
	}
}

func TestValidatePipelineFormSyntax(t *testing.T) {
	opts := DefaultLoadOptions()
	opts.Env = func(name string) (string, bool) {
		if name == "PF_DSN" {
			return "file:" + filepath.Join(t.TempDir(), "never.db"), true
		}
		return "", false
	}
	if r := Validate(context.Background(), []byte(formsApp), ".", opts); !r.Valid {
		t.Fatalf("forms app should validate: %v", r.Errors)
	}
	for _, tc := range []struct{ from, to, want string }{
		{`text "I declare that the information I have given is true."`, `title "no text"`, `acknowledge "truthful" needs text`},
		{`style warning`, `style loud`, `style "loud"`},
		{`before "docs"`, `before "nowhere"`, `unknown group "nowhere"`},
		{"kind text\n      required true", "kind text\n      max_files 3\n      required true", "file inputs only"},
	} {
		bad := strings.Replace(formsApp, tc.from, tc.to, 1)
		r := Validate(context.Background(), []byte(bad), ".", opts)
		if r.Valid || !strings.Contains(strings.Join(r.Errors, "\n"), tc.want) {
			t.Errorf("%s -> %s: want error %q, got %v", tc.from, tc.to, tc.want, r.Errors)
		}
	}
}
