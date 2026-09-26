package loadtest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// The soak drives an app with a durably hooked, versioned, search-indexed
// entity and a pipeline with an outbox hook, triage and a notify rule, on
// PostgreSQL, through two replicas sharing one database. Both hooks fail
// soakFaultPct of the time to exercise retry and dead-letter under load.
//
//	TEST_POSTGRES_DSN=postgres://... REF_SOAK=1 go test ./loadtest -run TestSoak -v
//
// REF_SOAK_DURATION (default 30s) and REF_SOAK_WORKERS (default 32) size it.

const soakFaultPct = 10

const soakApp = `
name "soak"

resource "db" {
  kind "database.sql"
  config {
    driver "pgx"
    dsn env("SOAK_DSN")
    migrations [
      "CREATE TABLE IF NOT EXISTS hook_log (event_id TEXT NOT NULL, record_id TEXT NOT NULL, event TEXT NOT NULL)",
      "CREATE TABLE IF NOT EXISTS case_log (event_id TEXT NOT NULL, case_id TEXT NOT NULL, event TEXT NOT NULL)",
      "CREATE TABLE IF NOT EXISTS mail (user_id TEXT NOT NULL, subject TEXT NOT NULL)"
    ]
  }
}

resource "jwt" {
  kind "auth.jwt"
  config {
    secret env.required("SOAK_JWT_SECRET")
  }
}

resource "cases" {
  kind "pipeline.cases"
  config {
    database "db"
    event_max_attempts 6
    event_retry_base "20ms"
    notify_channels {
      email "send_mail"
    }
  }
}

entity "item" {
  database "db"
  auth "jwt"
  versioned true
  search ["title"]
  search_index true
  bulk true
  column "code" {
    required true
    unique true
  }
  column "title" {
    required true
  }
  column "n" {
    kind integer
  }
  on "created" {
    hook "sync"
    durable true
    max_attempts 6
    retry_base "20ms"
  }
  on "updated" {
    hook "sync"
    durable true
    max_attempts 6
    retry_base "20ms"
  }
  on "deleted" {
    hook "sync"
    durable true
    max_attempts 6
    retry_base "20ms"
  }
}

pipeline "ticket" {
  worker "sc1" {
    roles ["screener"]
  }
  on "*" {
    hook "deliver"
  }
  form "t" {
    input "title" {
      kind text
      required true
    }
    input "amount" {
      kind number
      required true
    }
  }
  stage "apply" {
    public true
    page {
      group "g" {
        forms ["t"]
      }
    }
  }
  stage "screen" {
    roles ["screener"]
    review "triage" {
      bucket "urgent" {
        condition "t.amount > 500"
        priority 1
        queue "urgent"
      }
      bucket "normal" {
        priority 2
        queue "standard"
      }
    }
    action "ok" {
      outcome advance
    }
  }
  stage "done" {
    roles ["screener"]
    action "close" {
      outcome approve
    }
  }
  notify "stage.entered" {
    stage "screen"
    to ["role:screener"]
    channels ["email"]
    subject "New work: {case.number}"
  }
}

# Every hook run is logged (duplicates included); soakFaultPct of runs fail.
intent "sync" {
  response "n"
  node "n" {
    uses "database.exec"
    resource "db"
    kind effect
    requires [input]
    provides [n]
    config {
      statement "INSERT INTO hook_log (event_id, record_id, event) SELECT $1, $2, $3 WHERE random() >= 0.10"
      args ["input.event_id", "input.record.id", "input.event"]
      require_affected true
    }
  }
}

intent "deliver" {
  response "n"
  node "n" {
    uses "database.exec"
    resource "db"
    kind effect
    requires [input]
    provides [n]
    config {
      statement "INSERT INTO case_log (event_id, case_id, event) SELECT $1, $2, $3 WHERE random() >= 0.10"
      args ["input.event.id", "input.case.id", "input.event.name"]
      require_affected true
    }
  }
}

intent "send_mail" {
  response "n"
  node "n" {
    uses "database.exec"
    resource "db"
    kind effect
    requires [input]
    provides [n]
    config {
      statement "INSERT INTO mail (user_id, subject) VALUES ($1, $2)"
      args ["input.user", "input.subject"]
    }
  }
}

intent "start" {
  response "v"
  node "v" {
    uses "pipeline.start"
    resource "cases"
    kind effect
    requires [input]
    provides [v]
  }
}
intent "view" {
  response "v"
  node "v" {
    uses "pipeline.view"
    resource "cases"
    provides [v]
  }
}
intent "act" {
  response "v"
  node "v" {
    uses "pipeline.act"
    resource "cases"
    kind effect
    requires [input]
    provides [v]
  }
}

route "start" { method POST path "/tickets" intent "start" auth "jwt" status 201 }
route "view" { method GET path "/tickets/:id" intent "view" auth "jwt" }
route "act" { method POST path "/tickets/:id/stages/:stage/actions/:action" intent "act" auth "jwt" }
`

