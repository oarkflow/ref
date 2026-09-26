package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // Use pure Go SQLite driver

	"github.com/oarkflow/ref/capability"
	"github.com/oarkflow/ref/effect"
	"github.com/oarkflow/ref/execution"
	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/invocation"
	"github.com/oarkflow/ref/observer"
	"github.com/oarkflow/ref/runtime"
	"github.com/oarkflow/ref/source"
)

// --- Domain Models ---
type User struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type TenantSettings struct {
	TenantID string `json:"tenant_id"`
	Theme    string `json:"theme"`
}

// DashboardResponse is what the intent returns
type DashboardResponse struct {
	Settings TenantSettings `json:"settings"`
	Users    map[int64]User `json:"users"`
}

// DashboardRequest is the incoming payload
type DashboardRequest struct {
	TenantID string  `json:"tenant_id"`
	UIDs     []int64 `json:"uids"`
}

// --- Fact Keys ---
var (
	KeyDashboardReq   = fact.NewKey[DashboardRequest]("dashboard.request")
	KeyUserResult     = fact.NewKey[map[int64]User]("users.result")
	KeyTenantSettings = fact.NewKey[TenantSettings]("tenant.settings")
)

// --- Live Database Setup ---
var db *sql.DB
var dbQueryCount atomic.Int32
var settingsQueryCount atomic.Int32

