package platform

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oarkflow/ref/platform/spi"
)

func registerS3StorageResource(r *Registry) {
	mustResource(r, "storage.s3", ResourceFactoryFunc(openS3Storage), ResourceKindInfo{
		Family:   "storage",
		Summary:  "S3-compatible object storage (AWS S3, MinIO, Cloudflare R2, Google Cloud Storage, Ceph).",
		Provides: []string{"ObjectStore"},
		Config: []ConfigField{
			{Name: "bucket", Type: "string", Required: true, Summary: "Target S3 bucket name"},
			{Name: "endpoint", Type: "string", Summary: "S3 endpoint URL (defaults to AWS S3; use http://minio:9000 or Cloudflare R2 URL for others)"},
			{Name: "region", Type: "string", Default: "us-east-1", Summary: "AWS region"},
			{Name: "access_key", Type: "string", Summary: "Access Key ID (falls back to AWS_ACCESS_KEY_ID env)"},
			{Name: "secret_key", Type: "string", Summary: "Secret Access Key (falls back to AWS_SECRET_ACCESS_KEY env)"},
			{Name: "session_token", Type: "string", Summary: "Optional session token for temporary credentials"},
			{Name: "use_path_style", Type: "bool", Default: "false", Summary: "Use path-style URL (http://endpoint/bucket/key) instead of virtual-host style"},
			{Name: "max_object_bytes", Type: "int", Default: "1073741824", Summary: "Max object bytes allowed for upload (default 1GB)"},
		},
	})
}

type s3Storage struct {
	bucket       string
	endpoint     string
	region       string
	accessKey    string
	secretKey    string
	sessionToken string
	pathStyle    bool
	maxBytes     int64
	client       *http.Client
}

func openS3Storage(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("storage.s3", spec.Config,
		"bucket", "endpoint", "region", "access_key", "secret_key", "session_token", "use_path_style", "max_object_bytes",
	); err != nil {
		return nil, nil, err
	}

	bucket, err := requiredString(spec.Config, "bucket")
	if err != nil {
		return nil, nil, fmt.Errorf("storage.s3 %q: %w", spec.Name, err)
	}

	endpoint := configString(spec.Config, "endpoint", "")
	region := configString(spec.Config, "region", "us-east-1")
	accessKey := configString(spec.Config, "access_key", os.Getenv("AWS_ACCESS_KEY_ID"))
	secretKey := configString(spec.Config, "secret_key", os.Getenv("AWS_SECRET_ACCESS_KEY"))
	sessionToken := configString(spec.Config, "session_token", os.Getenv("AWS_SESSION_TOKEN"))
	pathStyle := configBool(spec.Config, "use_path_style", false)
	maxBytes, err := configInt64(spec.Config, "max_object_bytes", 1<<30)
	if err != nil {
		return nil, nil, err
	}

	if endpoint == "" {
		if region == "us-east-1" {
			endpoint = "https://s3.us-east-1.amazonaws.com"
		} else {
			endpoint = fmt.Sprintf("https://s3.%s.amazonaws.com", region)
		}
	} else if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}

	s := &s3Storage{
		bucket:       bucket,
		endpoint:     strings.TrimRight(endpoint, "/"),
		region:       region,
		accessKey:    accessKey,
		secretKey:    secretKey,
		sessionToken: sessionToken,
		pathStyle:    pathStyle,
		maxBytes:     maxBytes,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
	return s, nil, nil
}

func (s *s3Storage) objectURL(key string) (*url.URL, error) {
	cleanKey := strings.TrimPrefix(path.Clean("/"+key), "/")
	if cleanKey == "" || cleanKey == "." {
		return nil, errors.New("storage.s3: object key cannot be empty")
	}

	base, err := url.Parse(s.endpoint)
	if err != nil {
		return nil, fmt.Errorf("storage.s3: invalid endpoint: %w", err)
	}

	if s.pathStyle {
		base.Path = path.Join(base.Path, s.bucket, cleanKey)
	} else {
		// Virtual-hosted style
		if strings.Contains(base.Host, "localhost") || strings.Contains(base.Host, "127.0.0.1") {
			base.Path = path.Join(base.Path, s.bucket, cleanKey)
		} else {
			base.Host = s.bucket + "." + base.Host
			base.Path = path.Join(base.Path, cleanKey)
		}
	}
	return base, nil
}

