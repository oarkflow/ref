package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/bcl"
)

// ---------------------------------------------------------------------------
// Generation Store & Versioning
// ---------------------------------------------------------------------------

// GenerationRecord is one compiled application or workflow generation stored durably.
type GenerationRecord struct {
	ID          string         `json:"id"`
	AppID       string         `json:"app_id"`
	RevisionID  int64          `json:"revision_id"`
	Source      []byte         `json:"source"`
	Schema      string         `json:"schema"`
	CreatedAt   time.Time      `json:"created_at"`
	CreatedBy   string         `json:"created_by,omitempty"`
	ActivatedAt time.Time      `json:"activated_at,omitempty"`
	Active      bool           `json:"active"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// GenerationStore persists application and workflow generations for audit,
// rollback, and multi-replica synchronization.
type GenerationStore interface {
	Save(ctx context.Context, gen GenerationRecord) error
	GetActive(ctx context.Context, appID string) (GenerationRecord, bool, error)
	GetRevision(ctx context.Context, appID string, revisionID int64) (GenerationRecord, bool, error)
	ListRevisions(ctx context.Context, appID string, limit int) ([]GenerationRecord, error)
	Rollback(ctx context.Context, appID string, revisionID int64) error
}

// MemoryGenerationStore is an in-memory thread-safe GenerationStore.
type MemoryGenerationStore struct {
	mu        sync.RWMutex
	records   map[string][]GenerationRecord // appID -> records sorted by revision
	activeGen map[string]int64              // appID -> active revisionID
}

// NewMemoryGenerationStore returns an initialized in-memory GenerationStore.
func NewMemoryGenerationStore() *MemoryGenerationStore {
	return &MemoryGenerationStore{
		records:   make(map[string][]GenerationRecord),
		activeGen: make(map[string]int64),
	}
}

func (s *MemoryGenerationStore) Save(ctx context.Context, gen GenerationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	appID := gen.AppID
	if appID == "" {
		appID = "default"
	}
	gen.AppID = appID

	if gen.CreatedAt.IsZero() {
		gen.CreatedAt = time.Now()
	}
	if gen.Schema == "" && len(gen.Source) > 0 {
		h := sha256.Sum256(gen.Source)
		gen.Schema = hex.EncodeToString(h[:])
	}

	list := s.records[appID]
	if gen.RevisionID <= 0 {
		var maxRev int64
		for _, r := range list {
			if r.RevisionID > maxRev {
				maxRev = r.RevisionID
			}
		}
		gen.RevisionID = maxRev + 1
	}

	if gen.Active {
		gen.ActivatedAt = time.Now()
		// Deactivate others
		for i := range list {
			list[i].Active = false
		}
		s.activeGen[appID] = gen.RevisionID
	}

	s.records[appID] = append(list, gen)
	return nil
}

func (s *MemoryGenerationStore) GetActive(ctx context.Context, appID string) (GenerationRecord, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if appID == "" {
		appID = "default"
	}

	activeRev, hasActive := s.activeGen[appID]
	if !hasActive {
		return GenerationRecord{}, false, nil
	}

	for _, r := range s.records[appID] {
		if r.RevisionID == activeRev {
			return r, true, nil
		}
	}
	return GenerationRecord{}, false, nil
}

func (s *MemoryGenerationStore) GetRevision(ctx context.Context, appID string, revisionID int64) (GenerationRecord, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if appID == "" {
		appID = "default"
	}

	for _, r := range s.records[appID] {
		if r.RevisionID == revisionID {
			return r, true, nil
		}
	}
	return GenerationRecord{}, false, nil
}

func (s *MemoryGenerationStore) ListRevisions(ctx context.Context, appID string, limit int) ([]GenerationRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if appID == "" {
		appID = "default"
	}

	src := s.records[appID]
	out := make([]GenerationRecord, len(src))
	copy(out, src)

	// Sort descending by revision
	sort.Slice(out, func(i, j int) bool {
		return out[i].RevisionID > out[j].RevisionID
	})

	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryGenerationStore) Rollback(ctx context.Context, appID string, revisionID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if appID == "" {
		appID = "default"
	}

	list := s.records[appID]
	found := false
	for i := range list {
		if list[i].RevisionID == revisionID {
			list[i].Active = true
			list[i].ActivatedAt = time.Now()
			s.activeGen[appID] = revisionID
			found = true
		} else {
			list[i].Active = false
		}
	}

	if !found {
		return fmt.Errorf("revision %d not found for app %q", revisionID, appID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dynamic Workflow Model
// ---------------------------------------------------------------------------

// WorkflowDef is the full definition of a dynamic workflow.
type WorkflowDef struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Version     string         `json:"version"`
	RevisionID  int64          `json:"revision_id"`
	Status      string         `json:"status"` // draft | active | deprecated | archived
	SourceBCL   string         `json:"source_bcl"`
	Intents     []string       `json:"intents,omitempty"`
	Pipelines   []string       `json:"pipelines,omitempty"`
	Routes      []string       `json:"routes,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	UpdatedBy   string         `json:"updated_by,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// WorkflowSummary is a lightweight view of a workflow.
type WorkflowSummary struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	RevisionID int64     `json:"revision_id"`
	Status     string    `json:"status"`
	Intents    int       `json:"intents"`
	Pipelines  int       `json:"pipelines"`
	Routes     int       `json:"routes"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// WorkflowFilter specifies filter parameters for ListWorkflows.
type WorkflowFilter struct {
	Status string `json:"status,omitempty"`
	Search string `json:"search,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

// WorkflowManager is the runtime authoring and management contract for workflows.
type WorkflowManager interface {
	CreateWorkflow(ctx context.Context, def WorkflowDef) (WorkflowDef, error)
	UpdateWorkflow(ctx context.Context, id string, def WorkflowDef) (WorkflowDef, error)
	ActivateWorkflow(ctx context.Context, id string, revisionID int64) error
	DeleteWorkflow(ctx context.Context, id string) error
	ListWorkflows(ctx context.Context, filter WorkflowFilter) ([]WorkflowSummary, error)
	GetWorkflow(ctx context.Context, id string) (WorkflowDef, error)
	ValidateWorkflow(ctx context.Context, def WorkflowDef) ([]Diagnostic, error)
	RollbackWorkflow(ctx context.Context, id string, revisionID int64) error
	ExportWorkflow(ctx context.Context, id string) (string, error)
	CloneWorkflow(ctx context.Context, id string, newID string, newName string) (WorkflowDef, error)
}

// ---------------------------------------------------------------------------
// Workflow Manager Resource Implementation
// ---------------------------------------------------------------------------

// registerWorkflowManagerResources installs the workflow.manager resource provider.
func registerWorkflowManagerResources(r *Registry) {
	mustResource(r, "workflow.manager", ResourceFactoryFunc(openWorkflowManager), ResourceKindInfo{
		Family:   "workflow",
		Summary:  "Dynamic workflow and pipeline authoring engine with versioning, BCL validation, and hot reload.",
		Provides: []string{"WorkflowManager", "GenerationStore"},
		Config: []ConfigField{
			{Name: "database", Type: "resource", Summary: "Database resource for durable generation storage"},
			{Name: "store_table", Type: "string", Default: "ref_workflows"},
			{Name: "hot_reload", Type: "bool", Default: "true", Summary: "Atomically update running platform without restart"},
		},
	})
}

type workflowManagerResource struct {
	name       string
	store      GenerationStore
	hotReload  bool
	workflows  map[string]WorkflowDef
	history    map[string][]WorkflowDef
	mu         sync.RWMutex
	revCounter atomic.Int64
}

var (
	_ WorkflowManager = (*workflowManagerResource)(nil)
	_ io.Closer       = (*workflowManagerResource)(nil)
)

func openWorkflowManager(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	hotReload := configBool(spec.Config, "hot_reload", true)
	store := NewMemoryGenerationStore()

	wm := &workflowManagerResource{
		name:      spec.Name,
		store:     store,
		hotReload: hotReload,
		workflows: make(map[string]WorkflowDef),
		history:   make(map[string][]WorkflowDef),
	}

	return wm, wm, nil
}

func (w *workflowManagerResource) Close() error {
	return nil
}

func (w *workflowManagerResource) ValidateWorkflow(ctx context.Context, def WorkflowDef) ([]Diagnostic, error) {
	var diags []Diagnostic
	if strings.TrimSpace(def.SourceBCL) == "" {
		diags = append(diags, Diagnostic{
			Severity: SeverityError,
			Code:     DiagParse,
			Message:  "source_bcl cannot be empty",
		})
		return diags, nil
	}

	doc, err := bcl.ParseFile("", []byte(def.SourceBCL))
	if err != nil {
		diags = append(diags, Diagnostic{
			Severity: SeverityError,
			Code:     DiagParse,
			Message:  fmt.Sprintf("BCL parse error: %v", err),
		})
		return diags, nil
	}

	if doc == nil || len(doc.Items) == 0 {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Code:     DiagWarning,
			Message:  "workflow contains no top-level blocks",
		})
	}

	return diags, nil
}