func setupDatabase() {
	var err error
	// Use an in-memory SQLite database for the live example
	db, err = sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatalf("Failed to open SQLite database: %v", err)
	}

	// Create Tables
	_, err = db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
		CREATE TABLE tenant_settings (tenant_id TEXT PRIMARY KEY, theme TEXT);
	`)
	if err != nil {
		log.Fatalf("Failed to create tables: %v", err)
	}

	// Insert Test Data
	_, err = db.Exec(`
		INSERT INTO users (id, name) VALUES
		(101, 'Alice'), (102, 'Bob'), (103, 'Charlie'), (104, 'David'), (105, 'Eve'),
		(201, 'Frank'), (202, 'Grace'), (999, 'Tony Stark');

		INSERT INTO tenant_settings (tenant_id, theme) VALUES
		('tenant-acme', 'dark'), ('tenant-stark', 'light');
	`)
	if err != nil {
		log.Fatalf("Failed to insert data: %v", err)
	}
}

// --- Live Database Fetchers ---

func liveDBFetchUsers(ctx context.Context, ids []int64) (map[int64]User, error) {
	if len(ids) == 0 {
		return make(map[int64]User), nil
	}
	dbQueryCount.Add(1)
	log.Printf("[LIVE DB] Executing SQL batch query for %d users: %v", len(ids), ids)

	// Build query: SELECT id, name FROM users WHERE id IN (?, ?, ...)
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf("SELECT id, name FROM users WHERE id IN (%s)", strings.Join(placeholders, ","))

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make(map[int64]User)
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name); err != nil {
			return nil, err
		}
		results[u.ID] = u
	}
	return results, nil
}

func liveDBFetchSettings(ctx context.Context, tenantID string) (TenantSettings, error) {
	dbQueryCount.Add(1)
	settingsQueryCount.Add(1)
	log.Printf("[LIVE DB] Fetching settings for tenant: %s", tenantID)

	var s TenantSettings
	err := db.QueryRowContext(ctx, "SELECT tenant_id, theme FROM tenant_settings WHERE tenant_id = ?", tenantID).Scan(&s.TenantID, &s.Theme)
	if err != nil {
		return TenantSettings{}, err
	}
	return s, nil
}

// --- Custom Observer for Metrics ---
type metricsObserver struct {
	observer.Noop
	collector *source.MetricsCollector
	mu        sync.Mutex
}

func (m *metricsObserver) SourceFetched(metrics observer.SourceMetrics) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sm := source.Metrics{
		SourceName:  metrics.SourceName,
		Operation:   metrics.Operation,
		QueryHash:   metrics.QueryHash,
		QueueWait:   metrics.QueueWait,
		ConnWait:    metrics.ConnWait,
		ExecTime:    metrics.ExecTime,
		DecodeTime:  metrics.DecodeTime,
		TotalTime:   metrics.TotalTime,
		ResultCount: metrics.ResultCount,
		ResultBytes: metrics.ResultBytes,
		CacheHit:    metrics.CacheHit,
		CacheLevel:  metrics.CacheLevel,
		Batched:     metrics.Batched,
		BatchSize:   metrics.BatchSize,
		Coalesced:   metrics.Coalesced,
		Error:       metrics.Error,
	}
	m.collector.Record(sm)
}

func adapt(sr source.Registration) capability.Registration {
	return capability.Registration{
		Name:        sr.Name,
		Requires:    sr.Requires,
		Provides:    sr.Provides,
		Kind:        sr.Kind,
		Speculation: sr.Speculation,
		Source:      sr.Source,
		Run:         sr.Run,
	}
}

func main() {
	if _, err := run(os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// scenarioStats records the live database queries each scenario executed,
// in total and for tenant settings alone.
type scenarioStats struct {
	Queries         [3]int32
	SettingsQueries [3]int32
}

// run executes the three scenarios, writing the report to out.
func run(out io.Writer) (scenarioStats, error) {
	var counts scenarioStats
	fmt.Fprintln(out, "=== REF DataSource Subsystem (Live SQLite Database) ===")
	setupDatabase()
	defer db.Close()
	dbQueryCount.Store(0)
	settingsQueryCount.Store(0)

	// 1. Setup Caching and Metrics
	cache := source.NewProcessCache(source.ProcessCacheConfig{MaxEntries: 1000})
	coalescer := source.NewCoalescer()
	metricsColl := source.NewMetricsCollector()
	metricsObs := &metricsObserver{collector: metricsColl}
	compObs := observer.NewCompositeObserver(nil, observer.TieredObserver{Observer: metricsObs, Tier: observer.Critical})

	// 2. Define Capabilities
	// The DataLoader collapses N simultaneous fetches into a single SQL IN (...) clause.
	userLoader := source.NewLoader[int64, User](liveDBFetchUsers, source.LoaderConfig{
		Wait:     5 * time.Millisecond,
		MaxBatch: 100,
	})

	nPlusOneResolver := capability.NewRegistration(
		"capability.resolve_users_batched",
		graph.ReadNode,
		capability.WithSource(&source.Spec{
			Name:      "users-db",
			Kind:      source.KindDatabase,
			Batchable: true,
		}),
	).WithRequires(KeyDashboardReq.Any()).
		WithProvides(KeyUserResult.Any()).
		WithRun(func(nc *execution.NodeContext) error {
			start := time.Now()
			slot, _ := nc.SlotOf(KeyDashboardReq.DefinitionID())
			req, _ := fact.Get[DashboardRequest](nc.Facts(), slot)

			var wg sync.WaitGroup
			var mu sync.Mutex
			results := make(map[int64]User)

			for _, uid := range req.UIDs {
				wg.Add(1)
				go func(id int64) {
					defer wg.Done()
					// 🚀 THIS IS THE MAGIC: N concurrent Loads collapse into 1 batch SQL call
					user, err := userLoader.Load(context.Background(), id)
					if err == nil {
						mu.Lock()
						results[id] = user
						mu.Unlock()
					}
				}(uid)
			}
			wg.Wait()

			metricsObs.SourceFetched(observer.SourceMetrics{
				SourceName: "users-db",
				Operation:  "batch_fetch",
				Batched:    true,
				BatchSize:  len(req.UIDs),
				ExecTime:   time.Since(start),
				TotalTime:  time.Since(start),
			})

			outSlot, _ := nc.SlotOf(KeyUserResult.DefinitionID())
			fact.Put(nc.Facts(), outSlot, results)
			return nil
		})

	settingsFetcherReg := source.NewFetchCapability(
		"capability.fetch_tenant_settings",
		source.Spec{
			Name:     "tenant-db",
			Kind:     source.KindDatabase,
			ReadOnly: true,
			// Tenant settings are not principal-scoped and the key function
			// already includes the tenant ID. Without SecurityPublic the
			// default tenant scope fails closed (no cache, no coalescing)
			// because this example performs no authorization decision.
			Security:    source.SecurityPublic,
			Cacheable:   true,
			Coalescible: true,
			CacheTTL:    5 * time.Minute,
			CacheScope:  source.ScopeProcess,
			Consistency: source.Eventual,
		},
		KeyTenantSettings.Any(),
		func(nc *execution.NodeContext) (any, error) {
			slot, _ := nc.SlotOf(KeyDashboardReq.DefinitionID())
			req, _ := fact.Get[DashboardRequest](nc.Facts(), slot)
			return liveDBFetchSettings(context.Background(), req.TenantID)
		},
		source.WithCache(cache),
		source.WithCoalescer(coalescer),
		source.WithKeyFunc(func(nc *execution.NodeContext) string {
			slot, _ := nc.SlotOf(KeyDashboardReq.DefinitionID())
			req, _ := fact.Get[DashboardRequest](nc.Facts(), slot)
			return req.TenantID
		}),
		source.WithMetricsObserver(func(sm source.Metrics) {
			// MetricsCollector is not safe for concurrent use; share the
			// observer's lock since both paths record into it.
			metricsObs.mu.Lock()
			defer metricsObs.mu.Unlock()
			metricsColl.Record(sm)
		}),
	)
	settingsFetcherReg.Requires = []fact.AnyKey{KeyDashboardReq.Any()}
	settingsFetcher := adapt(settingsFetcherReg)

	// 3. Define the Dashboard Intent
	dashboardIntent := &intent.Definition{
		Name: "LoadDashboardData",
		Spec: intent.Spec{
			Requires: []fact.AnyKey{KeyUserResult.Any(), KeyTenantSettings.Any()},
		},
		InputKey: KeyDashboardReq.Any(),
		DecodeNode: func(nc *execution.NodeContext) error {
			var req DashboardRequest
			if err := json.Unmarshal(nc.Invocation().Input.RawBytes(), &req); err != nil {
				return err
			}
			slot, _ := nc.SlotOf(KeyDashboardReq.DefinitionID())
			fact.Put(nc.Facts(), slot, req)
			return nil
		},
		Run: func(nc *execution.NodeContext) (any, effect.EffectPlan, intent.OutcomeMeta, error) {
			s1, _ := nc.SlotOf(KeyUserResult.DefinitionID())
			users, _ := fact.Get[map[int64]User](nc.Facts(), s1)
			s2, _ := nc.SlotOf(KeyTenantSettings.DefinitionID())
			settings, _ := fact.Get[TenantSettings](nc.Facts(), s2)

			res := DashboardResponse{Users: users, Settings: settings}
			return res, effect.EffectPlan{}, intent.OutcomeMeta{}, nil
		},
	}

	engine := runtime.NewEngine(runtime.WithCapability(nPlusOneResolver), runtime.WithCapability(settingsFetcher), runtime.WithObserver(compObs))
	if err := engine.RegisterDefinition(dashboardIntent); err != nil {
		return counts, fmt.Errorf("RegisterDefinition failed: %w", err)
	}
	if err := engine.Compile(); err != nil {
		return counts, fmt.Errorf("Compile failed: %w", err)
	}

	runIntent := func(req DashboardRequest) error {
		payload, _ := json.Marshal(req)
		inv := &invocation.Invocation{
			ID:     "inv-1",
			Intent: "LoadDashboardData",
			Input:  invocation.NewInputDirect(payload, "application/json"),
		}
		res, err := engine.Dispatch(context.Background(), inv)
		if err != nil {
			return fmt.Errorf("Dispatch failed: %w", err)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Fprintf(out, "Response: %s\n", string(data))
		return nil
	}

	// === SCENARIO 1: First Request (Cold Cache, simulates N+1) ===
	fmt.Fprintln(out, "\n--- Scenario 1: First Request (Cold Cache, 5 concurrent user loads) ---")
	if err := runIntent(DashboardRequest{TenantID: "tenant-acme", UIDs: []int64{101, 102, 103, 104, 105}}); err != nil {
		return counts, err
	}
	counts.Queries[0], counts.SettingsQueries[0] = dbQueryCount.Load(), settingsQueryCount.Load()
	fmt.Fprintf(out, "Live DB Queries Executed: %d (Expected: 2 -> 1 for settings, 1 for batched users!)\n", dbQueryCount.Load())

	// === SCENARIO 2: Second Request (Hot Cache) ===
	fmt.Fprintln(out, "\n--- Scenario 2: Second Request (Hot Cache for Tenant API) ---")
	dbQueryCount.Store(0)
	settingsQueryCount.Store(0)
	if err := runIntent(DashboardRequest{TenantID: "tenant-acme", UIDs: []int64{201, 202}}); err != nil {
		return counts, err
	}
	counts.Queries[1], counts.SettingsQueries[1] = dbQueryCount.Load(), settingsQueryCount.Load()
	fmt.Fprintf(out, "Live DB Queries Executed: %d (Expected: 1 -> Users only, Settings served from ProcessCache!)\n", dbQueryCount.Load())

	// === SCENARIO 3: High Concurrency Thundering Herd (Coalescing) ===
	fmt.Fprintln(out, "\n--- Scenario 3: Thundering Herd (100 simultaneous requests) ---")
	dbQueryCount.Store(0)
	settingsQueryCount.Store(0)
	cache.InvalidateSource("tenant-db")

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload, _ := json.Marshal(DashboardRequest{TenantID: "tenant-stark", UIDs: []int64{999}})
			inv := &invocation.Invocation{
				ID:     "inv-n",
				Intent: "LoadDashboardData",
				Input:  invocation.NewInputDirect(payload, "application/json"),
			}
			_, _ = engine.Dispatch(context.Background(), inv)
		}()
	}
	wg.Wait()

	counts.Queries[2], counts.SettingsQueries[2] = dbQueryCount.Load(), settingsQueryCount.Load()
	fmt.Fprintf(out, "Live DB Queries Executed for 100 concurrent requests: %d (Expected: 2 -> 1 for batched users, 1 for settings via Coalescer!)\n", counts.Queries[2])
	return counts, nil
}
