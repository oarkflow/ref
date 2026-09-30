package preview

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// maxRewriteBody bounds the HTML bodies that are buffered for link rewriting.
const maxRewriteBody = 8 << 20

// attrRE matches attributes whose value is a root-relative URL ("/x", not
// "//host"), in double or single quotes.
var attrRE = regexp.MustCompile(`(?i)(\s(?:href|src|action|formaction|poster|data-src)\s*=\s*)(["'])/([^/"'][^"']*|)(["'])`)

// rewriteLocation maps a redirect target under the prefix. Relative and
// foreign locations pass through.
func rewriteLocation(loc, prefix string) string {
	if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") && !strings.HasPrefix(loc, prefix+"/") && loc != prefix {
		return prefix + loc
	}
	return loc
}

// rewriteCookie rewrites a Set-Cookie header's Path so the cookie is scoped to
// this preview alone and does not collide with other drafts or with Studio.
func rewriteCookie(v, prefix string) string {
	parts := strings.Split(v, ";")
	found := false
	for i, p := range parts {
		t := strings.TrimSpace(p)
		if len(t) >= 5 && strings.EqualFold(t[:5], "path=") {
			path := strings.TrimSpace(t[5:])
			if path != prefix && !strings.HasPrefix(path, prefix+"/") {
				parts[i] = " Path=" + prefix + "/" + strings.TrimPrefix(path, "/")
			}
			found = true
		}
	}
	if !found {
		parts = append(parts, " Path="+prefix+"/")
	}
	return strings.Join(parts, ";")
}

// rewriteHTML prefixes root-relative links so a page rendered by the app still
// works when served under /preview/{id}. Links assembled by scripts at run time
// cannot be rewritten; the response carries X-Forwarded-Prefix for apps that
// want to build them correctly.
func rewriteHTML(body []byte, prefix string) []byte {
	return attrRE.ReplaceAll(body, []byte("${1}${2}"+prefix+"/${3}${4}"))
}

// modifyResponse applies the preview's response rewrites.
func modifyResponse(resp *http.Response, prefix string) error {
	if loc := resp.Header.Get("Location"); loc != "" {
		resp.Header.Set("Location", rewriteLocation(loc, prefix))
	}
	if cookies := resp.Header.Values("Set-Cookie"); len(cookies) > 0 {
		resp.Header.Del("Set-Cookie")
		for _, c := range cookies {
			resp.Header.Add("Set-Cookie", rewriteCookie(c, prefix))
		}
	}
	// The preview is shown in an iframe of the same origin; a DENY / 'none'
	// framing policy from the app would blank it.
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("X-Frame-Options")), "deny") {
		resp.Header.Set("X-Frame-Options", "SAMEORIGIN")
	}
	if csp := resp.Header.Get("Content-Security-Policy"); strings.Contains(csp, "frame-ancestors 'none'") {
		resp.Header.Set("Content-Security-Policy", strings.ReplaceAll(csp, "frame-ancestors 'none'", "frame-ancestors 'self'"))
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, "text/html") || resp.Header.Get("Content-Encoding") != "" {
		return nil
	}
	if resp.ContentLength > maxRewriteBody {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRewriteBody+1))
	if err != nil {
		_ = resp.Body.Close()
		return err
	}
	if len(body) > maxRewriteBody {
		// Too large to rewrite: pass everything through unchanged.
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return nil
	}
	_ = resp.Body.Close()
	body = rewriteHTML(body, prefix)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}
