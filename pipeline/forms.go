package pipeline

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Form UX: file uploads, page acknowledgements and confirm-before-submit.

// ---------------------------------------------------------------------------
// File uploads
// ---------------------------------------------------------------------------

// DefaultMaxUploadBytes caps an uploaded file when neither its input nor the
// engine sets a limit.
const DefaultMaxUploadBytes = 10 << 20

// ErrIntegrity is returned when a stored file no longer matches the checksum
// recorded when it was uploaded.
var ErrIntegrity = errors.New("pipeline: stored file failed its integrity check")

// FileRef is an uploaded file as recorded on the case, at the file input's
// data path. The content lives in the host's object storage under Key.
type FileRef struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type"`
	SHA256      string    `json:"sha256"`
	Key         string    `json:"key"`
	UploadedBy  string    `json:"uploaded_by"`
	UploadedAt  time.Time `json:"uploaded_at"`
}

// value is the plain-data form stored in the case, so expressions can read
// it (documents.photo.content_type).
func (f FileRef) value() map[string]any {
	return map[string]any{
		"id": f.ID, "name": f.Name, "size": f.Size, "content_type": f.ContentType, "sha256": f.SHA256,
		"key": f.Key, "uploaded_by": f.UploadedBy, "uploaded_at": f.UploadedAt.UTC().Format(time.RFC3339Nano),
	}
}

// fileRefs reads the files a file input holds: one object, or a list.
func fileRefs(v any) []FileRef {
	var items []any
	switch t := v.(type) {
	case map[string]any:
		items = []any{t}
	case []any:
		items = t
	default:
		return nil
	}
	var out []FileRef
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		raw, err := json.Marshal(m)
		if err != nil {
			continue
		}
		var ref FileRef
		if json.Unmarshal(raw, &ref) == nil && ref.ID != "" && ref.SHA256 != "" {
			out = append(out, ref)
		}
	}
	return out
}

// Upload is a file sent for a file input.
type Upload struct {
	Name    string
	Content []byte
}

// VerifyFile checks content read back from storage against the size and
// SHA-256 recorded at upload.
func VerifyFile(ref FileRef, content []byte) error {
	if int64(len(content)) != ref.Size {
		return fmt.Errorf("%w: %s is %d bytes, %d were uploaded", ErrIntegrity, ref.Name, len(content), ref.Size)
	}
	sum := sha256.Sum256(content)
	if !hmac.Equal([]byte(hex.EncodeToString(sum[:])), []byte(ref.SHA256)) {
		return fmt.Errorf("%w: %s does not match its recorded SHA-256", ErrIntegrity, ref.Name)
	}
	return nil
}

// SniffContentType identifies content by its magic bytes. The type a client
// declares is never trusted: a renamed executable must not pass as a photo.
// Unknown binary content is application/octet-stream; UTF-8 text without
// control characters is text/plain.
func SniffContentType(content []byte) string {
	has := func(prefix string) bool { return bytes.HasPrefix(content, []byte(prefix)) }
	switch {
	case has("\xFF\xD8\xFF"):
		return "image/jpeg"
	case has("\x89PNG\r\n\x1a\n"):
		return "image/png"
	case has("GIF87a"), has("GIF89a"):
		return "image/gif"
	case len(content) >= 12 && has("RIFF") && string(content[8:12]) == "WEBP":
		return "image/webp"
	case has("II*\x00"), has("MM\x00*"):
		return "image/tiff"
	case has("BM") && len(content) > 14:
		return "image/bmp"
	case has("%PDF-"):
		return "application/pdf"
	case has("PK\x03\x04"):
		return "application/zip"
	case has("\x1f\x8b"):
		return "application/gzip"
	case len(content) >= 12 && string(content[4:8]) == "ftyp":
		switch string(content[8:12]) {
		case "heic", "heix", "mif1", "msf1":
			return "image/heic"
		}
		return "video/mp4"
	}
	sample := content[:min(len(content), 512)]
	if len(content) > len(sample) {
		// The sample may end inside a multi-byte character.
		for i := 0; i < 3 && !utf8.Valid(sample); i++ {
			sample = sample[:len(sample)-1]
		}
	}
	if utf8.Valid(sample) {
		text := true
		for _, b := range sample {
			if b < 0x20 && b != '\n' && b != '\r' && b != '\t' && b != '\f' {
				text = false
				break
			}
		}
		if text {
			return "text/plain"
		}
	}
	return "application/octet-stream"
}

