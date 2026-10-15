package platform

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestMediaActionsEndToEnd(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	uploadDir := filepath.Join(tempDir, "uploads")

	// 1. Setup in-memory / local storage and DB
	ctx := context.Background()
	storageRes, storageCloser, err := openFSStorage(ctx, ResourceSpec{
		Name: "test-storage",
		Config: map[string]any{
			"dir": uploadDir,
		},
	})
	if err != nil {
		t.Fatalf("openFSStorage failed: %v", err)
	}
	if storageCloser != nil {
		defer storageCloser.Close()
	}

	dbRes, dbCloser, err := openDatabase(ctx, ResourceSpec{
		Name: "test-db",
		Config: map[string]any{
			"driver": "sqlite",
			"dsn":    dbPath,
		},
	})
	if err != nil {
		t.Fatalf("openDatabase failed: %v", err)
	}
	if dbCloser != nil {
		defer dbCloser.Close()
	}

	db := dbRes.(*Database)
	_, err = db.ExecContext(ctx, "CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT, avatar_url TEXT, avatar_meta TEXT)")
	if err != nil {
		t.Fatalf("create table failed: %v", err)
	}
	_, err = db.ExecContext(ctx, "INSERT INTO users (id, name) VALUES ('u1', 'Alice')")
	if err != nil {
		t.Fatalf("insert user failed: %v", err)
	}

	// 2. Build mock build context with registered resources
	registry := NewRegistry()

	build := BuildContext{
		Resources: map[string]Resource{
			"files": storageRes,
			"db":    dbRes,
		},
	}

	// 3. Test media.upload action
	uploadActionFactory := registry.actions["media.upload"]
	uploadAction, err := uploadActionFactory.Build(build, NodeSpec{
		Name:     "upload_avatar",
		Resource: "files",
		Config: map[string]any{
			"file_fact":     "input.avatar",
			"path":          "avatars/",
			"url_prefix":    "/files/",
			"allowed_types": []string{"image/*"},
		},
		Provides: []string{"upload"},
	})
	if err != nil {
		t.Fatalf("Build media.upload failed: %v", err)
	}

	avatarContent := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR...")
	avatarBase64 := base64.StdEncoding.EncodeToString(avatarContent)

	actCtx := &ActionContext{
		Context: ctx,
		Now:     time.Now().UTC(),
		Inputs: map[string]any{
			"input": map[string]any{
				"avatar": map[string]any{
					"filename":       "photo.png",
					"content_type":   "image/png",
					"content_base64": avatarBase64,
				},
				"user_id": "u1",
			},
		},
	}

	uploadResult, err := uploadAction.Run(actCtx)
	if err != nil {
		t.Fatalf("Run media.upload failed: %v", err)
	}

	uploadData, ok := uploadResult.Outputs["upload"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected upload result type: %T", uploadResult.Outputs["upload"])
	}

	fileURL, _ := uploadData["url"].(string)
	fileKey, _ := uploadData["key"].(string)
	if !strings.HasPrefix(fileURL, "/files/avatars/") {
		t.Fatalf("unexpected file URL: %s", fileURL)
	}
	if !strings.HasPrefix(fileKey, "avatars/") {
		t.Fatalf("unexpected file key: %s", fileKey)
	}

	// Verify file exists on disk
	expectedDiskFile := filepath.Join(uploadDir, filepath.FromSlash(fileKey))
	if _, err := os.Stat(expectedDiskFile); err != nil {
		t.Fatalf("uploaded file missing on disk: %s", expectedDiskFile)
	}

	// 4. Test media.link action
	linkActionFactory := registry.actions["media.link"]
	linkAction, err := linkActionFactory.Build(build, NodeSpec{
		Name:     "link_avatar",
		Resource: "db",
		Config: map[string]any{
			"table":       "users",
			"id_fact":     "input.user_id",
			"id_column":   "id",
			"url_fact":    "upload.url",
			"url_column":  "avatar_url",
			"meta_fact":   "upload",
			"meta_column": "avatar_meta",
		},
		Provides: []string{"link_result"},
	})
	if err != nil {
		t.Fatalf("Build media.link failed: %v", err)
	}

	actCtx.Inputs["upload"] = uploadData
	linkResult, err := linkAction.Run(actCtx)
	if err != nil {
		t.Fatalf("Run media.link failed: %v", err)
	}

	linkData, ok := linkResult.Outputs["link_result"].(map[string]any)
	if !ok || linkData["linked"] != true {
		t.Fatalf("unexpected link result: %+v", linkResult.Outputs["link_result"])
	}

	// Verify database record has updated avatar_url and avatar_meta
	var dbAvatarURL, dbAvatarMeta string
	err = db.QueryRowContext(ctx, "SELECT avatar_url, avatar_meta FROM users WHERE id = 'u1'").Scan(&dbAvatarURL, &dbAvatarMeta)
	if err != nil {
		t.Fatalf("query updated user failed: %v", err)
	}
	if dbAvatarURL != fileURL {
		t.Fatalf("expected avatar_url %q, got %q", fileURL, dbAvatarURL)
	}
	if !strings.Contains(dbAvatarMeta, fileKey) {
		t.Fatalf("expected avatar_meta to contain fileKey, got %q", dbAvatarMeta)
	}

	// 5. Test media.serve action
	serveActionFactory := registry.actions["media.serve"]
	serveAction, err := serveActionFactory.Build(build, NodeSpec{
		Name:     "serve_avatar",
		Resource: "files",
		Config: map[string]any{
			"key":         "{{ upload.key }}",
			"disposition": "inline",
		},
		Provides: []string{"serve_result"},
	})
	if err != nil {
		t.Fatalf("Build media.serve failed: %v", err)
	}

	serveResult, err := serveAction.Run(actCtx)
	if err != nil {
		t.Fatalf("Run media.serve failed: %v", err)
	}

	rawResp, ok := serveResult.Outputs["serve_result"].(RawResponse)
	if !ok {
		t.Fatalf("expected RawResponse, got %T", serveResult.Outputs["serve_result"])
	}
	if !rawResp.Inline {
		t.Fatalf("expected Inline disposition")
	}
	if rawResp.ContentType != "image/png" {
		t.Fatalf("expected image/png content type, got %s", rawResp.ContentType)
	}
	if string(rawResp.Body) != string(avatarContent) {
		t.Fatalf("served body does not match uploaded content")
	}
}

func TestMediaActionsS3PresignAndValidation(t *testing.T) {
	ctx := context.Background()
	mock := &s3MockTransport{
		stored: make(map[string][]byte),
		t:      t,
	}

	res, closer, err := openS3Storage(ctx, ResourceSpec{
		Name: "s3",
		Config: map[string]any{
			"bucket":         "media-bucket",
			"endpoint":       "http://s3.local",
			"access_key":     "AKIAEXAMPLE",
			"secret_key":     "SECRETEXAMPLE",
			"use_path_style": true,
		},
	})
	if err != nil {
		t.Fatalf("openS3Storage failed: %v", err)
	}
	if closer != nil {
		defer closer.Close()
	}

	store := res.(*s3Storage)
	store.client = &http.Client{Transport: mock}

	registry := NewRegistry()
	build := BuildContext{
		Resources: map[string]Resource{
			"s3": store,
		},
	}

	uploadActionFactory := registry.actions["media.upload"]
	uploadAction, err := uploadActionFactory.Build(build, NodeSpec{
		Name:     "upload_doc",
		Resource: "s3",
		Config: map[string]any{
			"file_fact":     "input.file",
			"path":          "docs/",
			"presign":       true,
			"presign_ttl":   "30m",
			"allowed_types": []string{"application/pdf", "image/*"},
		},
		Provides: []string{"upload"},
	})
	if err != nil {
		t.Fatalf("Build media.upload failed: %v", err)
	}

	// 1. Valid PDF upload with pre-signed URL
	pdfContent := []byte("%PDF-1.4 ...")
	actCtx := &ActionContext{
		Context: ctx,
		Now:     time.Now().UTC(),
		Inputs: map[string]any{
			"input": map[string]any{
				"file": map[string]any{
					"filename":       "contract.pdf",
					"content_type":   "application/pdf",
					"content_base64": base64.StdEncoding.EncodeToString(pdfContent),
				},
			},
		},
	}

	uploadResult, err := uploadAction.Run(actCtx)
	if err != nil {
		t.Fatalf("Run media.upload failed: %v", err)
	}

	uploadData := uploadResult.Outputs["upload"].(map[string]any)
	presignedURL, _ := uploadData["url"].(string)
	if !strings.Contains(presignedURL, "X-Amz-Signature=") {
		t.Fatalf("expected pre-signed S3 URL, got: %s", presignedURL)
	}
	if uploadData["content_type"] != "application/pdf" {
		t.Fatalf("expected application/pdf, got: %s", uploadData["content_type"])
	}

	// 2. Disallowed type rejection (.exe)
	actCtxInvalid := &ActionContext{
		Context: ctx,
		Now:     time.Now().UTC(),
		Inputs: map[string]any{
			"input": map[string]any{
				"file": map[string]any{
					"filename":       "malware.exe",
					"content_type":   "application/x-msdownload",
					"content_base64": base64.StdEncoding.EncodeToString([]byte("MZ...")),
				},
			},
		},
	}

	_, err = uploadAction.Run(actCtxInvalid)
	if err == nil {
		t.Fatalf("expected error for disallowed file type, got nil")
	}
}

