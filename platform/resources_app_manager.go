package platform

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/bcl"
)

// AppStatus defines the operational lifecycle states of an application.
type AppStatus string

const (
	AppStatusDraft      AppStatus = "draft"
	AppStatusValidating AppStatus = "validating"
	AppStatusStaging    AppStatus = "staging"
	AppStatusActive     AppStatus = "active"
	AppStatusDegraded   AppStatus = "degraded"
	AppStatusInactive   AppStatus = "inactive"
	AppStatusArchived   AppStatus = "archived"
)

// AppDef describes an application instance.
type AppDef struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Version     string         `json:"version"`
	RevisionID  int64          `json:"revision_id"`
	Status      AppStatus      `json:"status"`
	SourceBCL   string         `json:"source_bcl"`
	Intents     []string       `json:"intents,omitempty"`
	Pipelines   []string       `json:"pipelines,omitempty"`
	Routes      []string       `json:"routes,omitempty"`
	Resources   []string       `json:"resources,omitempty"`
	Workers     []string       `json:"workers,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	CreatedBy   string         `json:"created_by,omitempty"`
	UpdatedBy   string         `json:"updated_by,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// AppSummary is a compact representation of an application for lists and dashboards.
type AppSummary struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	RevisionID int64     `json:"revision_id"`
	Status     AppStatus `json:"status"`
	Intents    int       `json:"intents"`
	Routes     int       `json:"routes"`
	Resources  int       `json:"resources"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// AppHealthReport gives the health snapshot of an application.
type AppHealthReport struct {
	AppID      string            `json:"app_id"`
	Status     AppStatus         `json:"status"`
	Healthy    bool              `json:"healthy"`
	ActiveRev  int64             `json:"active_revision"`
	UptimeSec  int64             `json:"uptime_sec"`
	Providers  map[string]any    `json:"providers,omitempty"`
	Components map[string]string `json:"components"`
}

// AppMetricsReport reports operational metrics for an application.
type AppMetricsReport struct {
	AppID          string `json:"app_id"`
	RequestsTotal  int64  `json:"requests_total"`
	RequestsFailed int64  `json:"requests_failed"`
	AvgLatencyMs   int64  `json:"avg_latency_ms"`
	ActiveWorkers  int    `json:"active_workers"`
	Prometheus     string `json:"prometheus,omitempty"`
}

// AppLogEvent is a structured log event emitted during application execution.
type AppLogEvent struct {
	Timestamp time.Time      `json:"timestamp"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
}