const soakSecret = "soak-test-jwt-secret-0123456789abcdef-xyz"

func TestSoak(t *testing.T) {
	admin := os.Getenv("TEST_POSTGRES_DSN")
	if admin == "" || os.Getenv("REF_SOAK") == "" {
		t.Skip("set TEST_POSTGRES_DSN and REF_SOAK=1 to run the soak")
	}
	duration := 30 * time.Second
	if v := os.Getenv("REF_SOAK_DURATION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatal(err)
		}
		duration = d
	}
	workers := 32
	if v := os.Getenv("REF_SOAK_WORKERS"); v != "" {
		workers, _ = strconv.Atoi(v)
	}

	baseline := runtime.NumGoroutine()
	dsn, dbName, dropDB := soakDatabase(t, admin)
	defer dropDB()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(soakApp), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOAK_DSN", dsn)
	t.Setenv("SOAK_JWT_SECRET", soakSecret)
	replicas := []*soakReplica{startReplica(t, path), startReplica(t, path)}
	transport := &http.Transport{MaxIdleConnsPerHost: workers}
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}

	s := &soak{t: t, client: client, replicas: replicas, stats: map[string]*opStats{},
		user: mintJWT("u1", nil), screener: mintJWT("sc1", []string{"screener"})}

	var heap []uint64
	sample := func() {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		heap = append(heap, m.HeapAlloc)
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(i), uint64(time.Now().UnixNano())))
			for ctx.Err() == nil {
				s.step(rng)
			}
		}()
	}
	start := time.Now()
	sampler := time.NewTicker(duration / 10)
	for done := false; !done; {
		select {
		case <-ctx.Done():
			done = true
		case <-sampler.C:
			sample()
		}
	}
	sampler.Stop()
	wg.Wait()
	cancel()
	elapsed := time.Since(start)
	s.report(elapsed)

	// Invariants. The dispatchers of both replicas drain the outboxes.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	drainStart := time.Now()
	for time.Since(drainStart) < 90*time.Second {
		if count("SELECT count(*) FROM ref_entity_events WHERE dead = 0")+count("SELECT count(*) FROM pipeline_outbox WHERE dead = 0")+
			count("SELECT count(*) FROM pipeline_notifications WHERE dead = 0") == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("outbox drained in %v", time.Since(drainStart).Round(time.Millisecond))
	pendingEntity, pendingCase := count("SELECT count(*) FROM ref_entity_events WHERE dead = 0"), count("SELECT count(*) FROM pipeline_outbox WHERE dead = 0")
	deadEntity, deadCase := count("SELECT count(*) FROM ref_entity_events WHERE dead = 1"), count("SELECT count(*) FROM pipeline_outbox WHERE dead = 1")
	if pendingEntity+pendingCase != 0 {
		t.Errorf("outbox not drained: %d entity, %d pipeline events pending", pendingEntity, pendingCase)
	}

	runs, distinct := count("SELECT count(*) FROM hook_log"), count("SELECT count(DISTINCT event_id) FROM hook_log")
	committed := int(s.committed.Load())
	t.Logf("entity hooks: committed changes %d, delivered %d (+%d duplicate runs), dead-lettered %d", committed, distinct, runs-distinct, deadEntity)
	if distinct+deadEntity != committed {
		t.Errorf("entity hooks: %d delivered + %d dead != %d committed changes", distinct, deadEntity, committed)
	}
	cruns, cdistinct := count("SELECT count(*) FROM case_log"), count("SELECT count(DISTINCT event_id) FROM case_log")
	started := int(s.started.Load())
	startedDelivered := count("SELECT count(DISTINCT case_id) FROM case_log WHERE event = 'case.started'")
	t.Logf("pipeline hooks: %d events delivered (+%d duplicate runs), dead-lettered %d; %d cases started, %d with case.started delivered",
		cdistinct, cruns-cdistinct, deadCase, started, startedDelivered)
	if startedDelivered+deadCase < started {
		t.Errorf("pipeline: %d cases started, only %d case.started delivered (+%d dead)", started, startedDelivered, deadCase)
	}
	mailed, mailedCases := count("SELECT count(*) FROM mail"), count("SELECT count(DISTINCT subject) FROM mail")
	t.Logf("notifications: %d mailed for %d cases", mailed, mailedCases)
	if mailed != mailedCases {
		t.Errorf("notifications: %d mails for %d cases entering screen, want one each", mailed, mailedCases)
	}

	// The search index holds exactly the live rows' words.
	if n := count("SELECT count(*) FROM item WHERE id NOT IN (SELECT record_id FROM item_search)"); n != 0 {
		t.Errorf("search index: %d rows without tokens", n)
	}
	if n := count("SELECT count(*) FROM item_search WHERE record_id NOT IN (SELECT id FROM item)"); n != 0 {
		t.Errorf("search index: %d tokens of deleted rows", n)
	}
	if n := count("SELECT count(*) FROM item i WHERE NOT EXISTS (SELECT 1 FROM item_search s WHERE s.record_id = i.id AND s.token = split_part(i.title, ' ', 2))"); n != 0 {
		t.Errorf("search index: %d rows whose current title is not indexed", n)
	}
	db.Close()

	s.mu.Lock()
	for op, st := range s.stats {
		for code, n := range st.codes {
			if code >= 500 {
				t.Errorf("%s: %d responses with status %d", op, n, code)
			}
		}
	}
	// A stale version is a 409, or a 404 when the row was deleted meanwhile.
	if st := s.stats["entity.update.stale"]; st == nil || st.codes[409] == 0 || st.codes[409]+st.codes[404] != len(st.lat) {
		t.Errorf("stale updates must be 409: %v", st.codes)
	}
	s.mu.Unlock()

	if len(heap) >= 4 {
		t.Logf("heap after GC (MB): %v", mb(heap))
		first, last := heap[1], heap[len(heap)-1]
		if last > 3*first && sort.SliceIsSorted(heap[1:], func(i, j int) bool { return heap[1+i] < heap[1+j] }) {
			t.Errorf("heap grew monotonically from %d to %d bytes", first, last)
		}
	}

	// Shutdown releases goroutines and connections.
	for _, r := range replicas {
		r.close()
	}
	transport.CloseIdleConnections()
	var g int
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if g = runtime.NumGoroutine(); g <= baseline+5 {
			break
		}
	}
	t.Logf("goroutines: baseline %d, after shutdown %d", baseline, g)
	if g > baseline+5 {
		buf := make([]byte, 1<<20)
		t.Errorf("goroutines did not return to baseline (%d > %d):\n%s", g, baseline, buf[:runtime.Stack(buf, true)])
	}
	if n := soakConnections(t, admin, dbName); n != 0 {
		t.Errorf("%d connections to the test database remain after shutdown", n)
	}
}

