package pipeline

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PartyView tells staff who a stage is waiting for outside the workflow.
type PartyView struct {
	Party    string     `json:"party"`
	IssuedAt time.Time  `json:"issued_at"`
	Expires  time.Time  `json:"expires_at"`
	UsedAt   *time.Time `json:"submitted_at,omitempty"`
	// Status: waiting, submitted or expired.
	Status string `json:"status"`
	// Token is the single-use link for the party. Only the people who may run
	// the stage and its admins are given it.
	Token string `json:"token,omitempty"`
}

// linkToken rebuilds the signed token of an issued link.
func (e *Engine) linkToken(c *Case, l *Link) string {
	payload := c.ID + "." + l.Nonce + "." + strconv.FormatInt(l.Expires.Unix(), 10)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + e.linkMAC(payload)
}

// externalScope is what the outside party may fill: the configured scope, else
// every form on the stage's page.
func (e *Engine) externalScope(st *Stage) []string {
	if len(st.External.Scope) > 0 {
		return st.External.Scope
	}
	var out []string
	if st.Page != nil {
		for _, g := range st.Page.Groups {
			for _, f := range g.Forms {
				out = append(out, f)
			}
		}
	}
	return out
}

// issueExternal gives the stage's outside party a single-use link, on entry
// (and again when an admin reissues it). The link.issued event carries the
// token so a hook can send it.
func (e *Engine) issueExternal(c *Case, st *Stage, ss *StageState, by string, now time.Time) error {
	spec := st.External
	if len(e.SigningKey) == 0 {
		return badState("stage %q is handled by an outside party and needs a signing key", st.Name)
	}
	party := strings.TrimSpace(spec.Party)
	if spec.AssigneeFrom != "" {
		if v, ok := c.Get(spec.AssigneeFrom); ok && !isEmpty(v) {
			party = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	if party == "" {
		party = "outside party"
	}
	scope := e.externalScope(st)
	if len(scope) == 0 {
		return badState("stage %q gives its outside party nothing to fill", st.Name)
	}
	ttl := 7 * 24 * time.Hour
	if sp, err := ParseSpan(spec.TTL); err == nil && !sp.Zero() {
		ttl = sp.From(nil, now).Sub(now)
	}
	link := &Link{Nonce: randomID(), Party: party, Stage: st.Name, Scope: scope, IssuedBy: by, IssuedAt: now, Expires: now.Add(ttl)}
	if c.Links == nil {
		c.Links = map[string]*Link{}
	}
	if old := c.Links[ss.ExternalLink]; old != nil && old.UsedAt == nil {
		old.Expires = now.Add(-time.Second) // the old link stops working
	}
	c.Links[link.Nonce] = link
	ss.ExternalLink = link.Nonce
	c.History = append(c.History, Entry{At: now, Actor: by, Stage: st.Name, Action: "link_issued", To: party, Changes: scope})
	c.emit("link.issued", st.Name, by, now, map[string]any{"party": party, "expires_at": link.Expires, "token": e.linkToken(c, link)})
	return nil
}

// ReissueExternal replaces the outside party's link (it was lost, or expired).
func (e *Engine) ReissueExternal(_ context.Context, in *Case, actor Actor, stage string) (*Case, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	if st.External == nil {
		return nil, badState("stage %q is not handled by an outside party", stage)
	}
	if !isAdmin(st, actor) && !actor.HasAnyRole(st.Roles) && !actor.HasAnyRole(st.ExternalRoles) {
		return nil, forbidden("you may not reissue the link for stage %q", stage)
	}
	now := e.now()
	if err := e.issueExternal(c, st, ss, actor.ID, now); err != nil {
		return nil, err
	}
	c.UpdatedAt = now
	return c, nil
}

// partyView describes the outside party's link to staff who may see it.
func (e *Engine) partyView(c *Case, st *Stage, ss *StageState, actor Actor) *PartyView {
	if st.External == nil || ss.ExternalLink == "" {
		return nil
	}
	l := c.Links[ss.ExternalLink]
	if l == nil {
		return nil
	}
	pv := &PartyView{Party: l.Party, IssuedAt: l.IssuedAt, Expires: l.Expires, UsedAt: l.UsedAt, Status: "waiting"}
	switch {
	case l.UsedAt != nil:
		pv.Status = "submitted"
	case !e.now().Before(l.Expires):
		pv.Status = "expired"
	}
	if pv.Status == "waiting" && (isAdmin(st, actor) || actor.HasAnyRole(st.Roles) || actor.HasAnyRole(st.ExternalRoles)) {
		pv.Token = e.linkToken(c, l)
	}
	return pv
}
