package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"
)

// The orders application end to end: app.bcl compiled and served in-process on
// a fresh PostgreSQL database, with the payment gateway and the SMTP server
// replaced by local fakes. Runs when TEST_POSTGRES_DSN names a server the test
// may create databases on, and skips otherwise.

func TestOrdersEndToEnd(t *testing.T) {
	dsn := freshPostgres(t)
	gateway := newFakeGateway(t)
	mail := newFakeSMTP(t)
	invoices := t.TempDir()

	h := startApp(t, "app.bcl", map[string]string{
		"APP_ENV":                "development",
		"DATABASE_DRIVER":        "pgx",
		"DATABASE_URL":           dsn,
		"JWT_SECRET":             "jwt-secret-0123456789abcdef-0123456789",
		"SESSION_SECRET":         "session-secret-0123456789abcdef-012345",
		"PAYMENT_API_KEY":        "test-key",
		"PAYMENT_WEBHOOK_SECRET": "webhook-secret-0123456789abcdef-01234",
		"PAYMENT_BASE_URL":       gateway.URL,
		"PAYMENT_HOST":           "127.0.0.1",
		"PAYMENT_ALLOW_PRIVATE":  "true",
		"SMTP_HOST":              "127.0.0.1",
		"SMTP_PORT":              mail.port,
		"INVOICE_DIR":            invoices,
	})
	db := openDB(t, dsn)

	// Anonymous callers cannot place orders or decide tasks.
	anon := h.client(t)
	if status, body := h.do(anon, "POST", "/orders", map[string]any{"items": []any{map[string]any{"sku": "A", "quantity": 1}}}); status != http.StatusUnauthorized {
		t.Fatalf("anonymous order: %d %v, want 401", status, body)
	}

	buyer := h.client(t)
	if status, body := h.do(buyer, "POST", "/auth/register", map[string]any{"email": "buyer@example.com", "name": "Buyer", "password": "correct-horse-battery"}); status != http.StatusCreated {
		t.Fatalf("register: %d %v", status, body)
	}
	if _, err := db.Exec(`INSERT INTO products (sku,name,price_cents,stock) VALUES ('A','Apple',250,10),('B','Bread',1000,5),('BIG','Boat',60000,2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO products (sku,name,price_cents,stock,active) VALUES ('OLD','Old',99,1,false)`); err != nil {
		t.Fatal(err)
	}

	if status, body := h.do(anon, "GET", "/catalogue", nil); status != http.StatusOK || len(asList(body)) != 3 {
		t.Fatalf("catalogue: %d %v, want the 3 active products", status, body)
	}

	// A small order is priced from the products table, charged, fulfilled,
	// invoiced and mailed without anyone approving it.
	status, body := h.do(buyer, "POST", "/orders", map[string]any{"items": []any{
		map[string]any{"sku": "A", "quantity": 2},
		map[string]any{"sku": "B", "quantity": 3},
		// A client-supplied price is ignored.
		map[string]any{"sku": "A", "quantity": 1, "price_cents": 1},
	}})
	if status != http.StatusCreated {
		t.Fatalf("place order: %d %v", status, body)
	}
	small := orderOf(t, body)
	if got := num(small["total_cents"]); got != 3750 {
		t.Fatalf("order total = %v, want 3750 (3x250 + 3x1000)", small["total_cents"])
	}
	smallID := small["id"].(string)
	waitStatus(t, h, buyer, smallID, "fulfilled")
	if got := gateway.charged(smallID); got != "3750" {
		t.Fatalf("gateway charged %q for %s, want 3750", got, smallID)
	}
	waitFor(t, 20*time.Second, "the invoice to be stored", func() bool {
		raw, err := os.ReadFile(filepath.Join(invoices, "invoices", smallID+".txt"))
		return err == nil && strings.Contains(string(raw), "Total: 3750 GBP")
	})
	waitFor(t, 20*time.Second, "the notification mail", func() bool { return mail.sentTo("buyer@example.com", smallID) })

	// An unknown or inactive SKU is refused and records nothing.
	before := countOrders(t, db)
	for _, sku := range []string{"NOPE", "OLD"} {
		status, body := h.do(buyer, "POST", "/orders", map[string]any{"items": []any{
			map[string]any{"sku": "A", "quantity": 1}, map[string]any{"sku": sku, "quantity": 1},
		}})
		if status < 400 || status >= 500 {
			t.Fatalf("order with SKU %s: %d %v, want a 4xx refusal", sku, status, body)
		}
	}
	if after := countOrders(t, db); after != before {
		t.Fatalf("refused orders were recorded: %d orders, want %d", after, before)
	}

	// An order over 50000 parks at the approval task until an approver decides.
	status, body = h.do(buyer, "POST", "/orders", map[string]any{"items": []any{map[string]any{"sku": "BIG", "quantity": 1}}, "currency": "EUR"})
	if status != http.StatusCreated {
		t.Fatalf("place large order: %d %v", status, body)
	}
	big := orderOf(t, body)
	bigID := big["id"].(string)
	if got := num(big["total_cents"]); got != 60000 {
		t.Fatalf("large order total = %v, want 60000", big["total_cents"])
	}

	approver := h.client(t)
	if status, body := h.do(approver, "POST", "/auth/register", map[string]any{"email": "approver@example.com", "name": "Approver", "password": "correct-horse-battery"}); status != http.StatusCreated {
		t.Fatalf("register approver: %d %v", status, body)
	}
	if _, err := db.Exec(`UPDATE users SET roles='approver' WHERE id='approver@example.com'`); err != nil {
		t.Fatal(err)
	}
	if status, body := h.do(approver, "POST", "/auth/login", map[string]any{"email": "approver@example.com", "password": "correct-horse-battery"}); status != http.StatusOK {
		t.Fatalf("approver login: %d %v", status, body)
	}

	var taskID string
	waitFor(t, 20*time.Second, "the approval task", func() bool {
		_, body := h.do(approver, "GET", "/tasks", nil)
		for _, task := range asList(body) {
			m, _ := task.(map[string]any)
			if strings.Contains(fmt.Sprint(m["title"]), bigID) {
				taskID = fmt.Sprint(m["task_id"])
				return true
			}
		}
		return false
	})
	if gateway.charged(bigID) != "" {
		t.Fatal("the large order was charged before anyone approved it")
	}
	if status := orderStatus(t, h, buyer, bigID); status != "placed" {
		t.Fatalf("large order status before approval = %q, want placed", status)
	}

	// The customer cannot approve their own order.
	if status, body := h.do(buyer, "POST", "/tasks/"+taskID+"/decide", map[string]any{"action": "approve"}); status != http.StatusForbidden {
		t.Fatalf("customer deciding a task: %d %v, want 403", status, body)
	}
	if status, body := h.do(approver, "POST", "/tasks/"+taskID+"/decide", map[string]any{"action": "approve", "note": "ok"}); status != http.StatusOK {
		t.Fatalf("approver decision: %d %v", status, body)
	}
	waitStatus(t, h, buyer, bigID, "fulfilled")
	if got := gateway.charged(bigID); got != "60000" {
		t.Fatalf("gateway charged %q for the approved order, want 60000", got)
	}
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type harness struct {
	base string
}

func startApp(t *testing.T, bclPath string, env map[string]string) *harness {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	src, err := os.ReadFile(bclPath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := platform.Compile(context.Background(), src, filepath.Dir(bclPath), platform.DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile %s: %v", bclPath, err)
	}
	t.Cleanup(func() { _ = p.Close() })
	app := fh.NewFast()
	if err := p.Mount(app); err != nil {
		t.Fatalf("mount: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = app.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = app.ShutdownWithTimeout(2 * time.Second)
		_ = listener.Close()
		<-served
	})
	return &harness{base: "http://" + listener.Addr().String()}
}

// client is one browser: its own cookie jar, so its own session.
func (h *harness) client(t *testing.T) *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Timeout: 15 * time.Second, Jar: jar}
}