func extractWorkflowBlocks(src string) (intents []string, pipelines []string, routes []string) {
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
			}
		}
	}
	return
}

func (w *workflowManagerResource) CreateWorkflow(ctx context.Context, def WorkflowDef) (WorkflowDef, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if def.ID == "" {
		def.ID = fmt.Sprintf("wf_%d", time.Now().UnixNano())
	}
	if _, exists := w.workflows[def.ID]; exists {
		return WorkflowDef{}, fmt.Errorf("workflow %q already exists", def.ID)
	}

	// Validate BCL
	diags, err := w.ValidateWorkflow(ctx, def)
	if err != nil {
		return WorkflowDef{}, err
	}
	for _, d := range diags {
		if d.Severity == SeverityError {
			return WorkflowDef{}, fmt.Errorf("validation error: %s", d.Message)
		}
	}

	intents, pipelines, routes := extractWorkflowBlocks(def.SourceBCL)
	def.Intents = intents
	def.Pipelines = pipelines
	def.Routes = routes

	rev := w.revCounter.Add(1)
	def.RevisionID = rev
	now := time.Now()
	def.CreatedAt = now
	def.UpdatedAt = now
	if def.Status == "" {
		def.Status = "draft"
	}
	if def.Version == "" {
		def.Version = "0.1.0"
	}

	w.workflows[def.ID] = def
	w.history[def.ID] = append(w.history[def.ID], def)

	// Save to generation store
	_ = w.store.Save(ctx, GenerationRecord{
		ID:         fmt.Sprintf("gen_%s_rev_%d", def.ID, rev),
		AppID:      def.ID,
		RevisionID: rev,
		Source:     []byte(def.SourceBCL),
		CreatedAt:  now,
		Active:     def.Status == "active",
	})

	return def, nil
}

