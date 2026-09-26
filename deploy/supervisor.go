package deploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"
)

// Supervisor serves an application's active revision on one listener and
// swaps to a newly activated revision without refusing a connection: new
// connections go to the new generation while the old one finishes its
// in-flight requests and shuts down. A revision that fails to build is marked
// failed and the running generation keeps serving.
type Supervisor struct {
	Manager *Manager
	// Build compiles a revision's source into a generation.
	Build func(ctx context.Context, src []byte) (*platform.Platform, error)
	// Poll is how often the store is checked for a newly active revision
	// (default 2s). Notify triggers an immediate check.
	Poll time.Duration
	// Drain bounds how long an old generation may take to finish in-flight
	// requests (default 30s).
	Drain time.Duration
	Logf  func(format string, args ...any)

	mu      sync.Mutex
	current *generation
	notify  chan struct{}
}

type generation struct {
	rev *Revision
	p   *platform.Platform
	app *fh.App
	ln  *chanListener
	// done is closed when the app's Serve returns.
	done chan struct{}
}

func (s *Supervisor) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// Notify asks the supervisor to check for a new active revision now.
func (s *Supervisor) Notify() {
	s.mu.Lock()
	ch := s.notify
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Current returns the revision being served (nil before the first build).
func (s *Supervisor) Current() *Revision {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil
	}
	return s.current.rev
}

// Serve runs until ctx ends. It needs an active revision to start.
func (s *Supervisor) Serve(ctx context.Context, ln net.Listener) error {
	s.mu.Lock()
	s.notify = make(chan struct{}, 1)
	s.mu.Unlock()
	active, err := s.Manager.Active(ctx)
	if err != nil {
		return err
	}
	if active == nil {
		return fmt.Errorf("deploy: %s has no active revision to serve", s.Manager.App)
	}
	gen, err := s.start(ctx, active, ln.Addr())
	if err != nil {
		return fmt.Errorf("deploy: revision %d does not build: %w", active.Seq, err)
	}
	s.mu.Lock()
	s.current = gen
	s.mu.Unlock()
	s.logf("serving %s revision %d", s.Manager.App, active.Seq)

	acceptErr := make(chan error, 1)
	go func() { acceptErr <- s.acceptLoop(ln) }()

	poll := s.Poll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = ln.Close()
			s.mu.Lock()
			last := s.current
			s.mu.Unlock()
			s.stop(last)
			<-acceptErr
			return nil
		case err := <-acceptErr:
			return err
		case <-ticker.C:
		case <-s.notify:
		}
		s.reconcile(ctx, ln.Addr())
	}
}

// reconcile swaps to the store's active revision when it changed.
func (s *Supervisor) reconcile(ctx context.Context, addr net.Addr) {
	active, err := s.Manager.Active(ctx)
	if err != nil || active == nil {
		return
	}
	s.mu.Lock()
	cur := s.current
	s.mu.Unlock()
	if cur != nil && cur.rev.ID == active.ID {
		return
	}
	next, err := s.start(ctx, active, addr)
	if err != nil {
		s.logf("revision %d failed to build, keeping revision %d: %v", active.Seq, cur.rev.Seq, err)
		_ = s.Manager.MarkFailed(ctx, active.ID, cur.rev.ID, err.Error())
		return
	}
	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
	s.logf("swapped %s from revision %d to %d", s.Manager.App, cur.rev.Seq, active.Seq)
	go s.stop(cur)
}

func (s *Supervisor) start(ctx context.Context, rev *Revision, addr net.Addr) (*generation, error) {
	if err := s.Manager.Verify(rev); err != nil {
		return nil, err
	}
	p, err := s.Build(ctx, []byte(rev.Source))
	if err != nil {
		return nil, err
	}
	app := fh.NewFast(fh.WithStartupBannerDisabled(true))
	if err := p.Mount(app); err != nil {
		_ = p.Close()
		return nil, err
	}
	gen := &generation{rev: rev, p: p, app: app, ln: newChanListener(addr), done: make(chan struct{})}
	go func() {
		defer close(gen.done)
		_ = app.Serve(gen.ln)
	}()
	return gen, nil
}

// stop drains a generation: no new connections reach it, in-flight
// requests finish (bounded by Drain), then its resources close.
func (s *Supervisor) stop(g *generation) {
	if g == nil {
		return
	}
	drain := s.Drain
	if drain <= 0 {
		drain = 30 * time.Second
	}
	_ = g.app.ShutdownWithTimeout(drain)
	_ = g.ln.Close()
	<-g.done
	_ = g.p.Close()
}

// acceptLoop hands each accepted connection to the current generation.
func (s *Supervisor) acceptLoop(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.mu.Lock()
		g := s.current
		s.mu.Unlock()
		if g == nil || !g.ln.deliver(conn) {
			_ = conn.Close()
		}
	}
}

// chanListener is a net.Listener fed by the supervisor's accept loop.
type chanListener struct {
	addr   net.Addr
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newChanListener(addr net.Addr) *chanListener {
	return &chanListener{addr: addr, conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *chanListener) deliver(c net.Conn) bool {
	select {
	case l.conns <- c:
		return true
	case <-l.closed:
		return false
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }
