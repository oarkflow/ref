package pipeline

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SealedMarker stands in case data for a sealed value until it is opened.
const SealedMarker = "[sealed]"

// SealedValue is an encrypted input value (AES-256-GCM, bound to the case
// and path so a ciphertext cannot be moved to another case or field).
type SealedValue struct {
	Nonce      string    `json:"nonce"`
	Ciphertext string    `json:"ciphertext"`
	SealedAt   time.Time `json:"sealed_at"`
	By         string    `json:"by,omitempty"`
	// Digest is the SHA-256 of the plaintext, disclosed at opening so anyone
	// can check the opened value is the one submitted.
	Digest string `json:"digest"`
}

// SealOpening tracks approvals to open a case's sealed values.
type SealOpening struct {
	Approvals []Approval `json:"approvals,omitempty"`
	OpenedAt  *time.Time `json:"opened_at,omitempty"`
}

// ErrNoSealKey is returned when a pipeline with sealed inputs has no key.
var ErrNoSealKey = errors.New("pipeline: sealed inputs need a seal key")

func (e *Engine) sealAEAD() (cipher.AEAD, error) {
	if len(e.SealKey) == 0 {
		return nil, ErrNoSealKey
	}
	key := sha256.Sum256(e.SealKey) // any length secret -> 256-bit key
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (c *Case) opened() bool { return c.SealOpening != nil && c.SealOpening.OpenedAt != nil }

// sealValue encrypts a value for path. Before opening, case data holds only the
// marker; after opening, sealed inputs are stored in the clear.
func (e *Engine) sealValue(c *Case, path string, value any, by string) error {
	if c.opened() {
		c.Set(path, value)
		return nil
	}
	aead, err := e.sealAEAD()
	if err != nil {
		return err
	}
	plain, err := json.Marshal(value)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sum := sha256.Sum256(plain)
	ct := aead.Seal(nil, nonce, plain, []byte(c.ID+"|"+path))
	if c.Sealed == nil {
		c.Sealed = map[string]SealedValue{}
	}
	c.Sealed[path] = SealedValue{
		Nonce: base64.StdEncoding.EncodeToString(nonce), Ciphertext: base64.StdEncoding.EncodeToString(ct),
		SealedAt: e.now(), By: by, Digest: hex.EncodeToString(sum[:]),
	}
	c.Set(path, SealedMarker)
	return nil
}

func (e *Engine) unseal(c *Case, path string, sv SealedValue) (any, error) {
	aead, err := e.sealAEAD()
	if err != nil {
		return nil, err
	}
	nonce, err1 := base64.StdEncoding.DecodeString(sv.Nonce)
	ct, err2 := base64.StdEncoding.DecodeString(sv.Ciphertext)
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("pipeline: sealed %s is corrupt", path)
	}
	plain, err := aead.Open(nil, nonce, ct, []byte(c.ID+"|"+path))
	if err != nil {
		return nil, fmt.Errorf("pipeline: sealed %s cannot be opened with this key", path)
	}
	var v any
	if err := json.Unmarshal(plain, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// openAfter resolves the seal policy's earliest opening time for the case.
func (e *Engine) openAfter(c *Case) (time.Time, bool) {
	p := e.C.Def.Seal
	if p == nil || p.OpenAfter == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, p.OpenAfter); err == nil {
		return t, true
	}
	if v, ok := c.Get(p.OpenAfter); ok {
		if t, ok := parseDay(fmt.Sprint(v), false); ok {
			return t, true
		}
	}
	// A path that is not set yet means the deadline is unknown: stay sealed.
	return time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), true
}