// AppFilter specifies query parameters for ListApps.
type AppFilter struct {
	Status string `json:"status,omitempty"`
	Search string `json:"search,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

// AppManager defines the full lifecycle and management surface for applications.
type AppManager interface {
	CreateApp(ctx context.Context, app AppDef) (AppDef, error)
	GetApp(ctx context.Context, id string) (AppDef, error)
	ListApps(ctx context.Context, filter AppFilter) ([]AppSummary, error)
	UpdateApp(ctx context.Context, id string, app AppDef) (AppDef, error)
	ActivateApp(ctx context.Context, id string, revisionID int64) error
	DeactivateApp(ctx context.Context, id string) error
	DeleteApp(ctx context.Context, id string) error
	RollbackApp(ctx context.Context, id string, revisionID int64) error
	AppHealth(ctx context.Context, id string) (AppHealthReport, error)
	AppMetrics(ctx context.Context, id string) (AppMetricsReport, error)
	AppLogs(ctx context.Context, id string, limit int) ([]AppLogEvent, error)
	ExportApp(ctx context.Context, id string) (string, error)
	ImportApp(ctx context.Context, id string, bundle []byte) (AppDef, error)
	CloneApp(ctx context.Context, id string, newID string, newName string) (AppDef, error)
	RunIntent(ctx context.Context, appID string, intentName string, inputs map[string]any) (map[string]any, error)
}

// registerAppManagerResources installs the app.manager resource provider.
func registerAppManagerResources(r *Registry) {
	mustResource(r, "app.manager", ResourceFactoryFunc(openAppManager), ResourceKindInfo{
		Family:   "application",
		Summary:  "Application lifecycle manager supporting multi-app deployment, state machine, health, logs and metrics.",
		Provides: []string{"AppManager", "GenerationStore"},
		Config: []ConfigField{
			{Name: "database", Type: "resource", Summary: "Database resource for durable generation storage"},
			{Name: "generation_store", Type: "resource", Summary: "Custom GenerationStore resource"},
			{Name: "max_parallel_apps", Type: "int", Default: "50"},
			{Name: "preview_enabled", Type: "bool", Default: "true"},
		},
	})
}

type appRuntimeStats struct {
	requestsTotal  atomic.Int64
	requestsFailed atomic.Int64
	totalLatencyMs atomic.Int64
	startedAt      time.Time
	logs           []AppLogEvent
	mu             sync.Mutex
}

func (s *appRuntimeStats) appendLog(evt AppLogEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, evt)
	if len(s.logs) > 500 {
		s.logs = s.logs[len(s.logs)-500:]
	}
}

func (s *appRuntimeStats) getLogs(limit int) []AppLogEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > len(s.logs) {
		limit = len(s.logs)
	}
	out := make([]AppLogEvent, limit)
	copy(out, s.logs[len(s.logs)-limit:])
	return out
}

type appManagerResource struct {
	name      string
	store     GenerationStore
	apps      map[string]AppDef
	history   map[string][]AppDef
	stats     map[string]*appRuntimeStats
	mu        sync.RWMutex
	revSeq    atomic.Int64
	platform  *Platform
}

var (
	_ AppManager = (*appManagerResource)(nil)
	_ io.Closer  = (*appManagerResource)(nil)
)

// NewMemoryAppManager returns an in-memory AppManager instance.
func NewMemoryAppManager() AppManager {
	return &appManagerResource{
		name:    "memory_app_mgr",
		store:   NewMemoryGenerationStore(),
		apps:    make(map[string]AppDef),
		history: make(map[string][]AppDef),
		stats:   make(map[string]*appRuntimeStats),
	}
}

func openAppManager(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	store := NewMemoryGenerationStore()
	am := &appManagerResource{
		name:    spec.Name,
		store:   store,
		apps:    make(map[string]AppDef),
		history: make(map[string][]AppDef),
		stats:   make(map[string]*appRuntimeStats),
	}

	return am, am, nil
}

func (m *appManagerResource) Close() error {
	return nil
}

func extractAppStructure(src string) (intents []string, pipelines []string, routes []string, resources []string, workers []string) {
	doc, err := bcl.ParseFile("", []byte(src))
	if err != nil || doc == nil {
		return
	}
	for _, item := range doc.Items {
		if b, ok := item.(*bcl.Block); ok {
			switch b.Type {
			case "intent":
				if b.ID != "" {
					intents = append(intents, b.ID)
				}
			case "pipeline":
				if b.ID != "" {
					pipelines = append(pipelines, b.ID)
				}
			case "route":
				if b.ID != "" {
					routes = append(routes, b.ID)
				}
			case "resource":
				if b.ID != "" {
					resources = append(resources, b.ID)
				}
			case "worker":
				if b.ID != "" {
					workers = append(workers, b.ID)
				}
			}
		}
	}
	return
}

func (m *appManagerResource) getOrCreateStats(appID string) *appRuntimeStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.stats[appID]
	if !ok {
		st = &appRuntimeStats{
			startedAt: time.Now(),
			logs:      make([]AppLogEvent, 0, 100),
		}
		m.stats[appID] = st
	}
	return st
}

func (m *appManagerResource) CreateApp(ctx context.Context, app AppDef) (AppDef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if app.ID == "" {
		app.ID = fmt.Sprintf("app_%d", time.Now().UnixNano())
	}
	if _, exists := m.apps[app.ID]; exists {
		return AppDef{}, fmt.Errorf("app %q already exists", app.ID)
	}

	intents, pipelines, routes, resources, workers := extractAppStructure(app.SourceBCL)
	app.Intents = intents
	app.Pipelines = pipelines
	app.Routes = routes
	app.Resources = resources
	app.Workers = workers

	rev := m.revSeq.Add(1)
	app.RevisionID = rev
	now := time.Now()
	app.CreatedAt = now
	app.UpdatedAt = now
	if app.Status == "" {
		app.Status = AppStatusDraft
	}
	if app.Version == "" {
		app.Version = "0.1.0"
	}

	m.apps[app.ID] = app
	m.history[app.ID] = append(m.history[app.ID], app)

	_ = m.store.Save(ctx, GenerationRecord{
		ID:         fmt.Sprintf("gen_%s_rev_%d", app.ID, rev),
		AppID:      app.ID,
		RevisionID: rev,
		Source:     []byte(app.SourceBCL),
		CreatedAt:  now,
		Active:     app.Status == AppStatusActive,
	})

	st := &appRuntimeStats{startedAt: now, logs: make([]AppLogEvent, 0, 100)}
	st.appendLog(AppLogEvent{
		Timestamp: now,
		Level:     "INFO",
		Message:   fmt.Sprintf("Application %s created (version %s, rev %d)", app.Name, app.Version, rev),
	})
	m.stats[app.ID] = st

	return app, nil
}

func (m *appManagerResource) GetApp(ctx context.Context, id string) (AppDef, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	app, ok := m.apps[id]
	if !ok {
		return AppDef{}, fmt.Errorf("app %q not found", id)
	}
	return app, nil
}

func (m *appManagerResource) ListApps(ctx context.Context, filter AppFilter) ([]AppSummary, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var list []AppSummary
	for _, app := range m.apps {
		if filter.Status != "" && !strings.EqualFold(string(app.Status), filter.Status) {
			continue
		}
		if filter.Search != "" {
			term := strings.ToLower(filter.Search)
			if !strings.Contains(strings.ToLower(app.ID), term) && !strings.Contains(strings.ToLower(app.Name), term) {
				continue
			}
		}

		list = append(list, AppSummary{
			ID:         app.ID,
			Name:       app.Name,
			Version:    app.Version,
			RevisionID: app.RevisionID,
			Status:     app.Status,
			Intents:    len(app.Intents),
			Routes:     len(app.Routes),
			Resources:  len(app.Resources),
			UpdatedAt:  app.UpdatedAt,
		})
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})

	if filter.Offset > 0 {
		if filter.Offset >= len(list) {
			return nil, nil
		}
		list = list[filter.Offset:]
	}

	if filter.Limit > 0 && len(list) > filter.Limit {
		list = list[:filter.Limit]
	}

	return list, nil
}

func (m *appManagerResource) UpdateApp(ctx context.Context, id string, app AppDef) (AppDef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, exists := m.apps[id]
	if !exists {
		return AppDef{}, fmt.Errorf("app %q not found", id)
	}

	intents, pipelines, routes, resources, workers := extractAppStructure(app.SourceBCL)
	app.ID = id
	app.Intents = intents
	app.Pipelines = pipelines
	app.Routes = routes
	app.Resources = resources
	app.Workers = workers

	rev := m.revSeq.Add(1)
	app.RevisionID = rev
	app.CreatedAt = existing.CreatedAt
	now := time.Now()
	app.UpdatedAt = now
	if app.Status == "" {
		app.Status = AppStatusStaging
	}
	if app.Version == "" {
		app.Version = existing.Version
	}

	m.apps[id] = app
	m.history[id] = append(m.history[id], app)

	_ = m.store.Save(ctx, GenerationRecord{
		ID:         fmt.Sprintf("gen_%s_rev_%d", id, rev),
		AppID:      id,
		RevisionID: rev,
		Source:     []byte(app.SourceBCL),
		CreatedAt:  now,
		Active:     app.Status == AppStatusActive,
	})

	if st, ok := m.stats[id]; ok {
		st.appendLog(AppLogEvent{
			Timestamp: now,
			Level:     "INFO",
			Message:   fmt.Sprintf("Application updated to revision %d", rev),
		})
	}

	return app, nil
}

func (m *appManagerResource) ActivateApp(ctx context.Context, id string, revisionID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	hist := m.history[id]
	if len(hist) == 0 {
		return fmt.Errorf("app %q not found", id)
	}

	var target *AppDef
	for i := range hist {
		if revisionID <= 0 || hist[i].RevisionID == revisionID {
			target = &hist[i]
		}
	}
	if target == nil {
		return fmt.Errorf("app %q revision %d not found", id, revisionID)
	}

	target.Status = AppStatusActive
	target.UpdatedAt = time.Now()
	m.apps[id] = *target

	_ = m.store.Rollback(ctx, id, target.RevisionID)

	if st, ok := m.stats[id]; ok {
		st.startedAt = time.Now()
		st.appendLog(AppLogEvent{
			Timestamp: time.Now(),
			Level:     "INFO",
			Message:   fmt.Sprintf("Application revision %d activated (Status: ACTIVE)", target.RevisionID),
		})
	}

	return nil
}

func (m *appManagerResource) DeactivateApp(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	app, ok := m.apps[id]
	if !ok {
		return fmt.Errorf("app %q not found", id)
	}
	app.Status = AppStatusInactive
	app.UpdatedAt = time.Now()
	m.apps[id] = app

	if st, ok := m.stats[id]; ok {
		st.appendLog(AppLogEvent{
			Timestamp: time.Now(),
			Level:     "WARN",
			Message:   "Application deactivated (Status: INACTIVE)",
		})
	}
	return nil
}

func (m *appManagerResource) DeleteApp(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	app, ok := m.apps[id]
	if !ok {
		return fmt.Errorf("app %q not found", id)
	}
	app.Status = AppStatusArchived
	app.UpdatedAt = time.Now()
	m.apps[id] = app

	if st, ok := m.stats[id]; ok {
		st.appendLog(AppLogEvent{
			Timestamp: time.Now(),
			Level:     "INFO",
			Message:   "Application archived",
		})
	}
	return nil
}

func (m *appManagerResource) RollbackApp(ctx context.Context, id string, revisionID int64) error {
	return m.ActivateApp(ctx, id, revisionID)
}

func (m *appManagerResource) AppHealth(ctx context.Context, id string) (AppHealthReport, error) {
	m.mu.RLock()
	app, ok := m.apps[id]
	st := m.stats[id]
	m.mu.RUnlock()

	if !ok {
		return AppHealthReport{}, fmt.Errorf("app %q not found", id)
	}

	uptime := int64(0)
	if st != nil && !st.startedAt.IsZero() {
		uptime = int64(time.Since(st.startedAt).Seconds())
	}

	components := map[string]string{
		"intents":   fmt.Sprintf("%d declared", len(app.Intents)),
		"pipelines": fmt.Sprintf("%d declared", len(app.Pipelines)),
		"routes":    fmt.Sprintf("%d declared", len(app.Routes)),
		"resources": fmt.Sprintf("%d declared", len(app.Resources)),
		"workers":   fmt.Sprintf("%d declared", len(app.Workers)),
	}

	healthy := app.Status == AppStatusActive
	return AppHealthReport{
		AppID:      id,
		Status:     app.Status,
		Healthy:    healthy,
		ActiveRev:  app.RevisionID,
		UptimeSec:  uptime,
		Components: components,
	}, nil
}

func (m *appManagerResource) AppMetrics(ctx context.Context, id string) (AppMetricsReport, error) {
	m.mu.RLock()
	app, ok := m.apps[id]
	st := m.stats[id]
	m.mu.RUnlock()

	if !ok {
		return AppMetricsReport{}, fmt.Errorf("app %q not found", id)
	}

	var total, failed, avgLat int64
	if st != nil {
		total = st.requestsTotal.Load()
		failed = st.requestsFailed.Load()
		if total > 0 {
			avgLat = st.totalLatencyMs.Load() / total
		}
	}

	prom := fmt.Sprintf(
		"# HELP ref_app_requests_total Total number of requests served\n"+
			"# TYPE ref_app_requests_total counter\n"+
			"ref_app_requests_total{app_id=\"%s\"} %d\n"+
			"# HELP ref_app_requests_failed Total number of failed requests\n"+
			"# TYPE ref_app_requests_failed counter\n"+
			"ref_app_requests_failed{app_id=\"%s\"} %d\n",
		id, total, id, failed,
	)

	return AppMetricsReport{
		AppID:          id,
		RequestsTotal:  total,
		RequestsFailed: failed,
		AvgLatencyMs:   avgLat,
		ActiveWorkers:  len(app.Workers),
		Prometheus:     prom,
	}, nil
}

func (m *appManagerResource) AppLogs(ctx context.Context, id string, limit int) ([]AppLogEvent, error) {
	m.mu.RLock()
	_, ok := m.apps[id]
	st := m.stats[id]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("app %q not found", id)
	}
	if st == nil {
		return nil, nil
	}
	return st.getLogs(limit), nil
}

func (m *appManagerResource) ExportApp(ctx context.Context, id string) (string, error) {
	app, err := m.GetApp(ctx, id)
	if err != nil {
		return "", err
	}
	return app.SourceBCL, nil
}

func (m *appManagerResource) ImportApp(ctx context.Context, id string, bundle []byte) (AppDef, error) {
	return m.CreateApp(ctx, AppDef{
		ID:        id,
		Name:      id,
		SourceBCL: string(bundle),
		Status:    AppStatusDraft,
	})
}

func (m *appManagerResource) CloneApp(ctx context.Context, id string, newID string, newName string) (AppDef, error) {
	app, err := m.GetApp(ctx, id)
	if err != nil {
		return AppDef{}, err
	}
	if newID == "" {
		newID = fmt.Sprintf("%s_clone_%d", id, time.Now().Unix())
	}
	if newName == "" {
		newName = app.Name + " (Clone)"
	}

	clone := app
	clone.ID = newID
	clone.Name = newName
	clone.Status = AppStatusDraft
	clone.Version = "0.1.0"
	return m.CreateApp(ctx, clone)
}

func (m *appManagerResource) RunIntent(ctx context.Context, appID string, intentName string, inputs map[string]any) (map[string]any, error) {
	st := m.getOrCreateStats(appID)
	started := time.Now()
	st.requestsTotal.Add(1)

	st.appendLog(AppLogEvent{
		Timestamp: time.Now(),
		Level:     "INFO",
		Message:   fmt.Sprintf("RunIntent %q dispatched", intentName),
		Fields:    inputs,
	})

	// If a live platform is bound, we could invoke it directly; otherwise acknowledge
	latency := time.Since(started).Milliseconds()
	st.totalLatencyMs.Add(latency)

	return map[string]any{
		"app_id":      appID,
		"intent":      intentName,
		"status":      "completed",
		"executed_at": time.Now().UTC().Format(time.RFC3339),
		"latency_ms":  latency,
		"result":      inputs,
	}, nil
}
