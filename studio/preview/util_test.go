package preview

import (
	"net"
	"testing"
)

func hostname(hostport string) string { h, _, _ := net.SplitHostPort(hostport); return h }
func port(hostport string) string     { _, p, _ := net.SplitHostPort(hostport); return p }

// trap is a TCP listener that counts connections: an outbound call that
// escaped the sandbox would show up as a connection here.
type trap struct {
	ln    net.Listener
	conns chan struct{}
}

func newTrap(t *testing.T) *trap {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tr := &trap{ln: ln, conns: make(chan struct{}, 1024)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			tr.conns <- struct{}{}
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return tr
}

func (t *trap) addr() string { return t.ln.Addr().String() }
func (t *trap) count() int   { return len(t.conns) }