func (h *harness) do(c *http.Client, method, path string, body any) (int, any) {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, h.base+path, reader)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		decoded = string(raw)
	}
	return resp.StatusCode, decoded
}

// unwrap strips the {"data": ...} envelope when there is one.
func unwrap(v any) any {
	if m, ok := v.(map[string]any); ok {
		if d, ok := m["data"]; ok {
			return d
		}
	}
	return v
}

func asList(v any) []any {
	list, _ := unwrap(v).([]any)
	return list
}

func orderOf(t *testing.T, body any) map[string]any {
	t.Helper()
	m, _ := unwrap(body).(map[string]any)
	order, _ := m["order"].(map[string]any)
	if order == nil || order["id"] == nil {
		t.Fatalf("no order in %v", body)
	}
	return order
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case string:
		var f float64
		_, _ = fmt.Sscan(n, &f)
		return f
	}
	return -1
}

func orderStatus(t *testing.T, h *harness, c *http.Client, id string) string {
	t.Helper()
	_, body := h.do(c, "GET", "/orders/"+id, nil)
	m, _ := unwrap(body).(map[string]any)
	return fmt.Sprint(m["status"])
}

func waitStatus(t *testing.T, h *harness, c *http.Client, id, want string) {
	t.Helper()
	waitFor(t, 30*time.Second, "order "+id+" to reach "+want, func() bool { return orderStatus(t, h, c, id) == want })
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func countOrders(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// freshPostgres creates an empty database on TEST_POSTGRES_DSN's server and
// drops it when the test ends; it skips the test when the variable is unset.
func freshPostgres(t *testing.T) string {
	t.Helper()
	admin := os.Getenv("TEST_POSTGRES_DSN")
	if admin == "" {
		t.Skip("TEST_POSTGRES_DSN is not set; skipping the PostgreSQL end-to-end test")
	}
	db := openDB(t, admin)
	name := fmt.Sprintf("reftest_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)") })
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func openDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakeGateway struct {
	*httptest.Server
	mu      sync.Mutex
	charges map[string]string // order id -> amount
}

func newFakeGateway(t *testing.T) *fakeGateway {
	g := &fakeGateway{charges: map[string]string{}}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		ref := fmt.Sprint(body["reference"])
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/charges" {
			g.mu.Lock()
			g.charges[ref] = fmt.Sprint(body["amount"])
			g.mu.Unlock()
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"reference": "ch_" + ref, "status": "succeeded"})
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeGateway) charged(orderID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.charges[orderID]
}

// fakeSMTP accepts every message and keeps it.
type fakeSMTP struct {
	port     string
	mu       sync.Mutex
	messages []string // "rcpt\ndata"
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &fakeSMTP{port: fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	reply := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	reply("220 fake ESMTP")
	var rcpt, data strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			reply("250-fake")
			reply("250 8BITMIME")
		case strings.HasPrefix(cmd, "HELO"), strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RSET"), strings.HasPrefix(cmd, "NOOP"):
			reply("250 OK")
		case strings.HasPrefix(cmd, "RCPT"):
			rcpt.WriteString(strings.TrimSpace(line) + " ")
			reply("250 OK")
		case cmd == "DATA":
			reply("354 go ahead")
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(l, "\r\n") == "." {
					break
				}
				data.WriteString(l)
			}
			s.mu.Lock()
			s.messages = append(s.messages, rcpt.String()+"\n"+data.String())
			s.mu.Unlock()
			rcpt.Reset()
			data.Reset()
			reply("250 queued")
		case cmd == "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 not implemented")
		}
	}
}

func (s *fakeSMTP) sentTo(rcpt, contains string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.messages {
		if strings.Contains(strings.ToLower(m), strings.ToLower(rcpt)) && strings.Contains(m, contains) {
			return true
		}
	}
	return false
}