// Put writes an object to S3.
func (s *s3Storage) Put(ctx context.Context, key string, body io.Reader, contentType string) (spi.ObjectInfo, error) {
	targetURL, err := s.objectURL(key)
	if err != nil {
		return spi.ObjectInfo{}, err
	}

	// Buffer or stream body up to maxBytes
	var buf bytes.Buffer
	hasher := sha256.New()
	mw := io.MultiWriter(&buf, hasher)
	written, err := io.Copy(mw, io.LimitReader(body, s.maxBytes+1))
	if err != nil {
		return spi.ObjectInfo{}, fmt.Errorf("storage.s3 put: read body: %w", err)
	}
	if written > s.maxBytes {
		return spi.ObjectInfo{}, fmt.Errorf("storage.s3 put: object exceeds limit of %d bytes", s.maxBytes)
	}

	payloadHash := hex.EncodeToString(hasher.Sum(nil))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, targetURL.String(), bytes.NewReader(buf.Bytes()))
	if err != nil {
		return spi.ObjectInfo{}, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Length", strconv.FormatInt(written, 10))

	s.sign(req, payloadHash, time.Now().UTC())

	resp, err := s.client.Do(req)
	if err != nil {
		return spi.ObjectInfo{}, fmt.Errorf("storage.s3 put %q: %w", key, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return spi.ObjectInfo{}, fmt.Errorf("storage.s3 put %q failed (status %d): %s", key, resp.StatusCode, string(respBody))
	}

	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if etag == "" {
		etag = payloadHash
	}

	return spi.ObjectInfo{
		Key:         key,
		Size:        written,
		ContentType: contentType,
		ETag:        etag,
		ModifiedAt:  time.Now().UTC(),
	}, nil
}

// Get reads an object from S3.
func (s *s3Storage) Get(ctx context.Context, key string) (io.ReadCloser, spi.ObjectInfo, error) {
	targetURL, err := s.objectURL(key)
	if err != nil {
		return nil, spi.ObjectInfo{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return nil, spi.ObjectInfo{}, err
	}

	emptyHash := hex.EncodeToString(sha256.New().Sum(nil))
	s.sign(req, emptyHash, time.Now().UTC())

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, spi.ObjectInfo{}, fmt.Errorf("storage.s3 get %q: %w", key, err)
	}

	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, spi.ObjectInfo{}, notFound("object", key)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return nil, spi.ObjectInfo{}, fmt.Errorf("storage.s3 get %q failed (status %d): %s", key, resp.StatusCode, string(respBody))
	}

	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	contentType := resp.Header.Get("Content-Type")
	size := resp.ContentLength
	if size < 0 {
		if cl := resp.Header.Get("Content-Length"); cl != "" {
			if parsed, err := strconv.ParseInt(cl, 10, 64); err == nil {
				size = parsed
			}
		}
	}
	lastMod, _ := http.ParseTime(resp.Header.Get("Last-Modified"))

	info := spi.ObjectInfo{
		Key:         key,
		Size:        size,
		ContentType: contentType,
		ETag:        etag,
		ModifiedAt:  lastMod.UTC(),
	}
	return resp.Body, info, nil
}

// Delete removes an object from S3.
func (s *s3Storage) Delete(ctx context.Context, key string) error {
	targetURL, err := s.objectURL(key)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, targetURL.String(), nil)
	if err != nil {
		return err
	}

	emptyHash := hex.EncodeToString(sha256.New().Sum(nil))
	s.sign(req, emptyHash, time.Now().UTC())

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("storage.s3 delete %q: %w", key, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("storage.s3 delete %q failed (status %d): %s", key, resp.StatusCode, string(respBody))
	}
	return nil
}

type s3ListResult struct {
	XMLName  xml.Name `xml:"ListBucketResult"`
	Contents []struct {
		Key          string    `xml:"Key"`
		LastModified time.Time `xml:"LastModified"`
		ETag         string    `xml:"ETag"`
		Size         int64     `xml:"Size"`
	} `xml:"Contents"`
}

// List lists objects under prefix.
func (s *s3Storage) List(ctx context.Context, prefix string, limit int) ([]spi.ObjectInfo, error) {
	if limit <= 0 {
		limit = 1000
	}

	base, err := url.Parse(s.endpoint)
	if err != nil {
		return nil, err
	}

	if s.pathStyle {
		base.Path = path.Join(base.Path, s.bucket)
	} else {
		if strings.Contains(base.Host, "localhost") || strings.Contains(base.Host, "127.0.0.1") {
			base.Path = path.Join(base.Path, s.bucket)
		} else {
			base.Host = s.bucket + "." + base.Host
		}
	}

	q := base.Query()
	q.Set("list-type", "2")
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	q.Set("max-keys", strconv.Itoa(limit))
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}

	emptyHash := hex.EncodeToString(sha256.New().Sum(nil))
	s.sign(req, emptyHash, time.Now().UTC())

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("storage.s3 list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("storage.s3 list failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result s3ListResult
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("storage.s3 list: decode xml: %w", err)
	}

	out := make([]spi.ObjectInfo, 0, len(result.Contents))
	for _, c := range result.Contents {
		out = append(out, spi.ObjectInfo{
			Key:        c.Key,
			Size:       c.Size,
			ETag:       strings.Trim(c.ETag, `"`),
			ModifiedAt: c.LastModified.UTC(),
		})
	}
	return out, nil
}