// extensionTypes maps accept entries written as extensions to the type the
// sniffer reports for such content.
var extensionTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif",
	".webp": "image/webp", ".tif": "image/tiff", ".tiff": "image/tiff", ".bmp": "image/bmp",
	".heic": "image/heic", ".pdf": "application/pdf", ".zip": "application/zip", ".gz": "application/gzip",
	".txt": "text/plain", ".csv": "text/plain", ".mp4": "video/mp4",
}

// acceptMatches reports whether a sniffed type satisfies an accept list of
// types ("image/png"), wildcards ("image/*") and extensions (".pdf").
func acceptMatches(accept []string, sniffed string) bool {
	for _, a := range accept {
		a = strings.ToLower(strings.TrimSpace(a))
		switch {
		case strings.HasPrefix(a, "."):
			if extensionTypes[a] == sniffed {
				return true
			}
		case strings.HasSuffix(a, "/*"):
			if strings.HasPrefix(sniffed, strings.TrimSuffix(a, "*")) {
				return true
			}
		case a == sniffed:
			return true
		}
	}
	return false
}

func (e *Engine) maxUpload(in Input) int64 {
	switch {
	case in.MaxBytes > 0:
		return in.MaxBytes
	case e.MaxUploadBytes > 0:
		return e.MaxUploadBytes
	}
	return DefaultMaxUploadBytes
}

// cleanFileName keeps the base name of a client file name, without path
// separators or control characters.
func cleanFileName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = path.Base(strings.TrimSpace(name))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' {
			return -1
		}
		return r
	}, name)
	if name == "." || name == "/" || name == "" {
		name = "file"
	}
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}

func (c *Compiled) input(form, name string) (Input, *compiledForm, bool) {
	cf := c.forms[form]
	if cf == nil {
		return Input{}, nil, false
	}
	for _, in := range cf.inputs {
		if in.Name == name {
			return in, cf, true
		}
	}
	return Input{}, nil, false
}

// caseFiles lists every uploaded file of the case by data path.
func (e *Engine) caseFiles(c *Case) map[string][]FileRef {
	out := map[string][]FileRef{}
	for name, cf := range e.C.forms {
		if cf.Repeatable {
			continue
		}
		for _, in := range cf.inputs {
			if in.Kind != KindFile {
				continue
			}
			p := name + "." + in.Name
			if v, ok := c.Get(p); ok {
				if refs := fileRefs(v); len(refs) > 0 {
					out[p] = refs
				}
			}
		}
	}
	return out
}

// Files lists every uploaded file a case references, ordered by data path.
// Hosts use it to remove stored objects a purge or anonymisation dropped.
func (e *Engine) Files(c *Case) []FileRef {
	byPath := e.caseFiles(c)
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	var out []FileRef
	for _, p := range paths {
		out = append(out, byPath[p]...)
	}
	return out
}

