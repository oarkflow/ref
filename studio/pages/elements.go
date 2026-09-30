package pages

import (
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Element is something a visitor can do on a page: follow a link, submit a
// form, press a button, or trigger a request from a script. The Studio
// journey view joins these to the routes they reach.
type Element struct {
	// Kind is link, form, button or fetch (a request made by a script or an
	// hx-* attribute).
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Method is the HTTP method: GET for links and forms without a method.
	Method string `json:"method,omitempty"`
	// URL is the normalised target path: "${todo.id}" and numeric segments
	// become ":param", the query and fragment are removed. Empty when the
	// element has no target (a UI-only button).
	URL string `json:"url,omitempty"`
	// Query is the target's query string without the redirect parameter.
	Query string `json:"query,omitempty"`
	// RawURL is the target as written.
	RawURL string `json:"rawUrl,omitempty"`
	// Redirect is where the app sends the visitor after the request, from
	// the "?redirect=/todos" convention (the parameter names redirect, next,
	// return_to and return are read), normalised like URL.
	Redirect string `json:"redirect,omitempty"`
	// External is true for an absolute http(s) URL; URL then holds host and
	// path.
	External bool `json:"external,omitempty"`
	// Dynamic is true when the target is computed at run time and cannot be
	// matched to a route.
	Dynamic bool `json:"dynamic,omitempty"`
	// Self is true for a form without an action: it posts to the URL of the
	// page that contains it.
	Self bool `json:"self,omitempty"`
	// Fields are the names of a form's inputs (hidden CSRF fields excluded).
	Fields []string `json:"fields,omitempty"`
	// Hints are clues about behaviour the scanner cannot follow: data-*
	// attributes, onclick handlers, "script".
	Hints []string `json:"hints,omitempty"`
	// Line is the 1-based line in Source.
	Line int `json:"line"`
	// Source is the file the element was found in: a template relative to
	// the templates directory ("components/navbar.html") or a script
	// ("static/js/todo-subtasks.js"). Set by PageElements.
	Source string `json:"source,omitempty"`
	// Shared is true when the element comes from a file other than the page
	// itself (its layout or a component), so every page using that file has it.
	Shared bool `json:"shared,omitempty"`
	// Via is the template that loads a script, for elements found in scripts.
	Via string `json:"via,omitempty"`
}

// URLInfo is a normalised link target.
type URLInfo struct {
	Path     string
	Query    string
	Redirect string
	External bool
	Dynamic  bool
	Skip     bool // #anchor, javascript:, mailto:, tel:, data:
}

var (
	spl1       = regexp.MustCompile(`\$\{[^}]*\}`)
	spl2       = regexp.MustCompile(`\{\{[^}]*\}\}`)
	numSeg     = regexp.MustCompile(`^[0-9]+$`)
	redirKeys  = []string{"redirect", "redirect_to", "next", "return_to", "return"}
	spaceRun   = regexp.MustCompile(`\s+`)
	locRE      = regexp.MustCompile(`(?:window\.|document\.)?location(?:\.href)?\s*=\s*`)
	locCallRE  = regexp.MustCompile(`location\.(?:assign|replace)\s*\(\s*`)
	fetchRE    = regexp.MustCompile(`\bfetch\s*\(\s*`)
	xhrRE      = regexp.MustCompile(`\.open\s*\(\s*['"]([A-Za-z]+)['"]\s*,\s*`)
	axiosRE    = regexp.MustCompile(`\baxios\.(get|post|put|patch|delete)\s*\(\s*`)
	jqRE       = regexp.MustCompile(`\$\.(get|post|getJSON)\s*\(\s*`)
	methodOpt  = regexp.MustCompile("method\\s*:\\s*['\"`]([A-Za-z]+)['\"`]")
	hxAttrs    = []string{"hx-get", "hx-post", "hx-put", "hx-patch", "hx-delete"}
	assetExt   = map[string]bool{".css": true, ".js": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".ico": true, ".woff": true, ".woff2": true, ".webp": true, ".map": true}
	scriptSkip = regexp.MustCompile(`^(?:https?:)?//`)
)

// NormalizeURL reduces a link, form action or request URL to the path a
// route could match. Template expressions ("${x}", "{{x}}") and numeric
// segments become ":param". A URL that is nothing but an expression is
// Dynamic.
func NormalizeURL(raw string) URLInfo {
	u := strings.TrimSpace(raw)
	switch {
	case u == "", strings.HasPrefix(u, "#"):
		return URLInfo{Skip: true}
	}
	low := strings.ToLower(u)
	for _, p := range []string{"javascript:", "mailto:", "tel:", "data:", "sms:", "blob:"} {
		if strings.HasPrefix(low, p) {
			return URLInfo{Skip: true}
		}
	}
	u = spl1.ReplaceAllString(u, ":param")
	u = spl2.ReplaceAllString(u, ":param")
	var info URLInfo
	if scriptSkip.MatchString(u) {
		info.External = true
	}
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	rawPath, query, _ := strings.Cut(u, "?")
	if info.External {
		rawPath = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(rawPath, "https://"), "http://"), "//")
		info.Path = normalizeSegments(rawPath, false)
	} else {
		info.Path = normalizeSegments(rawPath, true)
	}
	if strings.Trim(info.Path, "/") == ":param" || info.Path == "" && strings.Contains(u, ":param") {
		info.Dynamic, info.Path = true, ""
	}
	var rest []string
	for _, kv := range strings.Split(query, "&") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		if info.Redirect == "" && isRedirectKey(k) {
			if dv, err := url.QueryUnescape(v); err == nil {
				v = dv
			}
			if r := NormalizeURL(v); !r.Skip && !r.Dynamic {
				info.Redirect = r.Path
				continue
			}
			if v = strings.TrimSpace(v); v != "" && !strings.Contains(v, ":param") {
				info.Redirect = v
			}
			continue
		}
		rest = append(rest, kv)
	}
	info.Query = strings.Join(rest, "&")
	return info
}

