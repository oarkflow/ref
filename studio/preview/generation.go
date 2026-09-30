package preview

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"
)

// generation is one compiled, served build of a draft.
type generation struct {
	id      string
	version int64
	root    string
	p       *platform.Platform
	app     *fh.App
	ln      *memListener
	done    chan struct{}
	rec     *recorder
	routes  []routeMatcher
	changes []Change

	proxy     *httputil.ReverseProxy
	transport *http.Transport
	lastUse   atomic.Int64 // unix nanos
}

func (g *generation) touch() { g.lastUse.Store(time.Now().UnixNano()) }

// close drains the generation: it stops accepting, lets in-flight requests
// finish (bounded by drain), then closes the platform and deletes its temp dir.
func (g *generation) close(grace, drain time.Duration) {
	if grace > 0 {
		time.Sleep(grace)
	}
	if drain <= 0 {
		drain = 5 * time.Second
	}
	_ = g.app.ShutdownWithTimeout(drain)
	_ = g.ln.Close()
	<-g.done
	g.transport.CloseIdleConnections()
	_ = g.p.Close()
	_ = os.RemoveAll(g.root)
}

// memListener is a net.Listener fed by in-process pipes, so a preview needs no
// port. dial returns the client end of a new connection.
type memListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newMemListener() *memListener {
	return &memListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *memListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (*memListener) Addr() net.Addr { return memAddr{} }

func (l *memListener) dial(ctx context.Context) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.closed:
		_ = client.Close()
		_ = server.Close()
		return nil, errors.New("preview generation is closed")
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		return nil, ctx.Err()
	}
}

type memAddr struct{}

func (memAddr) Network() string { return "mem" }
func (memAddr) String() string  { return "preview" }

// routeMatcher derives "which route block served this" from method and path,
// because the fh handler chain does not expose the matched route.
type routeMatcher struct {
	method   string
	segments []string
	name     string
	intent   string
	static   int
}

func buildMatchers(doc platform.Document) []routeMatcher {
	out := make([]routeMatcher, 0, len(doc.Routes))
	for _, r := range doc.Routes {
		m := routeMatcher{method: strings.ToUpper(r.Method), name: r.Name, intent: r.Intent}
		if m.intent == "" {
			m.intent = r.Process
		}
		for _, seg := range strings.Split(strings.Trim(r.Path, "/"), "/") {
			m.segments = append(m.segments, seg)
			if seg != "" && seg[0] != ':' && seg[0] != '*' {
				m.static++
			}
		}
		out = append(out, m)
	}
	return out
}

func matchRoute(ms []routeMatcher, method, path string) (name, intent string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	best := -1
	for _, m := range ms {
		if m.method != "" && m.method != strings.ToUpper(method) && m.method != "ANY" && m.method != "ALL" {
			continue
		}
		if !segmentsMatch(m.segments, parts) {
			continue
		}
		if m.static > best {
			best, name, intent = m.static, m.name, m.intent
		}
	}
	return name, intent
}

func segmentsMatch(pattern, parts []string) bool {
	for i, seg := range pattern {
		if seg != "" && seg[0] == '*' {
			return true
		}
		if i >= len(parts) {
			return false
		}
		if seg != "" && seg[0] == ':' {
			continue
		}
		if seg != parts[i] {
			return false
		}
	}
	return len(pattern) == len(parts)
}