func (w *workflowManagerResource) UpdateWorkflow(ctx context.Context, id string, def WorkflowDef) (WorkflowDef, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	existing, exists := w.workflows[id]
	if !exists {
		return WorkflowDef{}, fmt.Errorf("workflow %q not found", id)
	}

	// Validate BCL
	diags, err := w.ValidateWorkflow(ctx, def)
	if err != nil {
		return WorkflowDef{}, err
	}
	for _, d := range diags {
		if d.Severity == SeverityError {
			return WorkflowDef{}, fmt.Errorf("validation error: %s", d.Message)
		}
	}

	intents, pipelines, routes := extractWorkflowBlocks(def.SourceBCL)
	def.ID = id
	def.Intents = intents
	def.Pipelines = pipelines
	def.Routes = routes

	rev := w.revCounter.Add(1)
	def.RevisionID = rev
	def.CreatedAt = existing.CreatedAt
	now := time.Now()
	def.UpdatedAt = now
	if def.Status == "" {
		def.Status = existing.Status
	}
	if def.Version == "" {
		def.Version = existing.Version
	}

	w.workflows[id] = def
	w.history[id] = append(w.history[id], def)

	_ = w.store.Save(ctx, GenerationRecord{
		ID:         fmt.Sprintf("gen_%s_rev_%d", id, rev),
		AppID:      id,
		RevisionID: rev,
		Source:     []byte(def.SourceBCL),
		CreatedAt:  now,
		Active:     def.Status == "active",
	})

	return def, nil
}

