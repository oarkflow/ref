package pipeline

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func fieldsEngine(t *testing.T, fields bool) (*Engine, *Case, *clock) {
	return policyEngine(t, func(d *Definition) {
		d.Information = &InfoPolicy{To: []string{"creator", "previous", "user"}, Fields: fields, Due: "2h"}
		d.Inputs = append(d.Inputs, Input{Name: "mrn", Kind: KindText, Pattern: `^M\d+$`, Label: "MRN"}, Input{Name: "kind", Kind: KindSelect, Options: []string{"ED", "OP"}})
		d.Forms[0].Fields = append(d.Forms[0].Fields, "mrn", "kind")
	})
}

func TestAQuestionCanAskForInputsAndTheAnswerIsMergedAndLogged(t *testing.T) {
	ctx := context.Background()

	// Off by default: free text only.
	e, c, _ := fieldsEngine(t, false)
	if _, _, err := e.RequestInformationTo(ctx, c, pCoder, "fill these", InfoOptions{Fields: []string{"f.mrn"}}); err == nil {
		t.Fatal("asking for inputs needs information fields")
	}

	e, c, _ = fieldsEngine(t, true)
	if _, _, err := e.RequestInformationTo(ctx, c, pCoder, "x", InfoOptions{Fields: []string{"f.nope"}}); err == nil {
		t.Fatal("unknown inputs are refused")
	}
	asked, item, err := e.RequestInformationTo(ctx, c, pCoder, "what is the MRN and kind?", InfoOptions{Fields: []string{"f.mrn", "f.kind"}})
	if err != nil || len(item.Fields) != 2 {
		t.Fatalf("ask: %v %+v", err, item)
	}
	if _, _, err := e.SubmitInformation(ctx, asked, pNurse, map[string]any{"answer": "see chart"}); err == nil {
		t.Fatal("every input asked for must be answered")
	}
	if _, _, err := e.SubmitInformation(ctx, asked, pNurse, map[string]any{"data": map[string]any{"f": map[string]any{"mrn": "bad", "kind": "ED"}}}); err == nil || !strings.Contains(err.Error(), "invalid") && !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("answers are validated like any entry: %v", err)
	}
	if _, _, err := e.SubmitInformation(ctx, asked, pNurse, map[string]any{"data": map[string]any{"f.mrn": "M42", "f.kind": "XX"}}); err == nil {
		t.Fatal("options are checked")
	}
	done, _, err := e.SubmitInformation(ctx, asked, pNurse, map[string]any{"answer": "from the chart", "data": map[string]any{"f": map[string]any{"mrn": "M42", "kind": "ED"}}})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := done.Get("f.mrn"); v != "M42" {
		t.Fatalf("the answer is in the case data: %v", v)
	}
	last := done.History[len(done.History)-1]
	if last.Action != "information.submitted" || !slices.Contains(last.Changes, "f.mrn") || !slices.Contains(last.Changes, "f.kind") {
		t.Fatalf("the history says which data the answer changed: %+v", last)
	}
	if done.Status == CaseRequiresInformation {
		t.Fatal("answered")
	}
}

func TestInformationNotificationRecipientsAndQuestionToken(t *testing.T) {
	ctx := context.Background()
	e, c, _ := fieldsEngine(t, true)
	who := func(c *Case, to string) []string {
		return e.recipients(ctx, c, Event{Name: "case.information_requested", Stage: c.Stage}, []string{to})
	}
	creator, _, err := e.RequestInformation(ctx, c, pCoder, "which chart?")
	if err != nil {
		t.Fatal(err)
	}
	if got := who(creator, "asked"); !slices.Equal(got, []string{"n1"}) {
		t.Fatalf("asked (creator): %v", got)
	}
	if got := who(creator, "asker"); !slices.Equal(got, []string{"c1"}) {
		t.Fatalf("asker: %v", got)
	}
	prev, _, _ := e.RequestInformationTo(ctx, c, pCoder, "q", InfoOptions{To: "previous"})
	if got := who(prev, "asked"); !slices.Equal(got, []string{"n1", "n2"}) {
		t.Fatalf("asked (previous stage's nurses): %v", got)
	}
	user, _, _ := e.RequestInformationTo(ctx, c, pCoder, "q", InfoOptions{To: "user", Assignee: "n2"})
	if got := who(user, "asked"); !slices.Equal(got, []string{"n2"}) {
		t.Fatalf("asked (named): %v", got)
	}
	if got := renderNotice("{case.number}: {question}", creator, Event{}); !strings.HasSuffix(got, ": which chart?") {
		t.Fatalf("the question token: %q", got)
	}
}

func TestOverdueShowsWithoutWaitingForTheSweep(t *testing.T) {
	ctx := context.Background()
	e, c, clk := fieldsEngine(t, true)
	c, _, err := e.RequestInformation(ctx, c, pCoder, "q")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := e.View(c, pCoder, "")
	if v.Information == nil || v.Information.Overdue {
		t.Fatalf("not overdue yet: %+v", v.Information)
	}
	clk.add(3 * time.Hour)
	v, _ = e.View(c, pCoder, "")
	if v.Information == nil || !v.Information.Overdue {
		t.Fatalf("overdue as soon as the due time passes, before any sweep: %+v", v.Information)
	}
}

// BCL never binds a key named body, so a notify rule's text is written as message.
func TestNotifyMessageIsTheBody(t *testing.T) {
	d := resumeToDef()
	d.Notify = []NotifyRule{{Event: "case.information_requested", To: []string{"asked"}, Message: "Question: {info.reason}"}}
	c, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Def.Notify[0].Body; got != "Question: {info.reason}" {
		t.Fatalf("message becomes the body: %q", got)
	}
}

// A role means the people of that role who work where the case is.
func TestRoleRecipientsAreLimitedToThePeopleWhoWorkWhereTheCaseIs(t *testing.T) {
	ctx := context.Background()
	e, c, _ := policyEngine(t, func(d *Definition) {
		d.Workers = []Worker{
			{ID: "n1", Roles: []string{"nurse"}, OrgUnits: []string{"a"}},
			{ID: "n2", Roles: []string{"nurse"}, OrgUnits: []string{"b"}},
			{ID: "n3", Roles: []string{"nurse"}}, // not tied to a unit: works anywhere
			{ID: "c1", Roles: []string{"coder"}},
		}
		d.Information = &InfoPolicy{To: []string{"creator", "previous"}}
	})
	e.OrgCovers = func(_ string, units []string, unit string) bool { return slices.Contains(units, unit) }
	c.OrgUnit = "a"
	asked, _, err := e.RequestInformationTo(ctx, c, pCoder, "q", InfoOptions{To: "previous"})
	if err != nil {
		t.Fatal(err)
	}
	got := e.recipients(ctx, asked, Event{Name: "case.information_requested", Stage: asked.Stage}, []string{"asked"})
	if !slices.Equal(got, []string{"n1", "n3"}) {
		t.Fatalf("only the nurses of facility a (and unscoped ones): %v", got)
	}
}
