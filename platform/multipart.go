package platform

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
)

// multipartToJSON turns a multipart/form-data body into the JSON object an
// intent's input is decoded from, so a browser form that uploads files needs
// no client-side encoding. Text fields become strings; a file part becomes
// {filename, content_type, size, content_base64}. A field sent more than once
// becomes a list. The route's max_body_bytes bounds the whole body, so parts
// are read into memory.
func multipartToJSON(body []byte, contentType string) ([]byte, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return nil, invalidInput("the multipart request has no boundary")
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	out := map[string]any{}
	add := func(name string, value any) {
		switch existing := out[name].(type) {
		case nil:
			out[name] = value
		case []any:
			out[name] = append(existing, value)
		default:
			out[name] = []any{existing, value}
		}
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, invalidInput("the multipart request could not be parsed: %v", err)
		}
		name := part.FormName()
		content, err := io.ReadAll(part)
		part.Close()
		if err != nil {
			return nil, invalidInput("the multipart request could not be read: %v", err)
		}
		if name == "" {
			continue
		}
		if part.FileName() == "" {
			add(name, string(content))
			continue
		}
		add(name, map[string]any{
			"filename":       part.FileName(),
			"content_type":   part.Header.Get("Content-Type"),
			"size":           len(content),
			"content_base64": base64.StdEncoding.EncodeToString(content),
		})
	}
	return json.Marshal(out)
}