// ApproveOpening records one officer's approval to open the case's sealed
// values. Once the quorum of distinct approvers is reached — and not before
// the policy's open_after time — every sealed value is decrypted into the
// case data, with its digest disclosed in the history.
func (e *Engine) ApproveOpening(ctx context.Context, in *Case, actor Actor, comment string) (*Case, error) {
	c := in.Clone()
	p := e.C.Def.Seal
	if p == nil || len(c.Sealed) == 0 {
		return nil, badState("this case has nothing sealed")
	}
	if c.opened() {
		return nil, badState("the sealed values are already open")
	}
	if !actor.HasAnyRole(p.OpenRoles) {
		return nil, forbidden("you may not approve opening sealed values")
	}
	if isApplicant(c, actor) {
		return nil, forbidden("the applicant may not open their own sealed values")
	}
	now := e.now()
	if at, ok := e.openAfter(c); ok && now.Before(at) {
		return nil, badState("sealed values cannot be opened before %s", at.UTC().Format(time.RFC3339))
	}
	if c.SealOpening == nil {
		c.SealOpening = &SealOpening{}
	}
	for _, a := range c.SealOpening.Approvals {
		if a.By == actor.ID {
			return nil, badState("you already approved the opening")
		}
	}
	c.SealOpening.Approvals = append(c.SealOpening.Approvals, Approval{By: actor.ID, At: now, Comment: comment})
	quorum := max(1, p.Quorum)
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Action: "seal_opening_approved",
		To: strconv.Itoa(len(c.SealOpening.Approvals)) + "/" + strconv.Itoa(quorum), Comment: comment})
	if len(c.SealOpening.Approvals) >= quorum {
		paths := make([]string, 0, len(c.Sealed))
		for path := range c.Sealed {
			paths = append(paths, path)
		}
		slices.Sort(paths)
		var digests []string
		for _, path := range paths {
			v, err := e.unseal(c, path, c.Sealed[path])
			if err != nil {
				return nil, err
			}
			c.Set(path, v)
			digests = append(digests, path+"="+c.Sealed[path].Digest)
		}
		c.SealOpening.OpenedAt = &now
		c.History = append(c.History, Entry{At: now, Actor: actor.ID, Action: "sealed_opened", Changes: digests})
		c.emit("sealed.opened", c.Stage, actor.ID, now, map[string]any{"paths": paths})
	}
	c.UpdatedAt = now
	return c, nil
}

// ---------------------------------------------------------------------------
// External-party links
// ---------------------------------------------------------------------------

// Link lets an outside party (a referee, an employer, an inspector) fill a
// few inputs of one stage without an account. It is single-use: submitting
// consumes it.
type Link struct {
	Nonce    string     `json:"nonce"`
	Party    string     `json:"party"`
	Stage    string     `json:"stage"`
	Scope    []string   `json:"scope"` // "form" or "form.input"
	IssuedBy string     `json:"issued_by"`
	IssuedAt time.Time  `json:"issued_at"`
	Expires  time.Time  `json:"expires_at"`
	UsedAt   *time.Time `json:"used_at,omitempty"`
}

func (l *Link) covers(form, input string) bool {
	return slices.Contains(l.Scope, form) || slices.Contains(l.Scope, form+"."+input)
}