func mb(v []uint64) []string {
	out := make([]string, len(v))
	for i, b := range v {
		out[i] = fmt.Sprintf("%.1f", float64(b)/(1<<20))
	}
	return out
}

// soakDatabase creates an empty database; drop removes it.
func soakDatabase(t *testing.T, adminDSN string) (dsn, name string, drop func()) {
	t.Helper()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	name = fmt.Sprintf("refsoak_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String(), name, func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
		admin.Close()
	}
}

func soakConnections(t *testing.T, adminDSN, name string) int {
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var n int
	if err := admin.QueryRow("SELECT count(*) FROM pg_stat_activity WHERE datname = $1", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type soakReplica struct {
	p      *platform.Platform
	app    *fh.App
	ln     net.Listener
	served chan struct{}
	base   string
	once   sync.Once
}

func startReplica(t *testing.T, path string) *soakReplica {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := platform.Compile(context.Background(), src, filepath.Dir(path), platform.DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	app := fh.NewFast()
	if err := p.Mount(app); err != nil {
		t.Fatalf("mount: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &soakReplica{p: p, app: app, ln: ln, served: make(chan struct{}), base: "http://" + ln.Addr().String()}
	go func() {
		defer close(r.served)
		_ = app.Serve(ln)
	}()
	t.Cleanup(r.close)
	return r
}

func (r *soakReplica) close() {
	r.once.Do(func() {
		_ = r.app.ShutdownWithTimeout(5 * time.Second)
		_ = r.ln.Close()
		<-r.served
		_ = r.p.Close()
	})
}

func mintJWT(sub string, roles []string) string {
	enc := base64.RawURLEncoding
	now := time.Now().Unix()
	claims := map[string]any{"sub": sub, "iat": now, "nbf": now, "exp": now + 3600}
	if roles != nil {
		claims["roles"] = roles
	}
	head, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	body, _ := json.Marshal(claims)
	msg := enc.EncodeToString(head) + "." + enc.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(soakSecret))
	mac.Write([]byte(msg))
	return msg + "." + enc.EncodeToString(mac.Sum(nil))
}

type opStats struct {
	lat   []time.Duration
	codes map[int]int
}

type soakItem struct {
	id      string
	version float64
}

type soak struct {
	t              *testing.T
	client         *http.Client
	replicas       []*soakReplica
	user, screener string
	seq            atomic.Int64
	committed      atomic.Int64 // entity changes that returned 2xx
	started        atomic.Int64

	mu    sync.Mutex
	stats map[string]*opStats
	items []soakItem
	cases []string // cases in screen, acted on concurrently
}

func (s *soak) call(op, method, path, token string, body any) (int, any) {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	r := s.replicas[rand.IntN(len(s.replicas))]
	req, _ := http.NewRequest(method, r.base+path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	began := time.Now()
	status, decoded := 0, any(nil)
	resp, err := s.client.Do(req)
	if err == nil {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		status = resp.StatusCode
		_ = json.Unmarshal(raw, &decoded)
		if status >= 500 {
			s.t.Logf("%s %s %s: %d %s", op, method, path, status, raw)
		}
	} else {
		s.t.Logf("%s %s %s: %v", op, method, path, err)
	}
	took := time.Since(began)
	s.mu.Lock()
	st := s.stats[op]
	if st == nil {
		st = &opStats{codes: map[int]int{}}
		s.stats[op] = st
	}
	st.lat = append(st.lat, took)
	st.codes[status]++
	s.mu.Unlock()
	return status, decoded
}

func field(v any, path ...string) any {
	for _, p := range path {
		m, _ := v.(map[string]any)
		v = m[p]
	}
	return v
}

var soakWords = []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}

func (s *soak) pickItem(rng *rand.Rand) (soakItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return soakItem{}, false
	}
	return s.items[rng.IntN(len(s.items))], true
}

func (s *soak) setItem(it soakItem, remove bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].id == it.id {
			if remove {
				s.items = slices.Delete(s.items, i, i+1)
			} else if it.version > s.items[i].version {
				s.items[i] = it
			}
			return
		}
	}
	if !remove {
		s.items = append(s.items, it)
	}
}

func (s *soak) step(rng *rand.Rand) {
	title := func() string {
		return fmt.Sprintf("item %s %d", soakWords[rng.IntN(len(soakWords))], rng.IntN(1000))
	}
	switch k := rng.IntN(100); {
	case k < 18: // create
		code := fmt.Sprintf("C-%d", s.seq.Add(1))
		status, body := s.call("entity.create", "POST", "/api/items", s.user, map[string]any{"code": code, "title": title(), "n": rng.IntN(100)})
		if status == 201 {
			s.committed.Add(1)
			v, _ := field(body, "version").(float64)
			s.setItem(soakItem{id: fmt.Sprint(field(body, "id")), version: v}, false)
		}
	case k < 33: // update at the version last seen; concurrent writers conflict
		it, ok := s.pickItem(rng)
		if !ok {
			return
		}
		status, body := s.call("entity.update", "PATCH", "/api/items/"+it.id, s.user, map[string]any{"title": title(), "version": it.version})
		if status == 200 {
			s.committed.Add(1)
			v, _ := field(body, "version").(float64)
			s.setItem(soakItem{id: it.id, version: v}, false)
		}
	case k < 36: // a deliberately stale version must be a 409
		it, ok := s.pickItem(rng)
		if !ok {
			return
		}
		if status, _ := s.call("entity.update.stale", "PATCH", "/api/items/"+it.id, s.user, map[string]any{"title": title(), "version": it.version + 1000}); status == 200 {
			s.committed.Add(1)
		}
	case k < 40: // delete
		it, ok := s.pickItem(rng)
		if !ok {
			return
		}
		status, _ := s.call("entity.delete", "DELETE", "/api/items/"+it.id, s.user, nil)
		if status == 200 {
			s.committed.Add(1)
		}
		if status == 200 || status == 404 {
			s.setItem(it, true)
		}
	case k < 48:
		s.call("entity.list", "GET", "/api/items?limit=20", s.user, nil)
	case k < 56:
		s.call("entity.search", "GET", "/api/items?q="+soakWords[rng.IntN(len(soakWords))], s.user, nil)
	case k < 60:
		if it, ok := s.pickItem(rng); ok {
			s.call("entity.get", "GET", "/api/items/"+it.id, s.user, nil)
		}
	case k < 63: // bulk: five creates, all or nothing
		var creates []any
		for range 5 {
			creates = append(creates, map[string]any{"code": fmt.Sprintf("B-%d", s.seq.Add(1)), "title": title()})
		}
		if status, _ := s.call("entity.bulk", "POST", "/api/items/-/bulk", s.user, map[string]any{"create": creates}); status == 200 {
			s.committed.Add(5)
		}
	case k < 75: // pipeline start + submit
		amount := rng.IntN(1000)
		status, body := s.call("pipeline.start", "POST", "/tickets", s.user, map[string]any{"data": map[string]any{"t": map[string]any{"title": title(), "amount": amount}}})
		if status != 201 {
			return
		}
		s.started.Add(1)
		id := fmt.Sprint(field(body, "case", "id"))
		if status, _ := s.call("pipeline.submit", "POST", "/tickets/"+id+"/stages/apply/actions/submit", s.user, map[string]any{}); status == 200 {
			s.mu.Lock()
			s.cases = append(s.cases, id)
			s.mu.Unlock()
		}
	case k < 90: // advance a screened case; other workers race on the same case
		s.mu.Lock()
		if len(s.cases) == 0 {
			s.mu.Unlock()
			return
		}
		id := s.cases[len(s.cases)-1-rng.IntN(min(len(s.cases), 4))]
		s.mu.Unlock()
		status, _ := s.call("pipeline.act", "POST", "/tickets/"+id+"/stages/screen/actions/ok", s.screener, map[string]any{})
		if status == 200 {
			s.call("pipeline.close", "POST", "/tickets/"+id+"/stages/done/actions/close", s.screener, map[string]any{})
			s.mu.Lock()
			if i := slices.Index(s.cases, id); i >= 0 {
				s.cases = slices.Delete(s.cases, i, i+1)
			}
			s.mu.Unlock()
		}
	default:
		s.mu.Lock()
		var id string
		if len(s.cases) > 0 {
			id = s.cases[rng.IntN(len(s.cases))]
		}
		s.mu.Unlock()
		if id != "" {
			s.call("pipeline.view", "GET", "/tickets/"+id, s.screener, nil)
		}
	}
}

func (s *soak) report(elapsed time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ops []string
	total := 0
	for op, st := range s.stats {
		ops = append(ops, op)
		total += len(st.lat)
	}
	sort.Strings(ops)
	var b strings.Builder
	fmt.Fprintf(&b, "\n%d requests in %v: %.0f req/s\n", total, elapsed.Round(time.Millisecond), float64(total)/elapsed.Seconds())
	fmt.Fprintf(&b, "%-20s %7s %8s %8s %8s  %s\n", "op", "n", "p50", "p95", "p99", "status")
	for _, op := range ops {
		st := s.stats[op]
		slices.Sort(st.lat)
		q := func(p float64) time.Duration {
			return st.lat[int(p*float64(len(st.lat)-1))].Round(100 * time.Microsecond)
		}
		var codes []string
		for code, n := range st.codes {
			codes = append(codes, fmt.Sprintf("%d:%d", code, n))
		}
		sort.Strings(codes)
		fmt.Fprintf(&b, "%-20s %7d %8v %8v %8v  %s\n", op, len(st.lat), q(.5), q(.95), q(.99), strings.Join(codes, " "))
	}
	s.t.Log(b.String())
}
