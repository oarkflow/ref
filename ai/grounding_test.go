package ai

import "testing"

func TestGroundedPromptRedactsAndValidatesCitations(t *testing.T) {
	prompt, err := BuildGroundedPrompt("Answer the question", []Document{
		{ID: "doc-b", Title: "B", Text: "secret-b"},
		{ID: "doc-a", Title: "A", Text: "secret-a"},
	}, GroundingPolicy{MaxDocuments: 2, Redact: func(value string) string {
		if value == "secret-a" {
			return "[REDACTED]"
		}
		return value
	}})
	if err != nil {
		t.Fatal(err)
	}
	if prompt.Documents[0].ID != "doc-a" || prompt.SystemText() == "" {
		t.Fatalf("unexpected prompt: %+v", prompt)
	}
	answer, err := prompt.ValidateAnswer("The value is [doc-a].", true)
	if err != nil || len(answer.Citations) != 1 || answer.Citations[0] != "doc-a" {
		t.Fatalf("unexpected answer: %+v err=%v", answer, err)
	}
	if _, err := prompt.ValidateAnswer("The value is [missing].", true); err == nil {
		t.Fatal("expected unknown citation error")
	}
}

func TestGroundedPromptTruncatesWithoutSplittingRunes(t *testing.T) {
	prompt, err := BuildGroundedPrompt("Answer", []Document{{ID: "a", Text: "αβγδ"}}, GroundingPolicy{MaxCharacters: 3})
	if err != nil {
		t.Fatal(err)
	}
	if prompt.Documents[0].Text != "αβ…" {
		t.Fatalf("unexpected truncated text: %q", prompt.Documents[0].Text)
	}
}