func (w *workflowManagerResource) ActivateWorkflow(ctx context.Context, id string, revisionID int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	hist := w.history[id]
	if len(hist) == 0 {
		return fmt.Errorf("workflow %q not found", id)
	}

	var target *WorkflowDef
	for i := range hist {
		if revisionID <= 0 || hist[i].RevisionID == revisionID {
			target = &hist[i]
		}
	}
	if target == nil {
		return fmt.Errorf("workflow %q revision %d not found", id, revisionID)
	}

	target.Status = "active"
	target.UpdatedAt = time.Now()
	w.workflows[id] = *target

	return w.store.Rollback(ctx, id, target.RevisionID)
}

func (w *workflowManagerResource) DeleteWorkflow(ctx context.Context, id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	wf, ok := w.workflows[id]
	if !ok {
		return fmt.Errorf("workflow %q not found", id)
	}
	wf.Status = "archived"
	wf.UpdatedAt = time.Now()
	w.workflows[id] = wf
	return nil
}

func (w *workflowManagerResource) ListWorkflows(ctx context.Context, filter WorkflowFilter) ([]WorkflowSummary, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	var list []WorkflowSummary
	for _, wf := range w.workflows {
		if filter.Status != "" && !strings.EqualFold(wf.Status, filter.Status) {
			continue
		}
		if filter.Search != "" {
			term := strings.ToLower(filter.Search)
			if !strings.Contains(strings.ToLower(wf.ID), term) && !strings.Contains(strings.ToLower(wf.Name), term) {
				continue
			}
		}

		list = append(list, WorkflowSummary{
			ID:         wf.ID,
			Name:       wf.Name,
			Version:    wf.Version,
			RevisionID: wf.RevisionID,
			Status:     wf.Status,
			Intents:    len(wf.Intents),
			Pipelines:  len(wf.Pipelines),
			Routes:     len(wf.Routes),
			UpdatedAt:  wf.UpdatedAt,
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

func (w *workflowManagerResource) GetWorkflow(ctx context.Context, id string) (WorkflowDef, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	wf, ok := w.workflows[id]
	if !ok {
		return WorkflowDef{}, fmt.Errorf("workflow %q not found", id)
	}
	return wf, nil
}

func (w *workflowManagerResource) RollbackWorkflow(ctx context.Context, id string, revisionID int64) error {
	return w.ActivateWorkflow(ctx, id, revisionID)
}

func (w *workflowManagerResource) ExportWorkflow(ctx context.Context, id string) (string, error) {
	wf, err := w.GetWorkflow(ctx, id)
	if err != nil {
		return "", err
	}
	return wf.SourceBCL, nil
}

func (w *workflowManagerResource) CloneWorkflow(ctx context.Context, id string, newID string, newName string) (WorkflowDef, error) {
	wf, err := w.GetWorkflow(ctx, id)
	if err != nil {
		return WorkflowDef{}, err
	}

	if newID == "" {
		newID = fmt.Sprintf("%s_clone_%d", id, time.Now().Unix())
	}
	if newName == "" {
		newName = wf.Name + " (Clone)"
	}

	clone := wf
	clone.ID = newID
	clone.Name = newName
	clone.Status = "draft"
	clone.Version = "0.1.0"

	return w.CreateWorkflow(ctx, clone)
}