func isRedirectKey(k string) bool {
	for _, r := range redirKeys {
		if k == r {
			return true
		}
	}
	return false
}

// normalizeSegments cleans a path: duplicate slashes collapse, numeric and
// expression segments become ":param". With rooted, a relative path is kept
// relative (it will not match a route).
func normalizeSegments(p string, rooted bool) string {
	if p == "" {
		return ""
	}
	lead := strings.HasPrefix(p, "/")
	out := make([]string, 0, 4)
	for _, s := range strings.Split(p, "/") {
		if s == "" {
			continue
		}
		if numSeg.MatchString(s) || strings.Contains(s, ":param") {
			s = ":param"
		}
		out = append(out, s)
	}
	res := strings.Join(out, "/")
	if lead || !rooted {
		return "/" + res
	}
	return res // relative: kept as written, it will not match a route
}

// Elements lists the interactive elements of an SPL/HTML template: links,
// forms (with their submit label and input names), buttons and hx-*
// requests, plus the requests made by its inline scripts. SPL directives are
// left alone; the HTML tokenizer treats them as text.
func Elements(src string) []Element {
	els, _ := parseElements(src)
	return els
}

// Scripts lists the external scripts a template loads (<script src>), as
// written.
func Scripts(src string) []string {
	_, s := parseElements(src)
	return s
}

// Heading guesses a page's title: its first <h1>, else its <title>.
func Heading(src string) string {
	tz := html.NewTokenizer(strings.NewReader(src))
	want := ""
	var b strings.Builder
	for {
		tt := tz.Next()
		switch tt {
		case html.ErrorToken:
			return ""
		case html.StartTagToken:
			name, _ := tz.TagName()
			n := string(name)
			if want == "" && (n == "h1" || n == "title") {
				want = n
				b.Reset()
			}
		case html.TextToken:
			if want != "" {
				b.Write(tz.Text())
			}
		case html.EndTagToken:
			name, _ := tz.TagName()
			if want != "" && string(name) == want {
				if t := cleanText(b.String()); strings.Trim(t, "… ") != "" {
					return t
				}
				want = ""
			}
		}
	}
}

func cleanText(s string) string {
	s = spl1.ReplaceAllString(s, "…")
	s = spl2.ReplaceAllString(s, "…")
	s = spaceRun.ReplaceAllString(strings.TrimSpace(s), " ")
	if len(s) > 80 {
		s = s[:79] + "…"
	}
	return s
}

func attr(t html.Token, name string) (string, bool) {
	for _, a := range t.Attr {
		if a.Key == name {
			return a.Val, true
		}
	}
	return "", false
}