// Attach validates an upload for the file input at path ("form.input") and
// records it on the case: its name, size, sniffed content type, SHA-256,
// storage key, uploader and time. The caller stores the content under the
// returned FileRef's Key before saving the case. A single-file input's
// previous file is returned as replaced, for the host to delete once the
// case is saved.
//
// The upload is checked against the input's max_bytes, its accept list (by
// the content's magic bytes, never the declared type) and max_files, and
// rejected when the case already holds a file with the same checksum.
func (e *Engine) Attach(ctx context.Context, in *Case, actor Actor, stage, dataPath string, up Upload) (*Case, FileRef, []FileRef, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, FileRef{}, nil, err
	}
	if actor.Link != nil || !e.CanAct(c, st, actor) {
		return nil, FileRef{}, nil, forbidden("you may not upload files at stage %q", stage)
	}
	if err := e.guardClaim(c, st, ss, actor); err != nil {
		return nil, FileRef{}, nil, err
	}
	form, name, _ := strings.Cut(dataPath, ".")
	input, cf, ok := e.C.input(form, name)
	if !ok || input.Kind != KindFile {
		return nil, FileRef{}, nil, fmt.Errorf("%w: file input %q", ErrNotFound, dataPath)
	}
	reject := func(rule, msg string) error {
		return &ValidationError{Message: "the file was rejected", Fields: []FieldError{{Path: dataPath, Rule: rule, Message: msg}}}
	}
	if cf.Repeatable {
		return nil, FileRef{}, nil, reject("upload", "files cannot be uploaded into a repeatable form")
	}
	if _, editable := e.editable(c, st, ss, actor)[form][name]; !editable {
		return nil, FileRef{}, nil, forbidden("%s is not editable at stage %q", dataPath, stage)
	}
	label := titleOr(input.Label, input.Name)
	size := int64(len(up.Content))
	if size == 0 {
		return nil, FileRef{}, nil, reject("required", label+": the file is empty")
	}
	if limit := e.maxUpload(input); size > limit {
		return nil, FileRef{}, nil, reject("max_bytes", fmt.Sprintf("%s must be at most %d bytes", label, limit))
	}
	sniffed := SniffContentType(up.Content)
	if len(input.Accept) > 0 && !acceptMatches(input.Accept, sniffed) {
		return nil, FileRef{}, nil, reject("content_type", fmt.Sprintf("%s must be %s; the file's content is %s", label, strings.Join(input.Accept, ", "), sniffed))
	}
	sum := sha256.Sum256(up.Content)
	digest := hex.EncodeToString(sum[:])
	files := e.caseFiles(c)
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		for _, ref := range files[p] {
			if ref.SHA256 == digest {
				return nil, FileRef{}, nil, reject("duplicate", fmt.Sprintf("this file was already uploaded as %s (%s)", p, ref.Name))
			}
		}
	}
	current, _ := c.Get(dataPath)
	existing := fileRefs(current)
	maxFiles := max(1, input.MaxFiles)
	var replaced []FileRef
	if maxFiles == 1 {
		replaced = existing
	} else if len(existing) >= maxFiles {
		return nil, FileRef{}, nil, reject("max_files", fmt.Sprintf("%s holds at most %d files", label, maxFiles))
	}
	now := e.now()
	id := e.newID("file")
	ref := FileRef{ID: id, Name: cleanFileName(up.Name), Size: size, ContentType: sniffed, SHA256: digest,
		Key: c.ID + "/" + id, UploadedBy: actor.ID, UploadedAt: now}
	if maxFiles == 1 {
		c.Set(dataPath, ref.value())
	} else {
		list := make([]any, 0, len(existing)+1)
		for _, r := range existing {
			list = append(list, r.value())
		}
		c.Set(dataPath, append(list, ref.value()))
	}
	e.recompute(c, actor)
	e.refreshChecks(c, stage)
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: stage, Action: "upload", Comment: ref.Name, Changes: []string{dataPath}})
	c.emit("file.uploaded", stage, actor.ID, now, map[string]any{"path": dataPath, "file": id, "sha256": digest})
	c.UpdatedAt = now
	return c, ref, replaced, nil
}

// fileValue resolves a submitted value for a managed file input. Files only
// arrive through Attach, so a save may clear the input, keep its files, or
// drop some of a multi-file input's files; files are matched by id and the
// recorded metadata is kept, so a client cannot forge a checksum or point
// the input at another object.
func (e *Engine) fileValue(c *Case, dataPath string, in Input, raw any) (any, string, string) {
	if isEmpty(raw) {
		return nil, "", ""
	}
	label := titleOr(in.Label, in.Name)
	current, _ := c.Get(dataPath)
	byID := map[string]FileRef{}
	for _, ref := range fileRefs(current) {
		byID[ref.ID] = ref
	}
	var submitted []any
	switch v := raw.(type) {
	case map[string]any:
		submitted = []any{v}
	case []any:
		submitted = v
	case string:
		if ref, ok := byID[v]; ok {
			submitted = []any{map[string]any{"id": ref.ID}}
		}
	}
	if len(submitted) == 0 {
		return nil, "upload", label + " must be uploaded as a file"
	}
	keep := make([]any, 0, len(submitted))
	for _, item := range submitted {
		m, _ := item.(map[string]any)
		id, _ := m["id"].(string)
		ref, ok := byID[id]
		if !ok {
			return nil, "upload", label + " must be uploaded as a file"
		}
		keep = append(keep, ref.value())
	}
	if max(1, in.MaxFiles) == 1 {
		if len(keep) > 1 {
			return nil, "max_files", label + " holds one file"
		}
		return keep[0], "", ""
	}
	return keep, "", ""
}

