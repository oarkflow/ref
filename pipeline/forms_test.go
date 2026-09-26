package pipeline

import (
	"context"
	"errors"
	"testing"
)

func TestSniffContentType(t *testing.T) {
	for _, tc := range []struct {
		content string
		want    string
	}{
		{"\xFF\xD8\xFF\xE0rest", "image/jpeg"},
		{"\x89PNG\r\n\x1a\nrest", "image/png"},
		{"GIF89a....", "image/gif"},
		{"RIFF\x00\x00\x00\x00WEBPVP8 ", "image/webp"},
		{"%PDF-1.4\n", "application/pdf"},
		{"PK\x03\x04....", "application/zip"},
		{"plain words\nand lines\n", "text/plain"},
		{"MZ\x90\x00\x03\x00\x00\x00", "application/octet-stream"},
	} {
		if got := SniffContentType([]byte(tc.content)); got != tc.want {
			t.Errorf("%q: got %s, want %s", tc.content, got, tc.want)
		}
	}
	if !acceptMatches([]string{"image/*"}, "image/png") || !acceptMatches([]string{".PDF"}, "application/pdf") ||
		acceptMatches([]string{"image/jpeg", ".pdf"}, "text/plain") {
		t.Fatal("acceptMatches")
	}
}

func formsDef() *Definition {
	return &Definition{
		Name: "permit",
		Forms: []Form{
			{Name: "who", Inputs: []Input{{Name: "name", Kind: KindText, Required: true}}},
			{Name: "docs", Inputs: []Input{{Name: "scan", Kind: KindFile, Accept: []string{"application/pdf"}, MaxBytes: 64}}},
		},
		Stages: []Stage{
			{Name: "apply", Public: true, ConfirmSubmit: true, Page: &Page{
				Groups:           []Group{{Name: "g", Forms: []string{"who", "docs"}}},
				Acknowledgements: []AcknowledgementSpec{{Name: "true", Text: "It is all true."}},
			}},
			{Name: "check", Roles: []string{"officer"}},
		},
	}
}

func TestEngineUploadAckConfirm(t *testing.T) {
	compiled, err := Compile(formsDef())
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(compiled)
	e.ManagedFiles = true
	ctx := context.Background()
	me := Actor{ID: "u1"}
	c, err := e.Start(ctx, me, StartOptions{Number: "P-1", Data: map[string]any{"who": map[string]any{"name": "A"}}})
	if err != nil {
		t.Fatal(err)
	}
	c.ID = "case_1"

	var ve *ValidationError
	if _, _, _, err := e.Attach(ctx, c, me, "apply", "docs.scan", Upload{Name: "x.pdf", Content: []byte("\xFF\xD8\xFF not a pdf")}); !errors.As(err, &ve) || ve.Fields[0].Rule != "content_type" {
		t.Fatalf("sniff: %v", err)
	}
	c2, ref, _, err := e.Attach(ctx, c, me, "apply", "docs.scan", Upload{Name: "a/b/x.pdf", Content: []byte("%PDF-1.4 tiny")})
	if err != nil || ref.Name != "x.pdf" || ref.Key != "case_1/"+ref.ID || ref.ContentType != "application/pdf" {
		t.Fatalf("attach: %v %+v", err, ref)
	}
	if got, err := e.File(c2, me, "docs.scan", ""); err != nil || got.SHA256 != ref.SHA256 {
		t.Fatalf("file: %v", err)
	}
	if err := VerifyFile(ref, []byte("%PDF-1.4 tinY")); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("verify: %v", err)
	}
	if _, err := e.File(c2, Actor{ID: "other"}, "docs.scan", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stranger: %v", err)
	}

	// Two-step: no token, then the ack is required, then a review commits.
	if _, err := e.Act(ctx, c2, me, "apply", "submit", ActInput{}); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("act without token: %v", err)
	}
	if _, err := e.Preview(ctx, c2, me, "apply", "submit", ActInput{}); !errors.As(err, &ve) || ve.Fields[0].Rule != "acknowledgement" {
		t.Fatalf("preview without ack: %v", err)
	}
	in := ActInput{Acknowledgements: Acks{"true": AckHash("It is all true.")}}
	r, err := e.Preview(ctx, c2, me, "apply", "submit", in)
	if err != nil {
		t.Fatal(err)
	}
	in.ConfirmToken = r.Token
	moved := c2.Clone()
	moved.Revision++
	if _, err := e.Act(ctx, moved, me, "apply", "submit", in); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("stale revision: %v", err)
	}
	if _, err := e.Act(ctx, c2, Actor{ID: "u2"}, "apply", "submit", in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other actor: %v", err)
	}
	done, err := e.Act(ctx, c2, me, "apply", "submit", in)
	if err != nil || done.Stage != "check" || len(done.Acknowledgements) != 1 || done.Acknowledgements[0].SHA256 != AckHash("It is all true.") {
		t.Fatalf("commit: %v", err)
	}
}
