// Package sandbox runs the upstreams of the demo deployment in-process: an
// SMPP SMSC (smppflow's server) and a vendor HTTP API. With them the whole
// application, SMPP binds, HTTP providers and delivery receipts included, runs
// end to end with nothing external. The tests use them for the same reason.
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/smppflow/pkg/protocol"
	"github.com/oarkflow/smppflow/pkg/server"
)

// SMSC is an SMPP server that accepts submit_sm and answers with a delivery
// receipt.
type SMSC struct {
	srv      *server.Server
	cancel   context.CancelFunc
	SystemID string
	Password string

	seq       atomic.Uint64
	mu        sync.Mutex
	rejects   []string
	submitted []Submitted
	down      atomic.Bool
}

// Submitted is one message the SMSC accepted.
type Submitted struct {
	ID   string
	From string
	To   string
	Text string
}

// SMSCConfig configures the SMSC.
type SMSCConfig struct {
	Addr     string // default 127.0.0.1:0
	SystemID string
	Password string
	// DLRDelay is the delay before the receipt.
	DLRDelay time.Duration
	// RejectPrefixes make submit_sm fail with ESME_RINVDSTADR for matching
	// destinations.
	RejectPrefixes []string
}

// StartSMSC starts the server and waits until it listens.
func StartSMSC(cfg SMSCConfig) (*SMSC, error) {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	if cfg.SystemID == "" {
		cfg.SystemID = "smsgw"
	}
	if cfg.DLRDelay == 0 {
		cfg.DLRDelay = 50 * time.Millisecond
	}
	// Reserve a concrete port so callers can read the address before binding.
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, err
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	s := &SMSC{SystemID: cfg.SystemID, Password: cfg.Password, rejects: cfg.RejectPrefixes}
	s.srv = server.New(server.Config{
		Addr: addr, SystemID: "sandbox-smsc", Version: protocol.Version34,
		AutoDLR: true, DLRDelay: cfg.DLRDelay,
		Auth: func(req protocol.BindRequest) protocol.CommandStatus {
			if req.SystemID != s.SystemID || req.Password != s.Password {
				return protocol.ESME_RINVPASWD
			}
			return protocol.ESME_ROK
		},
		Submit: func(_ context.Context, req protocol.SubmitSMRequest) (string, protocol.CommandStatus) {
			if s.down.Load() {
				return "", protocol.ESME_RSYSERR
			}
			s.mu.Lock()
			rejects := s.rejects
			s.mu.Unlock()
			for _, p := range rejects {
				if strings.HasPrefix(req.Destination.Addr, p) {
					return "", protocol.ESME_RINVDSTADR
				}
			}
			id := fmt.Sprintf("smsc-%06d", s.seq.Add(1))
			s.mu.Lock()
			s.submitted = append(s.submitted, Submitted{ID: id, From: req.Source.Addr, To: req.Destination.Addr, Text: string(req.ShortMessage)})
			s.mu.Unlock()
			return id, protocol.ESME_ROK
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go func() { _ = s.srv.ListenAndServe(ctx) }()
	select {
	case <-s.srv.Ready():
	case <-time.After(5 * time.Second):
		cancel()
		return nil, fmt.Errorf("sandbox: the SMSC did not start")
	}
	return s, nil
}

// Addr is the address the SMSC listens on.
func (s *SMSC) Addr() string { return s.srv.Addr() }

// SetRejectPrefixes replaces the destination prefixes the SMSC refuses with
// ESME_RINVDSTADR.
func (s *SMSC) SetRejectPrefixes(prefixes ...string) {
	s.mu.Lock()
	s.rejects = prefixes
	s.mu.Unlock()
}

// SetDown makes submit_sm fail with a system error until reversed.
func (s *SMSC) SetDown(v bool) { s.down.Store(v) }

// Submitted returns the messages accepted so far.
func (s *SMSC) Submitted() []Submitted {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Submitted(nil), s.submitted...)
}

// Close stops the server.
func (s *SMSC) Close() {
	s.cancel()
	_ = s.srv.Close()
}

// Vendor is a vendor HTTP API: POST /send accepts a message and later posts a
// receipt to the callback URL.
type Vendor struct {
	srv   *http.Server
	ln    net.Listener
	Token string

	Callback string // URL to post receipts to, set after the application starts
	Secret   string // sent as X-Webhook-Secret
	Delay    time.Duration

	seq atomic.Uint64
	mu  sync.Mutex
	got []VendorMessage
	// FailWith, when non-zero, answers every send with this status.
	failWith atomic.Int32
}

// VendorMessage is one accepted request.
type VendorMessage struct {
	ID        string
	To        string
	From      string
	Text      string
	Reference string
}

// StartVendor starts the vendor API on a free port.
func StartVendor(token string) (*Vendor, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	v := &Vendor{ln: ln, Token: token, Delay: 50 * time.Millisecond}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", v.send)
	v.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = v.srv.Serve(ln) }()
	return v, nil
}

// URL is the base URL of the vendor.
func (v *Vendor) URL() string { return "http://" + v.ln.Addr().String() }

// FailWith makes every send answer with status until reset with 0.
func (v *Vendor) FailWith(status int) { v.failWith.Store(int32(status)) }

// Messages returns what the vendor accepted.
func (v *Vendor) Messages() []VendorMessage {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]VendorMessage(nil), v.got...)
}

// Close stops the vendor.
func (v *Vendor) Close() { _ = v.srv.Close() }

func (v *Vendor) send(w http.ResponseWriter, r *http.Request) {
	if v.Token != "" && r.Header.Get("Authorization") != "Bearer "+v.Token {
		http.Error(w, `{"error":{"code":"unauthorized"}}`, http.StatusUnauthorized)
		return
	}
	if st := int(v.failWith.Load()); st != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		_, _ = w.Write([]byte(`{"error":{"code":"scripted_failure"}}`))
		return
	}
	var in struct {
		To, From, Text, Reference string
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, `{"error":{"code":"bad_request"}}`, http.StatusBadRequest)
		return
	}
	id := fmt.Sprintf("vnd-%06d", v.seq.Add(1))
	v.mu.Lock()
	v.got = append(v.got, VendorMessage{ID: id, To: in.To, From: in.From, Text: in.Text, Reference: in.Reference})
	callback, secret, delay := v.Callback, v.Secret, v.Delay
	v.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprintf(w, `{"data":{"message_id":%q,"status":"queued"}}`, id)

	if callback != "" {
		go func() {
			time.Sleep(delay)
			body, _ := json.Marshal(map[string]any{"id": id, "reference": in.Reference, "status": "DELIVRD"})
			req, err := http.NewRequest(http.MethodPost, callback, bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if secret != "" {
				req.Header.Set("X-Webhook-Secret", secret)
			}
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
	}
}

// SetCallback sets where receipts are posted.
func (v *Vendor) SetCallback(url, secret string) {
	v.mu.Lock()
	v.Callback, v.Secret = url, secret
	v.mu.Unlock()
}