// File returns an uploaded file's metadata if the actor may read it. id picks
// one file of a multi-file input (default: the first). The actor must be
// able to see the input: the applicant, or someone who may view a stage
// whose page (or review nodes) shows its form; sensitive inputs also need a
// reveal role.
func (e *Engine) File(c *Case, actor Actor, dataPath, id string) (FileRef, error) {
	form, name, _ := strings.Cut(dataPath, ".")
	input, cf, ok := e.C.input(form, name)
	if !ok || input.Kind != KindFile || cf.Repeatable {
		return FileRef{}, fmt.Errorf("%w: file input %q", ErrNotFound, dataPath)
	}
	if !e.canSeeInput(c, actor, form, input) {
		return FileRef{}, forbidden("you may not read %s", dataPath)
	}
	v, _ := c.Get(dataPath)
	for _, ref := range fileRefs(v) {
		if id == "" || ref.ID == id {
			return ref, nil
		}
	}
	return FileRef{}, fmt.Errorf("%w: no file at %s", ErrNotFound, dataPath)
}

func (e *Engine) canSeeInput(c *Case, actor Actor, form string, in Input) bool {
	if actor.Link != nil {
		return false
	}
	applicant := isApplicant(c, actor)
	if in.Sensitive && !applicant && !actor.HasAnyRole(e.C.Def.RevealRoles) {
		return false
	}
	for i := range e.C.Def.Stages {
		st := &e.C.Def.Stages[i]
		viewer := e.CanView(c, st, actor)
		if !viewer && !(applicant && st.Public) {
			continue
		}
		if viewer {
			for _, n := range st.Nodes {
				if slices.Contains(n.Forms, form) {
					return true
				}
			}
		}
		if st.Page == nil {
			continue
		}
		for _, g := range st.Page.Groups {
			if g.Mode == ModeHidden || !slices.Contains(g.Forms, form) {
				continue
			}
			if len(g.Roles) > 0 && !actor.HasAnyRole(g.Roles) {
				continue
			}
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Acknowledgements
// ---------------------------------------------------------------------------

// AckRecord is one accepted acknowledgement: who accepted which wording
// (and its SHA-256), when, and at which case revision.
type AckRecord struct {
	Stage    string    `json:"stage"`
	Name     string    `json:"name"`
	Text     string    `json:"text"`
	SHA256   string    `json:"sha256"`
	By       string    `json:"by"`
	At       time.Time `json:"at"`
	Revision int64     `json:"revision"`
}

// Acks are the acknowledgements a submitter accepts, by name. A value is the
// SHA-256 of the wording the client showed (checked against the current
// wording), or empty. JSON accepts a list of names or an object of names to
// true or to a hash.
type Acks map[string]string

// UnmarshalJSON implements json.Unmarshaler.
func (a *Acks) UnmarshalJSON(raw []byte) error {
	out := Acks{}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, name := range list {
			out[name] = ""
		}
		*a = out
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("acknowledgements must be a list of names or an object")
	}
	for name, v := range m {
		switch t := v.(type) {
		case bool:
			if t {
				out[name] = ""
			}
		case string:
			switch t {
			case "", "false":
			case "true":
				out[name] = ""
			default:
				out[name] = t
			}
		}
	}
	*a = out
	return nil
}

// AckHash is the SHA-256 of an acknowledgement's exact wording.
func AckHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// requiredAcks lists the page acknowledgements the actor must accept to
// submit the stage.
func (e *Engine) requiredAcks(c *Case, st *Stage, actor Actor) []AcknowledgementSpec {
	if st.Page == nil || len(st.Page.Acknowledgements) == 0 {
		return nil
	}
	env := e.Env(c, actor)
	var out []AcknowledgementSpec
	for _, a := range st.Page.Acknowledgements {
		if ok, err := e.cond(a.VisibleIf, env); err == nil && ok {
			out = append(out, a)
		}
	}
	return out
}

func (e *Engine) checkAcks(c *Case, st *Stage, actor Actor, given Acks) []FieldError {
	var errs []FieldError
	for _, a := range e.requiredAcks(c, st, actor) {
		hash, ok := given[a.Name]
		switch {
		case !ok:
			errs = append(errs, FieldError{Path: "acknowledgements." + a.Name, Rule: "acknowledgement",
				Message: titleOr(a.Title, a.Name) + " must be accepted"})
		case hash != "" && hash != AckHash(a.Text):
			errs = append(errs, FieldError{Path: "acknowledgements." + a.Name, Rule: "wording_changed",
				Message: "the wording of " + titleOr(a.Title, a.Name) + " has changed; reload and accept it again"})
		}
	}
	return errs
}

func (e *Engine) recordAcks(c *Case, st *Stage, actor Actor, now time.Time) {
	for _, a := range e.requiredAcks(c, st, actor) {
		c.Acknowledgements = append(c.Acknowledgements, AckRecord{Stage: st.Name, Name: a.Name, Text: a.Text,
			SHA256: AckHash(a.Text), By: actor.ID, At: now, Revision: c.Revision})
	}
}

// ---------------------------------------------------------------------------
// Confirm before submit
// ---------------------------------------------------------------------------

// ErrConfirmation is returned when a confirm_submit action is taken without
// a valid confirmation token: none, forged, expired, or issued for a case
// revision or data that has since changed.
var ErrConfirmation = errors.New("pipeline: the submission needs a valid confirmation")

// SubmitReview is what the first submit of a confirm_submit action returns: a
// summary of what will be submitted and a token that commits exactly that.
type SubmitReview struct {
	Case             CaseSummary   `json:"case"`
	Stage            string        `json:"stage"`
	Action           string        `json:"action"`
	Label            string        `json:"label,omitempty"`
	Prompt           string        `json:"prompt,omitempty"`
	Changes          []string      `json:"changes,omitempty"`
	Fields           []ReviewField `json:"fields"`
	Acknowledgements []AckView     `json:"acknowledgements,omitempty"`
	Token            string        `json:"confirm_token"`
	ExpiresAt        time.Time     `json:"expires_at"`
}

// ReviewField is one answered input in a review.
type ReviewField struct {
	Path   string `json:"path"`
	Label  string `json:"label"`
	Group  string `json:"group,omitempty"`
	Value  any    `json:"value"`
	Masked bool   `json:"masked,omitempty"`
}

type confirmClaims struct {
	Case     string `json:"c"`
	Stage    string `json:"s"`
	Action   string `json:"a"`
	Revision int64  `json:"r"`
	Digest   string `json:"d"`
	Actor    string `json:"u"`
	Expires  int64  `json:"x"`
}

// NeedsConfirmation reports whether taking action at stage is two-step.
func (e *Engine) NeedsConfirmation(c *Case, stage, action string) bool {
	st, ok := e.C.Stage(stage)
	if !ok {
		return false
	}
	spec, ok := findAction(st, action)
	return ok && e.confirmRequired(c, stage, spec)
}

func (e *Engine) confirmRequired(_ *Case, stage string, spec ActionSpec) bool {
	if spec.ConfirmSubmit {
		return true
	}
	st, ok := e.C.Stage(stage)
	if !ok || !st.ConfirmSubmit {
		return false
	}
	return spec.Outcome == "" || spec.Outcome == OutcomeAdvance || spec.Outcome == OutcomeApprove
}

// confirmDigest hashes everything a submission carries besides its token.
func confirmDigest(input ActInput) string {
	content := map[string]any{"comment": input.Comment}
	if len(input.Data) > 0 {
		content["data"] = input.Data
	}
	if len(input.Flags) > 0 {
		content["flags"] = input.Flags
	}
	if len(input.Acknowledgements) > 0 {
		content["acknowledgements"] = input.Acknowledgements
	}
	raw, _ := json.Marshal(content) // encoding/json sorts map keys
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (e *Engine) confirmSecret() []byte {
	if len(e.SigningKey) > 0 {
		return append([]byte("pipeline-confirm:"), e.SigningKey...)
	}
	e.confirmOnce.Do(func() {
		e.confirmKey = make([]byte, 32)
		_, _ = rand.Read(e.confirmKey)
	})
	return e.confirmKey
}

func (e *Engine) confirmMAC(payload string) string {
	mac := hmac.New(sha256.New, e.confirmSecret())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func confirmFailure(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrConfirmation, fmt.Sprintf(format, args...))
}

func (e *Engine) checkConfirmToken(c *Case, actor Actor, stage, action string, input ActInput) error {
	token := strings.TrimSpace(input.ConfirmToken)
	if token == "" {
		return confirmFailure("%s must be reviewed first: submit it without a confirm_token to get one", action)
	}
	enc, mac, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(e.confirmMAC(enc))) {
		return confirmFailure("the confirmation token is not valid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return confirmFailure("the confirmation token is not valid")
	}
	var claims confirmClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return confirmFailure("the confirmation token is not valid")
	}
	switch {
	case claims.Case != c.ID || claims.Stage != stage || claims.Action != action || claims.Actor != actor.ID:
		return confirmFailure("the confirmation token was issued for a different submission")
	case e.now().Unix() > claims.Expires:
		return confirmFailure("the confirmation has expired; review the submission again")
	case claims.Revision != c.Revision:
		return confirmFailure("the case changed after it was reviewed; review the submission again")
	case claims.Digest != confirmDigest(input):
		return confirmFailure("the submission differs from what was reviewed; review it again")
	}
	return nil
}

// Preview is the first step of a confirm_submit action. It runs every check
// the action would (permissions, the data sent, required inputs,
// acknowledgements, rules, nodes) on a copy of the case without saving
// anything, and returns a review of what would be submitted plus a token
// bound to the case, stage, action, actor, revision and a hash of the
// submission. Act with the same input and the token commits it.
func (e *Engine) Preview(ctx context.Context, in *Case, actor Actor, stage, action string, input ActInput) (*SubmitReview, error) {
	c := in.Clone()
	st, _, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	spec, found := findAction(st, action)
	if !found {
		return nil, fmt.Errorf("%w: action %q at stage %q", ErrNotFound, action, stage)
	}
	if !e.confirmRequired(c, stage, spec) {
		return nil, badState("action %q is not confirmed in two steps", action)
	}
	input.ConfirmToken = ""
	st, ss, changed, err := e.beginAct(c, actor, stage, spec, input, false)
	if err != nil {
		return nil, err
	}
	if spec.Outcome == "" || spec.Outcome == OutcomeAdvance || spec.Outcome == OutcomeApprove {
		if err := e.checkAdvance(c, st, ss, spec, actor, input, false); err != nil {
			return nil, err
		}
	}
	now := e.now()
	ttl := e.ConfirmTTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	expires := now.Add(ttl)
	claims := confirmClaims{Case: in.ID, Stage: stage, Action: action, Revision: in.Revision,
		Digest: confirmDigest(input), Actor: actor.ID, Expires: expires.Unix()}
	raw, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString(raw)
	r := &SubmitReview{
		Stage: stage, Action: action, Label: orDefault(spec.Label, action), Prompt: spec.Confirm,
		Changes: changed, Fields: []ReviewField{}, Token: enc + "." + e.confirmMAC(enc), ExpiresAt: expires,
	}
	v, err := e.View(c, actor, stage)
	if err != nil {
		return nil, err
	}
	r.Case = v.Case
	r.Case.Revision = in.Revision
	if v.Page != nil {
		for _, g := range v.Page.Groups {
			for _, f := range g.Forms {
				if f.Repeatable {
					if len(f.Entries) > 0 {
						r.Fields = append(r.Fields, ReviewField{Path: f.Name, Label: titleOr(f.Title, f.Name), Group: g.Name, Value: f.Entries})
					}
					continue
				}
				for _, iv := range f.Inputs {
					if iv.Value == nil || iv.Sealed {
						continue
					}
					r.Fields = append(r.Fields, ReviewField{Path: iv.Path, Label: iv.Label, Group: g.Name, Value: iv.Value, Masked: iv.Masked})
				}
			}
		}
		for _, a := range v.Page.Acknowledgements {
			if input.Acknowledgements != nil {
				if _, ok := input.Acknowledgements[a.Name]; ok {
					r.Acknowledgements = append(r.Acknowledgements, a)
				}
			}
		}
	}
	return r, nil
}
