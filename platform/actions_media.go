package platform

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/oarkflow/ref/platform/spi"
)

func registerMediaActions(r *Registry) {
	mustAction(r, "media.upload", mediaUploadAction, ActionInfo{
		Family:       "media",
		Summary:      "Upload and validate media or documents, store to object storage, and produce accessible URLs",
		ResourceKind: "storage",
		Provides:     "An object or list of objects with {key, url, filename, size, content_type, etag, uploaded_at}",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "file_fact", Type: "fact", Default: "input.file", Summary: "Fact path containing uploaded file or files (from multipart or base64 JSON)"},
			{Name: "path", Type: "template", Default: "uploads/", Summary: "Target storage path prefix (supports templates like uploads/{{ now '2006/01' }}/)"},
			{Name: "url_prefix", Type: "template", Default: "/files/", Summary: "URL prefix for public access (e.g. /files/ or https://cdn.example.com/)"},
			{Name: "allowed_types", Type: "[]string", Summary: "Allowed MIME types (e.g. image/*, application/pdf) or file extensions (.png, .jpg)"},
			{Name: "max_bytes", Type: "int", Default: "26214400", Summary: "Max file size in bytes (default 25MB)"},
			{Name: "presign", Type: "bool", Default: "false", Summary: "If true and using S3 storage, returns a pre-signed access URL"},
			{Name: "presign_ttl", Type: "duration", Default: "15m", Summary: "Time-to-live for pre-signed URLs"},
		},
	})

	mustAction(r, "media.serve", mediaServeAction, ActionInfo{
		Family:       "media",
		Summary:      "Serve a stored media or file directly over HTTP with proper MIME type, caching, and inline/attachment disposition",
		ResourceKind: "storage",
		Provides:     "Raw binary HTTP stream response",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "key", Type: "template", Required: true, Summary: "Object key in storage (e.g. {{ params['*'] }} or {{ params.key }})"},
			{Name: "disposition", Type: "string", Default: "inline", Summary: "Content disposition: 'inline' (for viewing in browser/img tag) or 'attachment' (for download)"},
			{Name: "cache_control", Type: "string", Default: "public, max-age=86400", Summary: "HTTP Cache-Control header"},
		},
	})

	mustAction(r, "media.link", mediaLinkAction, ActionInfo{
		Family:       "media",
		Summary:      "Link an uploaded media URL and metadata directly to a database record (e.g. user avatar, document attachment)",
		ResourceKind: "database",
		Provides:     "The updated record or confirmation",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "table", Type: "string", Required: true, Summary: "Target database table to update"},
			{Name: "id_fact", Type: "fact", Required: true, Summary: "Fact path containing the target record ID"},
			{Name: "id_column", Type: "string", Default: "id", Summary: "Primary key column name in table"},
			{Name: "url_fact", Type: "fact", Required: true, Summary: "Fact path containing the file URL (e.g. upload.url)"},
			{Name: "url_column", Type: "string", Required: true, Summary: "Column name in table to store the URL"},
			{Name: "meta_fact", Type: "fact", Summary: "Optional fact path containing full file metadata to store as JSON"},
			{Name: "meta_column", Type: "string", Summary: "Optional JSON column name to store full file metadata"},
		},
	})
}

// ---------------------------------------------------------------------------
// media.upload
// ---------------------------------------------------------------------------

type uploadedPayload struct {
	Filename    string
	ContentType string
	Content     []byte
}

var mediaUploadAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	store, err := requireResource[spi.ObjectStore](build, spec, "a storage resource")
	if err != nil {
		return nil, err
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}

	fileFact := configString(spec.Config, "file_fact", "input.file")
	pathTmpl, err := configTemplate(spec.Config, "path", "uploads/")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	urlPrefixTmpl, err := configTemplate(spec.Config, "url_prefix", "/files/")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}

	allowedTypes := configStrings(spec.Config, "allowed_types")
	maxBytes, err := configInt64(spec.Config, "max_bytes", 25<<20)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	presign := configBool(spec.Config, "presign", false)
	presignTTL, err := configDuration(spec.Config, "presign_ttl", 15*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}

	presigner, _ := store.(spi.ObjectPresigner)

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		rawFile, found := resolvePath(orgRoot(ctx), fileFact)
		if !found || rawFile == nil {
			// Try fallback to "input.files" or "input"
			if alt, ok := resolvePath(orgRoot(ctx), "input.files"); ok && alt != nil {
				rawFile = alt
			} else if alt, ok := resolvePath(orgRoot(ctx), "input"); ok && alt != nil {
				rawFile = alt
			}
		}

		uploads, isSlice, err := extractUploads(rawFile)
		if err != nil {
			return ActionResult{}, err
		}
		if len(uploads) == 0 {
			return ActionResult{}, invalidInput("no file found to upload at %q", fileFact)
		}

		targetPrefix, err := pathTmpl.Render(actionEnv(ctx))
		if err != nil {
			return ActionResult{}, err
		}
		targetPrefix = strings.TrimPrefix(targetPrefix, "/")
		if targetPrefix != "" && !strings.HasSuffix(targetPrefix, "/") {
			targetPrefix += "/"
		}

		publicPrefix := "/files/"
		if urlPrefixTmpl != nil {
			if p, err := urlPrefixTmpl.Render(actionEnv(ctx)); err == nil && p != "" {
				publicPrefix = p
			}
		}
		if !strings.HasSuffix(publicPrefix, "/") && !strings.Contains(publicPrefix, "?") {
			publicPrefix += "/"
		}

		var results []map[string]any
		now := time.Now().UTC()

		for _, up := range uploads {
			if int64(len(up.Content)) > maxBytes {
				return ActionResult{}, invalidInput("file %q exceeds max allowed size of %d bytes", up.Filename, maxBytes)
			}

			// Sniff MIME type from content if missing
			detectedType := up.ContentType
			if detectedType == "" || detectedType == "application/octet-stream" {
				detectedType = http.DetectContentType(up.Content)
			}
			if extType := mime.TypeByExtension(filepath.Ext(up.Filename)); extType != "" && (detectedType == "application/octet-stream" || detectedType == "text/plain") {
				detectedType = extType
			}

			// Validate allowed types
			if len(allowedTypes) > 0 && !isAllowedType(up.Filename, detectedType, allowedTypes) {
				return ActionResult{}, invalidInput("file %q type %q is not allowed; must be one of %v", up.Filename, detectedType, allowedTypes)
			}

			// Generate safe unique key
			uniqueID := randomHexString(8)
			cleanName := sanitizeFilename(up.Filename)
			key := fmt.Sprintf("%s%s_%s", targetPrefix, uniqueID, cleanName)

			// Store object
			info, err := store.Put(ctx.Context, key, bytes.NewReader(up.Content), detectedType)
			if err != nil {
				return ActionResult{}, storageFailure(err)
			}

			// Generate access URL
			fileURL := ""
			if presign && presigner != nil {
				if signed, err := presigner.Presign(ctx.Context, key, "GET", presignTTL); err == nil {
					fileURL = signed
				}
			}
			if fileURL == "" {
				if strings.HasPrefix(publicPrefix, "http://") || strings.HasPrefix(publicPrefix, "https://") {
					fileURL = strings.TrimRight(publicPrefix, "/") + "/" + key
				} else {
					fileURL = path.Join(publicPrefix, key)
				}
			}

			results = append(results, map[string]any{
				"key":          info.Key,
				"url":          fileURL,
				"filename":     up.Filename,
				"size":         info.Size,
				"content_type": detectedType,
				"etag":         info.ETag,
				"uploaded_at":  now.Format(time.RFC3339),
			})
		}

		if !isSlice && len(results) == 1 {
			return singleOutput(spec, results[0]), nil
		}
		return singleOutput(spec, results), nil
	}), nil
})

func extractUploads(raw any) ([]uploadedPayload, bool, error) {
	if raw == nil {
		return nil, false, nil
	}

	var rawList []any
	isSlice := false
	switch v := raw.(type) {
	case []any:
		rawList = v
		isSlice = true
	case map[string]any:
		// Check if it's a wrapper like { "file": { ... } } or { "files": [ ... ] }
		if subFiles, ok := v["files"].([]any); ok {
			rawList = subFiles
			isSlice = true
		} else if subFile, ok := v["file"].(map[string]any); ok {
			rawList = []any{subFile}
		} else {
			rawList = []any{v}
		}
	default:
		return nil, false, fmt.Errorf("invalid upload payload format: expected object or array")
	}

	var out []uploadedPayload
	for _, item := range rawList {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := orDefaultString(configString(m, "filename", ""), configString(m, "name", "file.bin"))
		cType := configString(m, "content_type", "")

		var content []byte
		if b64 := configString(m, "content_base64", ""); b64 != "" {
			decoded, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return nil, isSlice, fmt.Errorf("file %q has invalid base64 content: %w", name, err)
			}
			content = decoded
		} else if rawContent, ok := m["content"]; ok {
			switch c := rawContent.(type) {
			case string:
				content = []byte(c)
			case []byte:
				content = c
			}
		}

		if len(content) == 0 {
			continue
		}

		out = append(out, uploadedPayload{
			Filename:    name,
			ContentType: cType,
			Content:     content,
		})
	}

	return out, isSlice, nil
}

func isAllowedType(filename, mimeType string, allowed []string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	mimeType = strings.ToLower(mimeType)

	for _, a := range allowed {
		a = strings.TrimSpace(strings.ToLower(a))
		if strings.HasPrefix(a, ".") {
			if ext == a {
				return true
			}
		} else if strings.HasSuffix(a, "/*") {
			prefix := strings.TrimSuffix(a, "/*")
			if strings.HasPrefix(mimeType, prefix+"/") {
				return true
			}
		} else if mimeType == a {
			return true
		}
	}
	return false
}

