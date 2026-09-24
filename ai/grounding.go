package ai

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type Document struct {
	ID    string
	Title string
	Text  string
	URI   string
}

type GroundingPolicy struct {
	MaxDocuments     int
	MaxCharacters    int
	RequireCitations bool
	Redact           func(string) string
}

type GroundedPrompt struct {
	Instruction      string
	Documents        []Document
	RequireCitations bool
}

type GroundedAnswer struct {
	Text       string
	Citations  []string
	Ungrounded bool
}

var citationPattern = regexp.MustCompile(`\[([A-Za-z0-9_.:-]+)\]`)

func BuildGroundedPrompt(instruction string, documents []Document, policy GroundingPolicy) (GroundedPrompt, error) {
	if strings.TrimSpace(instruction) == "" {
		return GroundedPrompt{}, errors.New("ai: grounding instruction is required")
	}
	if policy.MaxDocuments <= 0 {
		policy.MaxDocuments = 8
	}
	if policy.MaxCharacters <= 0 {
		policy.MaxCharacters = 12000
	}
	if len(documents) == 0 {
		return GroundedPrompt{}, errors.New("ai: at least one grounding document is required")
	}
	selected := append([]Document(nil), documents...)
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	if len(selected) > policy.MaxDocuments {
		selected = selected[:policy.MaxDocuments]
	}
	seen := make(map[string]struct{}, len(selected))
	remaining := policy.MaxCharacters
	for i := range selected {
		if selected[i].ID == "" {
			return GroundedPrompt{}, errors.New("ai: grounding document id is required")
		}
		if _, ok := seen[selected[i].ID]; ok {
			return GroundedPrompt{}, fmt.Errorf("ai: duplicate grounding document %q", selected[i].ID)
		}
		seen[selected[i].ID] = struct{}{}
		if policy.Redact != nil {
			selected[i].Text = policy.Redact(selected[i].Text)
			selected[i].Title = policy.Redact(selected[i].Title)
		}
		if remaining <= 0 {
			selected[i].Text = ""
			continue
		}
		textLength := utf8.RuneCountInString(selected[i].Text)
		if textLength > remaining {
			selected[i].Text = truncate(selected[i].Text, remaining)
			textLength = remaining
		}
		remaining -= textLength
	}
	return GroundedPrompt{Instruction: strings.TrimSpace(instruction), Documents: selected, RequireCitations: policy.RequireCitations}, nil
}

func (p GroundedPrompt) SystemText() string {
	var b strings.Builder
	b.WriteString("Answer only from the numbered grounding documents. Cite every factual claim with the matching document id in square brackets. If the documents do not establish an answer, say that the answer is not grounded.\n\n")
	for _, document := range p.Documents {
		fmt.Fprintf(&b, "[%s] %s\n%s\n\n", document.ID, document.Title, document.Text)
	}
	return b.String()
}

func (p GroundedPrompt) ValidateAnswer(answer string, requireCitation ...bool) (GroundedAnswer, error) {
	requireCitations := p.RequireCitations
	if len(requireCitation) > 0 {
		requireCitations = requireCitation[0]
	}
	allowed := make(map[string]struct{}, len(p.Documents))
	for _, document := range p.Documents {
		allowed[document.ID] = struct{}{}
	}
	matches := citationPattern.FindAllStringSubmatch(answer, -1)
	citations := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		id := match[1]
		if _, ok := allowed[id]; !ok {
			return GroundedAnswer{}, fmt.Errorf("ai: unknown citation %q", id)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		citations = append(citations, id)
	}
	if requireCitations && len(citations) == 0 {
		return GroundedAnswer{Citations: citations, Ungrounded: true}, errors.New("ai: answer has no grounding citation")
	}
	return GroundedAnswer{Text: answer, Citations: citations, Ungrounded: len(citations) == 0}, nil
}

func truncate(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}