type formCtx struct {
	idx    int // index into els
	submit string
}

func parseElements(src string) ([]Element, []string) {
	var els []Element
	var scripts []string
	seen := map[string]bool{}
	add := func(e Element) int {
		key := e.Kind + "|" + e.Method + "|" + e.URL + "|" + e.Label + "|" + e.Redirect + "|" + strings.Join(e.Fields, ",")
		if e.Kind != "form" && seen[key] {
			return -1
		}
		seen[key] = true
		els = append(els, e)
		return len(els) - 1
	}

	tz := html.NewTokenizer(strings.NewReader(src))
	line := 1
	var forms []*formCtx
	collect := -1 // element whose text is being gathered (<a>, <button>)
	var collectTag string
	var text strings.Builder
	inScript := false
	scriptLine := 0
	var scriptSrc strings.Builder

	finish := func() {
		if collect >= 0 && collect < len(els) {
			if t := cleanText(text.String()); t != "" && els[collect].Label == "" {
				els[collect].Label = t
			}
			if els[collect].Label == "" {
				els[collect].Label = defaultLabel(els[collect])
			}
		}
		collect, collectTag = -1, ""
		text.Reset()
	}

	for {
		tt := tz.Next()
		raw := tz.Raw()
		startLine := line
		line += strings.Count(string(raw), "\n")
		switch tt {
		case html.ErrorToken:
			finish()
			return dedupeForms(els), scripts

		case html.TextToken:
			if inScript {
				scriptSrc.Write(raw)
				continue
			}
			if collectTag != "" {
				text.Write(tz.Text())
				text.WriteByte(' ')
			}

		case html.StartTagToken, html.SelfClosingTagToken:
			t := tz.Token()
			tag := t.Data
			selfClosing := tt == html.SelfClosingTagToken
			switch tag {
			case "script":
				if s, ok := attr(t, "src"); ok && strings.TrimSpace(s) != "" {
					scripts = append(scripts, strings.TrimSpace(s))
				} else if !selfClosing {
					inScript, scriptLine = true, startLine
					scriptSrc.Reset()
				}
				continue
			case "a":
				finish()
				href, ok := attr(t, "href")
				if !ok {
					break
				}
				e := linkElement(t, "link", "GET", href, startLine)
				if e == nil {
					break
				}
				if hx := hxTarget(t); hx != nil {
					e = hx
					e.Line = startLine
				}
				if i := add(*e); i >= 0 {
					collect, collectTag = i, "a"
					text.Reset()
				}
			case "form":
				finish()
				method := strings.ToUpper(strings.TrimSpace(firstAttr(t, "method")))
				if method == "" || !isMethod(method) {
					method = "GET"
				}
				action, hasAction := attr(t, "action")
				e := Element{Kind: "form", Method: method, Line: startLine}
				if hasAction && strings.TrimSpace(action) != "" {
					applyURL(&e, action)
				} else if hx := hxTarget(t); hx != nil {
					e.Method, e.URL, e.RawURL, e.Query, e.Redirect, e.External = hx.Method, hx.URL, hx.RawURL, hx.Query, hx.Redirect, hx.External
				} else {
					e.Self = true
				}
				e.Label = firstNonEmpty(firstAttr(t, "aria-label"), firstAttr(t, "title"))
				e.Hints = hintsOf(t)
				i := add(e)
				forms = append(forms, &formCtx{idx: i})
			case "input", "select", "textarea":
				if len(forms) == 0 {
					break
				}
				f := forms[len(forms)-1]
				name, _ := attr(t, "name")
				typ := strings.ToLower(firstAttr(t, "type"))
				if tag == "input" && (typ == "submit" || typ == "image") {
					if f.submit == "" {
						f.submit = firstNonEmpty(firstAttr(t, "value"), firstAttr(t, "alt"))
					}
					break
				}
				if name != "" && f.idx >= 0 && !strings.HasPrefix(strings.ToLower(name), "csrf") && !strings.HasPrefix(name, "_csrf") &&
					!strings.Contains(name, ":param") && !strings.HasPrefix(name, "${") {
					if !contains(els[f.idx].Fields, name) {
						els[f.idx].Fields = append(els[f.idx].Fields, name)
					}
				}
			case "button":
				finish()
				typ := strings.ToLower(firstAttr(t, "type"))
				inForm := len(forms) > 0 && forms[len(forms)-1].idx >= 0
				if inForm && typ != "button" && typ != "reset" {
					// A submit button: its label names the form's action.
					collect, collectTag = -2, "button" // -2: label goes to the form
					text.Reset()
					if fa, ok := attr(t, "formaction"); ok && strings.TrimSpace(fa) != "" {
						f := forms[len(forms)-1]
						applyURL(&els[f.idx], fa)
						els[f.idx].Self = false
						if m := strings.ToUpper(firstAttr(t, "formmethod")); isMethod(m) {
							els[f.idx].Method = m
						}
					}
					if al := firstNonEmpty(firstAttr(t, "aria-label"), firstAttr(t, "title")); al != "" && forms[len(forms)-1].submit == "" {
						forms[len(forms)-1].submit = al
					}
					break
				}
				e := Element{Kind: "button", Line: startLine, Hints: hintsOf(t)}
				e.Label = firstNonEmpty(firstAttr(t, "aria-label"), firstAttr(t, "title"))
				switch {
				case hxTarget(t) != nil:
					hx := hxTarget(t)
					e.Method, e.URL, e.RawURL, e.Query, e.Redirect, e.External = hx.Method, hx.URL, hx.RawURL, hx.Query, hx.Redirect, hx.External
				default:
					for _, k := range []string{"formaction", "data-href", "data-url", "data-action", "data-endpoint"} {
						if v, ok := attr(t, k); ok && strings.TrimSpace(v) != "" {
							applyURL(&e, v)
							e.Method = firstNonEmpty(strings.ToUpper(firstAttr(t, "formmethod")), strings.ToUpper(firstAttr(t, "data-method")), "GET")
							break
						}
					}
				}
				if e.URL == "" {
					if oc, ok := attr(t, "onclick"); ok {
						if u, ok := onclickTarget(oc); ok {
							applyURL(&e, u)
							e.Method = "GET"
						}
					}
				}
				if e.URL == "" && len(e.Hints) == 0 && firstAttr(t, "id") == "" {
					// A plain button that does nothing the scanner can see.
					if !selfClosing {
						collect, collectTag = -3, "button"
						text.Reset()
					}
					break
				}
				if id := firstAttr(t, "id"); id != "" {
					e.Hints = append(e.Hints, "#"+id)
				}
				if i := add(e); i >= 0 && !selfClosing {
					collect, collectTag = i, "button"
					text.Reset()
				}
			default:
				if hx := hxTarget(t); hx != nil {
					hx.Line = startLine
					hx.Kind = "fetch"
					hx.Label = firstNonEmpty(firstAttr(t, "aria-label"), firstAttr(t, "title"), "Request on "+tag)
					hx.Hints = append(hintsOf(t), "hx")
					add(*hx)
				} else if oc, ok := attr(t, "onclick"); ok && (tag == "div" || tag == "span" || tag == "li" || tag == "tr" || tag == "td") {
					if u, ok := onclickTarget(oc); ok {
						e := Element{Kind: "link", Method: "GET", Line: startLine, Label: firstNonEmpty(firstAttr(t, "aria-label"), firstAttr(t, "title"), "Clickable "+tag), Hints: []string{"onclick"}}
						applyURL(&e, u)
						add(e)
					}
				}
			}

		case html.EndTagToken:
			name, _ := tz.TagName()
			tag := string(name)
			switch tag {
			case "script":
				if inScript {
					for _, e := range ScriptElements(scriptSrc.String(), scriptLine) {
						add(e)
					}
					inScript = false
				}
			case "a":
				if collectTag == "a" {
					finish()
				}
			case "button":
				if collectTag == "button" {
					if collect == -2 && len(forms) > 0 {
						f := forms[len(forms)-1]
						if f.submit == "" {
							f.submit = cleanText(text.String())
						}
					}
					finish()
				}
			case "form":
				finish()
				if n := len(forms); n > 0 {
					f := forms[n-1]
					forms = forms[:n-1]
					if f.idx >= 0 && f.idx < len(els) {
						e := &els[f.idx]
						if e.Label == "" {
							e.Label = f.submit
						}
						if e.Label == "" {
							e.Label = defaultLabel(*e)
						}
					}
				}
			}
		}
	}
}