func sanitizeFilename(name string) string {
	name = filepath.Base(filepath.Clean(name))
	name = strings.ReplaceAll(name, " ", "_")
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	res := b.String()
	if res == "" || res == "." {
		return "file.bin"
	}
	return res
}

func randomHexString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// media.serve
// ---------------------------------------------------------------------------

var mediaServeAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	store, err := requireResource[spi.ObjectStore](build, spec, "a storage resource")
	if err != nil {
		return nil, err
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}

	keyTmpl, err := configTemplate(spec.Config, "key", "")
	if err != nil || keyTmpl == nil {
		return nil, fmt.Errorf("node %q: config.key template is required", spec.Name)
	}

	disposition := strings.ToLower(configString(spec.Config, "disposition", "inline"))
	cacheControl := configString(spec.Config, "cache_control", "public, max-age=86400")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		key, err := keyTmpl.Render(actionEnv(ctx))
		if err != nil {
			return ActionResult{}, err
		}
		key = strings.TrimPrefix(key, "/")
		if key == "" {
			return ActionResult{}, notFound("media", key)
		}

		reader, info, err := store.Get(ctx.Context, key)
		if err != nil {
			if isNotFound(err) {
				return ActionResult{}, notFound("media", key)
			}
			return ActionResult{}, storageFailure(err)
		}
		defer reader.Close()

		data, err := io.ReadAll(reader)
		if err != nil {
			return ActionResult{}, storageFailure(err)
		}

		contentType := info.ContentType
		if contentType == "" || contentType == "application/octet-stream" {
			contentType = http.DetectContentType(data)
		}
		if extType := mime.TypeByExtension(filepath.Ext(key)); extType != "" && (contentType == "application/octet-stream" || contentType == "text/plain") {
			contentType = extType
		}

		filename := filepath.Base(key)
		inline := disposition == "inline"

		headers := map[string]string{
			"Cache-Control": cacheControl,
		}
		if info.ETag != "" {
			headers["ETag"] = fmt.Sprintf("%q", strings.Trim(info.ETag, `"`))
		}
		if !info.ModifiedAt.IsZero() {
			headers["Last-Modified"] = info.ModifiedAt.UTC().Format(http.TimeFormat)
		}

		return acknowledgement(spec, RawResponse{
			ContentType: contentType,
			Filename:    filename,
			Inline:      inline,
			Headers:     headers,
			Body:        data,
		}), nil
	}), nil
})

// ---------------------------------------------------------------------------
// media.link
// ---------------------------------------------------------------------------

var mediaLinkAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	db, err := requireResource[*Database](build, spec, "a database.sql resource")
	if err != nil {
		return nil, err
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}

	table, err := requiredString(spec.Config, "table")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	idFact := configString(spec.Config, "id_fact", "input.id")
	idColumn := configString(spec.Config, "id_column", "id")
	urlFact := configString(spec.Config, "url_fact", "upload.url")
	urlColumn := configString(spec.Config, "url_column", "url")
	metaFact := configString(spec.Config, "meta_fact", "")
	metaColumn := configString(spec.Config, "meta_column", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		recordID, ok := resolvePath(orgRoot(ctx), idFact)
		if !ok || recordID == nil {
			return ActionResult{}, invalidInput("id not found at fact path %q", idFact)
		}
		fileURL, ok := resolvePath(orgRoot(ctx), urlFact)
		if !ok || fileURL == nil {
			return ActionResult{}, invalidInput("media url not found at fact path %q", urlFact)
		}

		var metaJSON []byte
		if metaFact != "" && metaColumn != "" {
			if metaVal, ok := resolvePath(orgRoot(ctx), metaFact); ok && metaVal != nil {
				metaJSON, _ = json.Marshal(metaVal)
			}
		}

		query := ""
		args := []any{}
		if len(metaJSON) > 0 {
			query = fmt.Sprintf("UPDATE %s SET %s = $1, %s = $2 WHERE %s = $3", table, urlColumn, metaColumn, idColumn)
			args = []any{Stringify(fileURL), string(metaJSON), recordID}
		} else {
			query = fmt.Sprintf("UPDATE %s SET %s = $1 WHERE %s = $2", table, urlColumn, idColumn)
			args = []any{Stringify(fileURL), recordID}
		}

		res, err := db.ExecContext(ctx.Context, rebind(db.Dialect, query), args...)
		if err != nil {
			return ActionResult{}, fmt.Errorf("media.link failed to update %s: %w", table, err)
		}

		affected, _ := res.RowsAffected()
		return singleOutput(spec, map[string]any{
			"linked":        true,
			"rows_affected": affected,
			"table":         table,
			"id":            recordID,
			"url":           fileURL,
		}), nil
	}), nil
})