// Presign mints an AWS Signature Version 4 pre-signed URL.
func (s *s3Storage) Presign(_ context.Context, key string, method string, ttl time.Duration) (string, error) {
	targetURL, err := s.objectURL(key)
	if err != nil {
		return "", err
	}

	if method == "" {
		method = http.MethodGet
	}
	method = strings.ToUpper(method)

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	expiresSecs := int(ttl.Seconds())
	if expiresSecs <= 0 {
		expiresSecs = 900 // 15 mins default
	}
	if expiresSecs > 604800 {
		expiresSecs = 604800 // S3 max 7 days
	}

	credential := fmt.Sprintf("%s/%s/%s/s3/aws4_request", s.accessKey, dateStamp, s.region)

	q := targetURL.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", credential)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.Itoa(expiresSecs))
	q.Set("X-Amz-SignedHeaders", "host")
	if s.sessionToken != "" {
		q.Set("X-Amz-Security-Token", s.sessionToken)
	}

	// Canonical query string
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var canonicalQuery strings.Builder
	for i, k := range keys {
		if i > 0 {
			canonicalQuery.WriteString("&")
		}
		canonicalQuery.WriteString(url.QueryEscape(k))
		canonicalQuery.WriteString("=")
		canonicalQuery.WriteString(url.QueryEscape(q.Get(k)))
	}

	canonicalHeaders := "host:" + targetURL.Host + "\n"
	signedHeaders := "host"
	payloadHash := "UNSIGNED-PAYLOAD"

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		method,
		targetURL.EscapedPath(),
		canonicalQuery.String(),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	)

	reqHash := hex.EncodeToString(sha256Hash([]byte(canonicalRequest)))
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s/%s/s3/aws4_request\n%s",
		amzDate, dateStamp, s.region, reqHash)

	signingKey := s.deriveSigningKey(dateStamp)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	q.Set("X-Amz-Signature", signature)
	targetURL.RawQuery = q.Encode()

	return targetURL.String(), nil
}

// AWS SigV4 implementation for standard HTTP requests.
func (s *s3Storage) sign(req *http.Request, payloadHash string, t time.Time) {
	if s.accessKey == "" || s.secretKey == "" {
		return // anonymous / local non-authenticated access
	}

	amzDate := t.Format("20060102T150405Z")
	dateStamp := t.Format("20060102")

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if s.sessionToken != "" {
		req.Header.Set("x-amz-security-token", s.sessionToken)
	}

	// Canonical headers
	headersToSign := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if s.sessionToken != "" {
		headersToSign = append(headersToSign, "x-amz-security-token")
	}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		headersToSign = append(headersToSign, "content-type")
	}
	sort.Strings(headersToSign)

	var canonicalHeaders strings.Builder
	for _, h := range headersToSign {
		val := ""
		if h == "host" {
			val = req.URL.Host
		} else {
			val = strings.TrimSpace(req.Header.Get(h))
		}
		canonicalHeaders.WriteString(strings.ToLower(h) + ":" + val + "\n")
	}

	signedHeaders := strings.Join(headersToSign, ";")

	// Canonical query string
	query := req.URL.Query()
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var canonicalQuery strings.Builder
	for i, k := range keys {
		if i > 0 {
			canonicalQuery.WriteString("&")
		}
		canonicalQuery.WriteString(url.QueryEscape(k))
		canonicalQuery.WriteString("=")
		canonicalQuery.WriteString(url.QueryEscape(query.Get(k)))
	}

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		req.Method,
		req.URL.EscapedPath(),
		canonicalQuery.String(),
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	)

	reqHash := hex.EncodeToString(sha256Hash([]byte(canonicalRequest)))
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s/%s/s3/aws4_request\n%s",
		amzDate, dateStamp, s.region, reqHash)

	signingKey := s.deriveSigningKey(dateStamp)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s/%s/s3/aws4_request, SignedHeaders=%s, Signature=%s",
		s.accessKey, dateStamp, s.region, signedHeaders, signature)

	req.Header.Set("Authorization", authHeader)
}

func (s *s3Storage) deriveSigningKey(dateStamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+s.secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(s.region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}

func hmacSHA256(key []byte, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func sha256Hash(data []byte) []byte {
	h := sha256.New()
	h.Write(data)
	return h.Sum(nil)
}

var (
	_ spi.ObjectStore     = (*s3Storage)(nil)
	_ spi.ObjectPresigner = (*s3Storage)(nil)
)