func isMethod(m string) bool {
	switch m {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	}
	return false
}

func firstAttr(t html.Token, name string) string {
	v, _ := attr(t, name)
	return v
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// applyURL fills the target fields of e from a URL as written.
func applyURL(e *Element, raw string) {
	info := NormalizeURL(raw)
	e.RawURL = strings.TrimSpace(raw)
	if info.Skip {
		e.RawURL = ""
		return
	}
	e.URL, e.Query, e.Redirect, e.External, e.Dynamic = info.Path, info.Query, info.Redirect, info.External, info.Dynamic
}

func linkElement(t html.Token, kind, method, href string, line int) *Element {
	info := NormalizeURL(href)
	if info.Skip {
		return nil
	}
	e := &Element{Kind: kind, Method: method, Line: line, Hints: hintsOf(t)}
	applyURL(e, href)
	e.Label = firstNonEmpty(firstAttr(t, "aria-label"), firstAttr(t, "title"))
	return e
}

func hxTarget(t html.Token) *Element {
	for _, a := range hxAttrs {
		if v, ok := attr(t, a); ok && strings.TrimSpace(v) != "" {
			e := &Element{Kind: "fetch", Method: strings.ToUpper(strings.TrimPrefix(a, "hx-"))}
			applyURL(e, v)
			return e
		}
	}
	return nil
}

func hintsOf(t html.Token) []string {
	var h []string
	for _, a := range t.Attr {
		switch {
		case a.Key == "onclick" || a.Key == "onsubmit":
			h = append(h, a.Key)
		case strings.HasPrefix(a.Key, "data-") && a.Val != "" && len(h) < 6:
			v := a.Val
			if len(v) > 40 {
				v = v[:40] + "…"
			}
			h = append(h, a.Key+"="+v)
		case strings.HasPrefix(a.Key, "data-") && len(h) < 6:
			h = append(h, a.Key)
		}
	}
	return h
}

func defaultLabel(e Element) string {
	switch e.Kind {
	case "form":
		if e.Method != "" && e.Method != "GET" {
			return "Submit form"
		}
		return "Search form"
	case "link":
		return "Link"
	case "button":
		return "Button"
	}
	return "Request"
}

var onclickLoc = regexp.MustCompile(`(?:window\.|document\.)?location(?:\.href)?\s*=\s*['"]([^'"]+)['"]|location\.(?:assign|replace)\(\s*['"]([^'"]+)['"]`)

func onclickTarget(js string) (string, bool) {
	m := onclickLoc.FindStringSubmatch(js)
	if m == nil {
		return "", false
	}
	if m[1] != "" {
		return m[1], true
	}
	return m[2], m[2] != ""
}

// ---------------------------------------------------------------------------
// Scripts
// ---------------------------------------------------------------------------

// ScriptElements finds the requests a script makes: fetch(), XMLHttpRequest
// .open(), axios and jQuery helpers, and location assignments. URLs built
// with string concatenation or template literals are reduced like any other
// ("/api/v1/todos/" + id + "/subtasks" -> "/api/v1/todos/:param/subtasks").
// baseLine is the 1-based line of the script's first line in its file.
func ScriptElements(js string, baseLine int) []Element {
	if baseLine < 1 {
		baseLine = 1
	}
	var out []Element
	lineAt := func(idx int) int { return baseLine + strings.Count(js[:idx], "\n") }
	commented := func(idx int) bool {
		ls := strings.LastIndexByte(js[:idx], '\n') + 1
		return strings.Contains(js[ls:idx], "//")
	}
	emit := func(kind, method, rawurl string, idx int, label string) {
		e := Element{Kind: kind, Method: method, Line: lineAt(idx), Hints: []string{"script"}}
		info := NormalizeURL(rawurl)
		if info.Skip {
			return
		}
		e.RawURL = rawurl
		e.URL, e.Query, e.Redirect, e.External, e.Dynamic = info.Path, info.Query, info.Redirect, info.External, info.Dynamic
		e.Label = label
		out = append(out, e)
	}
	for _, m := range fetchRE.FindAllStringIndex(js, -1) {
		if commented(m[0]) {
			continue
		}
		u, end, ok := jsURL(js, m[1])
		if !ok {
			continue
		}
		method := "GET"
		if opts := callArgs(js, m[1]-1); opts != "" {
			if mm := methodOpt.FindStringSubmatch(opts); mm != nil {
				method = strings.ToUpper(mm[1])
			}
		}
		_ = end
		emit("fetch", method, u, m[0], "Request "+method+" "+shortURL(u))
	}
	for _, m := range xhrRE.FindAllStringSubmatchIndex(js, -1) {
		if commented(m[0]) {
			continue
		}
		method := strings.ToUpper(js[m[2]:m[3]])
		if u, _, ok := jsURL(js, m[1]); ok {
			emit("fetch", method, u, m[0], "Request "+method+" "+shortURL(u))
		}
	}
	for _, m := range axiosRE.FindAllStringSubmatchIndex(js, -1) {
		if commented(m[0]) {
			continue
		}
		method := strings.ToUpper(js[m[2]:m[3]])
		if u, _, ok := jsURL(js, m[1]); ok {
			emit("fetch", method, u, m[0], "Request "+method+" "+shortURL(u))
		}
	}
	for _, m := range jqRE.FindAllStringSubmatchIndex(js, -1) {
		if commented(m[0]) {
			continue
		}
		method := "GET"
		if js[m[2]:m[3]] == "post" {
			method = "POST"
		}
		if u, _, ok := jsURL(js, m[1]); ok {
			emit("fetch", method, u, m[0], "Request "+method+" "+shortURL(u))
		}
	}
	for _, re := range []*regexp.Regexp{locRE, locCallRE} {
		for _, m := range re.FindAllStringIndex(js, -1) {
			if commented(m[0]) || strings.Contains(js[max(0, m[0]-8):m[0]], "==") {
				continue
			}
			if u, _, ok := jsURL(js, m[1]); ok {
				emit("link", "GET", u, m[0], "Go to "+shortURL(u))
			}
		}
	}
	return out
}

func shortURL(u string) string {
	u = strings.TrimSpace(u)
	if len(u) > 48 {
		return u[:47] + "…"
	}
	return u
}

// jsURL parses a JavaScript string expression starting at i: string
// literals, template literals and identifiers joined with "+". Anything that
// is not a literal becomes ":param". It reports false when the expression is
// not a string at all.
func jsURL(js string, i int) (string, int, bool) {
	var b strings.Builder
	sawLiteral := false
	for {
		for i < len(js) && (js[i] == ' ' || js[i] == '\t' || js[i] == '\n' || js[i] == '\r') {
			i++
		}
		if i >= len(js) {
			break
		}
		c := js[i]
		switch {
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(js) && js[j] != c {
				if js[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(js) {
				return "", i, false
			}
			b.WriteString(js[i+1 : j])
			sawLiteral = true
			i = j + 1
		case c == '`':
			j := i + 1
			for j < len(js) && js[j] != '`' {
				if js[j] == '\\' {
					j++
				} else if js[j] == '$' && j+1 < len(js) && js[j+1] == '{' {
					depth, k := 1, j+2
					for k < len(js) && depth > 0 {
						switch js[k] {
						case '{':
							depth++
						case '}':
							depth--
						}
						k++
					}
					b.WriteString(":param")
					j = k
					continue
				} else {
					b.WriteByte(js[j])
				}
				j++
			}
			sawLiteral = true
			i = j + 1
		default:
			// identifier, member access or call: consume to the next
			// top-level '+', ',' or ')'.
			depth, j := 0, i
			for j < len(js) {
				ch := js[j]
				if ch == '(' || ch == '[' || ch == '{' {
					depth++
				} else if ch == ')' || ch == ']' || ch == '}' {
					if depth == 0 {
						break
					}
					depth--
				} else if depth == 0 && (ch == '+' || ch == ',' || ch == ';' || ch == '\n') {
					break
				} else if ch == '"' || ch == '\'' || ch == '`' {
					q := ch
					j++
					for j < len(js) && js[j] != q {
						if js[j] == '\\' {
							j++
						}
						j++
					}
				}
				j++
			}
			if j == i {
				return "", i, false
			}
			b.WriteString(":param")
			i = j
		}
		for i < len(js) && (js[i] == ' ' || js[i] == '\t' || js[i] == '\n' || js[i] == '\r') {
			i++
		}
		if i < len(js) && js[i] == '+' {
			i++
			continue
		}
		break
	}
	s := b.String()
	if !sawLiteral && s == ":param" {
		return s, i, true // fully dynamic; the caller marks it Dynamic
	}
	return s, i, sawLiteral || strings.Contains(s, ":param")
}

// callArgs returns the text of the call arguments whose "(" is at open,
// after the first argument's comma, or "" when there is one argument.
func callArgs(js string, open int) string {
	if open < 0 || open >= len(js) || js[open] != '(' {
		return ""
	}
	depth := 0
	for j := open; j < len(js); j++ {
		switch js[j] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return js[open+1 : j]
			}
		case '"', '\'', '`':
			q := js[j]
			j++
			for j < len(js) && js[j] != q {
				if js[j] == '\\' {
					j++
				}
				j++
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Whole-page elements
// ---------------------------------------------------------------------------

// PageElements is the interactive elements of a page together with those of
// the layouts and components it pulls in and the scripts it loads.
type PageElements struct {
	Name     string    `json:"name"`
	Elements []Element `json:"elements"`
	// Heading is the page's own <h1>/<title> guess.
	Heading string `json:"heading,omitempty"`
	// Missing are scripts that could not be read.
	Missing []string `json:"missing,omitempty"`
}

// ElementsFor resolves the template name in templates (the templates
// directory) and collects the elements of the page, its layout chain and
// its includes. Scripts named by <script src="/static/..."> are read from
// resources (the tree holding templates/ and static/) when present. An
// element found in any file other than the page itself is Shared.
func ElementsFor(templates, resources fs.FS, name string) (*PageElements, error) {
	res, err := Resolve(templates, name)
	if err != nil {
		return nil, err
	}
	out := &PageElements{Name: strings.TrimSuffix(res.Name, ".html")}
	seenScript := map[string]bool{}
	for _, file := range res.Files {
		b, err := fs.ReadFile(templates, file)
		if err != nil {
			continue
		}
		shared := file != res.Name
		src := string(b)
		els, scripts := parseElements(src)
		for _, e := range els {
			e.Source, e.Shared = file, shared
			out.Elements = append(out.Elements, e)
		}
		if !shared {
			out.Heading = Heading(src)
		}
		for _, s := range scripts {
			p, ok := scriptPath(s)
			if !ok || seenScript[p] {
				continue
			}
			seenScript[p] = true
			if resources == nil {
				out.Missing = append(out.Missing, p)
				continue
			}
			jb, err := fs.ReadFile(resources, p)
			if err != nil {
				out.Missing = append(out.Missing, p)
				continue
			}
			for _, e := range ScriptElements(string(jb), 1) {
				e.Source, e.Shared, e.Via = p, shared, file
				out.Elements = append(out.Elements, e)
			}
		}
	}
	return out, nil
}

// scriptPath maps a <script src> to a path under the resources tree
// ("/static/js/a.js?v=3" -> "static/js/a.js"). External and non-static
// scripts are not read.
func scriptPath(src string) (string, bool) {
	src = strings.TrimSpace(src)
	if scriptSkip.MatchString(src) {
		return "", false
	}
	if i := strings.IndexAny(src, "?#"); i >= 0 {
		src = src[:i]
	}
	src = spl1.ReplaceAllString(src, "")
	p := path.Clean(strings.TrimPrefix(src, "/"))
	if !strings.HasPrefix(p, "static/") || !strings.HasSuffix(p, ".js") {
		return "", false
	}
	return p, true
}

// IsAssetURL reports whether a normalised URL path names a static file
// (/static/..., /favicon.ico, /sw.js) rather than a route.
func IsAssetURL(p string) bool {
	if strings.HasPrefix(p, "/static/") || p == "/favicon.ico" || p == "/robots.txt" || p == "/sw.js" || p == "/manifest.json" || p == "/manifest.webmanifest" {
		return true
	}
	return assetExt[strings.ToLower(path.Ext(p))]
}

// dedupeForms drops repeated identical forms (a template that renders the
// same form for several roles), keeping the first.
func dedupeForms(els []Element) []Element {
	seen := map[string]bool{}
	out := els[:0]
	for _, e := range els {
		if e.Kind == "form" {
			key := e.Method + "|" + e.URL + "|" + e.Redirect + "|" + e.Label + "|" + strings.Join(e.Fields, ",") + "|" + e.RawURL
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, e)
	}
	return out
}
