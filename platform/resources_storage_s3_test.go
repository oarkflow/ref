package platform

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type s3MockTransport struct {
	stored map[string][]byte
	t      *testing.T
}

func (m *s3MockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Verify SigV4 Authorization header
	auth := req.Header.Get("Authorization")
	if auth == "" && req.URL.Query().Get("X-Amz-Signature") == "" {
		m.t.Errorf("missing SigV4 authorization header or query param: %v", req.Header)
	}

	key := strings.TrimPrefix(req.URL.Path, "/test-bucket/")
	rec := httptest.NewRecorder()

	switch req.Method {
	case http.MethodPut:
		data, err := io.ReadAll(req.Body)
		if err != nil {
			rec.WriteHeader(http.StatusInternalServerError)
			return rec.Result(), nil
		}
		m.stored[key] = data
		rec.Header().Set("ETag", `"test-etag"`)
		rec.WriteHeader(http.StatusOK)
	case http.MethodGet:
		if req.URL.Query().Get("list-type") == "2" {
			res := s3ListResult{}
			for k, v := range m.stored {
				res.Contents = append(res.Contents, struct {
					Key          string    `xml:"Key"`
					LastModified time.Time `xml:"LastModified"`
					ETag         string    `xml:"ETag"`
					Size         int64     `xml:"Size"`
				}{
					Key:          k,
					LastModified: time.Now().UTC(),
					ETag:         `"test-etag"`,
					Size:         int64(len(v)),
				})
			}
			rec.Header().Set("Content-Type", "application/xml")
			rec.WriteHeader(http.StatusOK)
			_ = xml.NewEncoder(rec).Encode(res)
			return rec.Result(), nil
		}
		data, ok := m.stored[key]
		if !ok {
			rec.WriteHeader(http.StatusNotFound)
			return rec.Result(), nil
		}
		rec.Header().Set("Content-Type", "text/plain")
		rec.Header().Set("Content-Length", strconv.Itoa(len(data)))
		rec.Header().Set("ETag", `"test-etag"`)
		rec.WriteHeader(http.StatusOK)
		_, _ = rec.Write(data)
	case http.MethodDelete:
		delete(m.stored, key)
		rec.WriteHeader(http.StatusNoContent)
	default:
		rec.WriteHeader(http.StatusMethodNotAllowed)
	}

	return rec.Result(), nil
}

func TestStorageS3Operations(t *testing.T) {
	mock := &s3MockTransport{
		stored: make(map[string][]byte),
		t:      t,
	}

	ctx := context.Background()
	res, closer, err := openS3Storage(ctx, ResourceSpec{
		Name: "test-s3",
		Config: map[string]any{
			"bucket":         "test-bucket",
			"endpoint":       "http://s3.local",
			"region":         "us-east-1",
			"access_key":     "AKIAIOSFODNN7EXAMPLE",
			"secret_key":     "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
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

	// 1. Put
	info, err := store.Put(ctx, "documents/report.txt", bytes.NewBufferString("hello world"), "text/plain")
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if info.Key != "documents/report.txt" || info.Size != 11 {
		t.Fatalf("Put unexpected info: %+v", info)
	}

	// 2. Get
	rc, getInfo, err := store.Get(ctx, "documents/report.txt")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != "hello world" {
		t.Fatalf("Get unexpected body: %q", string(body))
	}
	if getInfo.Size != 11 {
		t.Fatalf("Get unexpected size: %d", getInfo.Size)
	}

	// 3. List
	list, err := store.List(ctx, "documents/", 10)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 || list[0].Key != "documents/report.txt" {
		t.Fatalf("List unexpected results: %+v", list)
	}

	// 4. Presign
	presigned, err := store.Presign(ctx, "documents/report.txt", "GET", 15*time.Minute)
	if err != nil {
		t.Fatalf("Presign failed: %v", err)
	}
	if !strings.Contains(presigned, "X-Amz-Signature=") {
		t.Fatalf("Presigned URL missing signature: %s", presigned)
	}

	// 5. Delete
	if err := store.Delete(ctx, "documents/report.txt"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// 6. Get after delete should return not found
	_, _, err = store.Get(ctx, "documents/report.txt")
	if err == nil {
		t.Fatalf("expected error after delete, got nil")
	}
}
