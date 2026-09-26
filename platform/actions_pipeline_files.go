package platform

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"strings"

	"github.com/oarkflow/ref/pipeline"
)

// Pipeline form UX: file uploads and downloads, and the review step of
// confirm_submit actions.

// upload attaches the request's file(s) to a file input of the stage. Every
// file is validated by the engine before anything is stored; the objects are
// written before the case is saved, and removed again if the save fails, so
// a case never references content that was not stored.
func (h *pipelineHandler) upload(ctx *ActionContext, hookCtx context.Context, c *pipeline.Case, e *pipeline.Engine, actor pipeline.Actor, stage string) (ActionResult, error) {
	path := h.param(ctx, "path")
	if path == "" {
		return ActionResult{}, invalidInput("the file input's path (form.input) is required")
	}
	uploads, err := uploadsOf(h.body(ctx, "file_fact", "input.file"))
	if err != nil {
		return ActionResult{}, err
	}
	next := c
	var (
		stored   []pipeline.FileRef
		replaced []pipeline.FileRef
		refs     []any
	)
	cleanup := func() {
		for _, ref := range stored {
			if err := h.res.files.Delete(ctx.Context, h.res.filePrefix+ref.Key); err != nil {
				slog.Warn("pipeline upload cleanup failed", "resource", h.res.name, "key", ref.Key, "error", err)
			}
		}
	}
	for _, up := range uploads {
		var (
			ref  pipeline.FileRef
			gone []pipeline.FileRef
		)
		next, ref, gone, err = e.Attach(hookCtx, next, actor, stage, path, up)
		if err != nil {
			cleanup()
			return ActionResult{}, pipelineFailure(err)
		}
		if _, err := h.res.files.Put(ctx.Context, h.res.filePrefix+ref.Key, bytes.NewReader(up.Content), ref.ContentType); err != nil {
			cleanup()
			return ActionResult{}, storageFailure(err)
		}
		stored = append(stored, ref)
		// A file replaced by a later file of this same request was stored
		// by it: it is cleaned up with the others if the save fails.
		replaced = append(replaced, gone...)
		refs = append(refs, ref)
	}
	if err := h.commit(ctx, hookCtx, e, next); err != nil {
		cleanup()
		return ActionResult{}, err
	}
	for _, ref := range replaced {
		if err := h.res.files.Delete(ctx.Context, h.res.filePrefix+ref.Key); err != nil {
			slog.Warn("pipeline replaced file cleanup failed", "resource", h.res.name, "key", ref.Key, "error", err)
		}
	}
	v, err := e.View(next, actor, stage)
	if err != nil {
		return h.out(map[string]any{"files": refs})
	}
	out, err := toMap(v)
	if err != nil {
		return ActionResult{}, err
	}
	out["files"] = refs
	return h.out(out)
}

// uploadsOf reads the file(s) of an upload request: an object (or a list of
// objects) with filename (or name) and content_base64 (or content), as sent
// in JSON or converted from multipart/form-data.
func uploadsOf(raw any) ([]pipeline.Upload, error) {
	var items []any
	switch t := raw.(type) {
	case map[string]any:
		items = []any{t}
	case []any:
		items = t
	}
	if len(items) == 0 {
		return nil, invalidInput("a file is required: send multipart/form-data with a file part named file, or input.file as {filename, content_base64}")
	}
	out := make([]pipeline.Upload, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, invalidInput("each file must be an object with filename and content_base64")
		}
		name := strings.TrimSpace(Stringify(m["filename"]))
		if name == "" {
			name = strings.TrimSpace(Stringify(m["name"]))
		}
		encoded, _ := m["content_base64"].(string)
		if encoded == "" {
			encoded, _ = m["content"].(string)
		}
		content, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, invalidInput("the file content is not valid base64")
		}
		out = append(out, pipeline.Upload{Name: name, Content: content})
	}
	return out, nil
}

// download serves an uploaded file to a caller who may see its input, after
// checking the stored bytes still match the size and SHA-256 recorded when it
// was uploaded. A mismatch is never served.
func (h *pipelineHandler) download(ctx *ActionContext, c *pipeline.Case, e *pipeline.Engine, actor pipeline.Actor) (ActionResult, error) {
	path := h.param(ctx, "path")
	if path == "" {
		return ActionResult{}, invalidInput("the file input's path (form.input) is required")
	}
	ref, err := e.File(c, actor, path, h.param(ctx, "file_id"))
	if err != nil {
		return ActionResult{}, pipelineFailure(err)
	}
	reader, _, err := h.res.files.Get(ctx.Context, h.res.filePrefix+ref.Key)
	if err != nil {
		if isNotFound(err) {
			slog.Error("pipeline file missing from storage", "resource", h.res.name, "case", c.ID, "path", path, "key", ref.Key)
			return ActionResult{}, pipelineFailure(pipeline.ErrIntegrity)
		}
		return ActionResult{}, storageFailure(err)
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, ref.Size+1))
	if err != nil {
		return ActionResult{}, storageFailure(err)
	}
	if err := pipeline.VerifyFile(ref, content); err != nil {
		slog.Error("pipeline file failed its integrity check", "resource", h.res.name, "case", c.ID, "path", path, "key", ref.Key, "error", err)
		return ActionResult{}, pipelineFailure(err)
	}
	return acknowledgement(h.spec, RawResponse{ContentType: ref.ContentType, Filename: ref.Name, Body: content}), nil
}

// review is the first step of a confirm_submit action: when the action needs
// confirmation and the request carries no confirm_token, it validates the
// submission without saving anything and returns the review with a token.
// ok is false when the action should be taken normally.
func (h *pipelineHandler) review(ctx *ActionContext, hookCtx context.Context, c *pipeline.Case, e *pipeline.Engine, actor pipeline.Actor, stage string) (ActionResult, bool, error) {
	body, _ := h.body(ctx, "", "input").(map[string]any)
	action := ""
	if v, ok := body["action"]; ok && v != nil {
		action = strings.TrimSpace(Stringify(v))
	}
	if action == "" {
		action = h.param(ctx, "action")
	}
	if action == "" || !e.NeedsConfirmation(c, stage, action) {
		return ActionResult{}, false, nil
	}
	var in pipeline.ActInput
	if err := decodeInto(body, &in); err != nil {
		return ActionResult{}, true, invalidInput("the action body is invalid: %v", err)
	}
	if in.ConfirmToken != "" {
		return ActionResult{}, false, nil
	}
	r, err := e.Preview(hookCtx, c, actor, stage, action, in)
	if err != nil {
		return ActionResult{}, true, pipelineFailure(err)
	}
	result, err := h.out(map[string]any{"confirmation_required": true, "review": r, "confirm_token": r.Token})
	return result, true, err
}

// dropFiles removes the stored objects of files before references and after
// no longer does (after nil: every file), once a purge or an anonymisation
// has been saved. Failures are logged: the case change already happened.
func (p *PipelineCases) dropFiles(ctx context.Context, e *pipeline.Engine, before, after *pipeline.Case) {
	if p.files == nil || before == nil {
		return
	}
	keep := map[string]bool{}
	if after != nil {
		for _, ref := range e.Files(after) {
			keep[ref.Key] = true
		}
	}
	for _, ref := range e.Files(before) {
		if keep[ref.Key] {
			continue
		}
		if err := p.files.Delete(ctx, p.filePrefix+ref.Key); err != nil {
			slog.Warn("pipeline file cleanup failed", "resource", p.name, "case", before.ID, "key", ref.Key, "error", err)
		}
	}
}