func (e *Engine) linkMAC(payload string) string {
	mac := hmac.New(sha256.New, append([]byte("pipeline-link:"), e.SigningKey...))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// IssueLink creates a signed link for an outside party to fill scope (form
// names or form.input paths) at the current stage. It returns the token.
func (e *Engine) IssueLink(ctx context.Context, in *Case, actor Actor, stage, party string, scope []string, ttl time.Duration) (*Case, string, error) {
	c := in.Clone()
	st, _, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, "", err
	}
	if !actor.HasAnyRole(st.ExternalRoles) {
		return nil, "", forbidden("you may not invite outside parties at stage %q", stage)
	}
	if len(e.SigningKey) == 0 {
		return nil, "", badState("external links need a signing key")
	}
	if strings.TrimSpace(party) == "" || len(scope) == 0 {
		return nil, "", &ValidationError{Message: "a party and a scope are required", Fields: []FieldError{{Path: "scope", Rule: "required", Message: "choose what the party may fill"}}}
	}
	for _, s := range scope {
		form, input, _ := strings.Cut(s, ".")
		inputs, ok := e.C.FormInputs(form)
		if !ok || (input != "" && !slices.ContainsFunc(inputs, func(in Input) bool { return in.Name == input })) {
			return nil, "", fmt.Errorf("%w: scope %q", ErrNotFound, s)
		}
	}
	if ttl <= 0 || ttl > 30*24*time.Hour {
		ttl = 7 * 24 * time.Hour
	}
	now := e.now()
	link := &Link{Nonce: randomID(), Party: party, Stage: stage, Scope: scope, IssuedBy: actor.ID, IssuedAt: now, Expires: now.Add(ttl)}
	if c.Links == nil {
		c.Links = map[string]*Link{}
	}
	c.Links[link.Nonce] = link
	payload := c.ID + "." + link.Nonce + "." + strconv.FormatInt(link.Expires.Unix(), 10)
	token := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + e.linkMAC(payload)
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: stage, Action: "link_issued", To: party, Changes: scope})
	c.emit("link.issued", stage, actor.ID, now, map[string]any{"party": party, "expires_at": link.Expires})
	c.UpdatedAt = now
	return c, token, nil
}

// ParseLinkToken returns the case id a link token names, after checking its
// signature, so the host knows which case to load.
func (e *Engine) ParseLinkToken(token string) (caseID, nonce string, err error) {
	enc, mac, ok := strings.Cut(token, ".")
	raw, derr := base64.RawURLEncoding.DecodeString(enc)
	if !ok || derr != nil || len(e.SigningKey) == 0 || !hmac.Equal([]byte(mac), []byte(e.linkMAC(string(raw)))) {
		return "", "", forbidden("the link is not valid")
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 3 {
		return "", "", forbidden("the link is not valid")
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || e.now().After(time.Unix(exp, 0)) {
		return "", "", forbidden("the link has expired")
	}
	return parts[0], parts[1], nil
}

// LinkActor resolves a link token against its case into the actor the
// outside party acts as.
func (e *Engine) LinkActor(c *Case, token string) (Actor, error) {
	caseID, nonce, err := e.ParseLinkToken(token)
	if err != nil {
		return Actor{}, err
	}
	link := c.Links[nonce]
	if caseID != c.ID || link == nil {
		return Actor{}, forbidden("the link is not valid")
	}
	if link.UsedAt != nil {
		return Actor{}, forbidden("the link has already been used")
	}
	if e.now().After(link.Expires) {
		return Actor{}, forbidden("the link has expired")
	}
	return Actor{ID: "external:" + link.Party, Link: link}, nil
}

// LinkSubmit saves an outside party's inputs and consumes the link. It does
// not advance the stage: staff decide what happens next.
func (e *Engine) LinkSubmit(ctx context.Context, in *Case, actor Actor, data map[string]any) (*Case, error) {
	if actor.Link == nil {
		return nil, forbidden("not an external link")
	}
	c := in.Clone()
	link := c.Links[actor.Link.Nonce]
	st, ss, err := e.stageOpen(c, link.Stage)
	if err != nil {
		return nil, err
	}
	actor.Link = link
	changed, err := e.saveInto(c, actor, st.Name, data)
	if err != nil {
		return nil, err
	}
	var missing []FieldError
	for _, f := range e.missingRequired(c, st, ss, actor) {
		missing = append(missing, f)
	}
	if len(missing) > 0 {
		return nil, &ValidationError{Message: "some required fields are missing", Fields: missing}
	}
	now := e.now()
	link.UsedAt = &now
	e.refreshChecks(c, st.Name)
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: st.Name, Action: "link_submitted", Changes: changed})
	c.emit("link.submitted", st.Name, actor.ID, now, map[string]any{"party": link.Party, "changes": changed})
	c.UpdatedAt = now
	return c, nil
}
