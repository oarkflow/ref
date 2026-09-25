package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/oarkflow/ref/intent"
)

// External orchestrators (Temporal/Cadence gateways, DAG engines, an in-house
// workflow service) are reached through the WorkflowService boundary. The
// platform's own durable engine is process.*; workflow.* exists for
// deployments that already run an orchestrator and want BCL to drive it.
//
// Any resource implementing WorkflowService works — a host registers one with
// RegisterResourceDriver. The built-in workflow.http adapter speaks a small
// REST convention through a service.http resource, so the resource's
// allowlist, timeout and retry policy apply to every orchestrator call.

func registerWorkflowResources(r *Registry) {
	mustResource(r, "workflow.http", ResourceFactoryFunc(openHTTPWorkflow), ResourceKindInfo{
		Family:   "workflow",
		Summary:  "External orchestrator over REST: POST start, POST signal, GET status",
		Provides: []string{"WorkflowService"},
		Config: []ConfigField{
			{Name: "service", Type: "string", Required: true, Summary: "service.http resource pointing at the orchestrator"},
			{Name: "start_path", Type: "string", Default: "/workflows/{workflow}/runs"},
			{Name: "signal_path", Type: "string", Default: "/runs/{run_id}/signals/{signal}"},
			{Name: "status_path", Type: "string", Default: "/runs/{run_id}"},
		},
	})
}

func registerWorkflowActions(r *Registry) {
	mustAction(r, "workflow.start", ActionFactoryFunc(buildWorkflowStart), ActionInfo{
		Family:       "workflow",
		Summary:      "Start a run on an external orchestrator",
		ResourceKind: "workflow",
		Provides:     "The run: id, workflow, status, version, result",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "workflow", Type: "template", Required: true},
			{Name: "input_fact", Type: "fact", Default: "input"},
			{Name: "idempotency_key", Type: "template", Summary: "Deduplicates starts on the orchestrator side"},
		},
	})
	mustAction(r, "workflow.signal", ActionFactoryFunc(buildWorkflowSignal), ActionInfo{
		Family:       "workflow",
		Summary:      "Send a signal to a running external workflow",
		ResourceKind: "workflow",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "run_fact", Type: "fact", Required: true},
			{Name: "signal", Type: "template", Required: true},
			{Name: "payload_fact", Type: "fact"},
		},
	})
	mustAction(r, "workflow.status", ActionFactoryFunc(buildWorkflowStatus), ActionInfo{
		Family:       "workflow",
		Summary:      "Read an external workflow run's status",
		ResourceKind: "workflow",
		Provides:     "The run: id, workflow, status, version, result",
		Kind:         "read",
		Config:       []ConfigField{{Name: "run_fact", Type: "fact", Required: true}},
	})
}

func workflowRunView(run WorkflowRun) map[string]any {
	out := map[string]any{"id": run.ID, "workflow": run.Workflow, "status": run.Status, "version": run.Version}
	if run.Result != nil {
		out["result"] = run.Result
	}
	return out
}

func buildWorkflowStart(build BuildContext, spec NodeSpec) (Action, error) {
	service, err := requireResource[WorkflowService](build, spec, "a workflow resource (e.g. workflow.http)")
	if err != nil {
		return nil, err
	}
	name, err := configTemplate(spec.Config, "workflow", "")
	if err != nil || name == nil {
		return nil, fmt.Errorf("node %q: workflow.start needs config.workflow", spec.Name)
	}
	key, err := configTemplate(spec.Config, "idempotency_key", "")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	inputFact := configString(spec.Config, "input_fact", "input")
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		workflow, err := name.Render(env)
		if err != nil {
			return ActionResult{}, err
		}
		opts := WorkflowStartOptions{TenantID: ctx.TenantID}
		if key != nil {
			if opts.IdempotencyKey, err = key.Render(env); err != nil {
				return ActionResult{}, err
			}
		}
		input, _ := resolvePath(ctx.Inputs, inputFact)
		run, err := service.Start(ctx.Context, workflow, input, opts)
		if err != nil {
			return ActionResult{}, workflowFailure(err)
		}
		return acknowledgement(spec, workflowRunView(run)), nil
	}), nil
}

func buildWorkflowSignal(build BuildContext, spec NodeSpec) (Action, error) {
	service, err := requireResource[WorkflowService](build, spec, "a workflow resource (e.g. workflow.http)")
	if err != nil {
		return nil, err
	}
	runFact, err := requiredString(spec.Config, "run_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	signal, err := configTemplate(spec.Config, "signal", "")
	if err != nil || signal == nil {
		return nil, fmt.Errorf("node %q: workflow.signal needs config.signal", spec.Name)
	}
	payloadFact := configString(spec.Config, "payload_fact", "")
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		runID, err := factString(ctx.Inputs, runFact)
		if err != nil {
			return ActionResult{}, invalidInput("%s", err.Error())
		}
		name, err := signal.Render(actionEnv(ctx))
		if err != nil {
			return ActionResult{}, err
		}
		var payload any
		if payloadFact != "" {
			payload, _ = resolvePath(ctx.Inputs, payloadFact)
		}
		if err := service.Signal(ctx.Context, runID, name, payload); err != nil {
			return ActionResult{}, workflowFailure(err)
		}
		return acknowledgement(spec, map[string]any{"run_id": runID, "signal": name, "delivered": true}), nil
	}), nil
}

func buildWorkflowStatus(build BuildContext, spec NodeSpec) (Action, error) {
	service, err := requireResource[WorkflowService](build, spec, "a workflow resource (e.g. workflow.http)")
	if err != nil {
		return nil, err
	}
	runFact, err := requiredString(spec.Config, "run_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		runID, err := factString(ctx.Inputs, runFact)
		if err != nil {
			return ActionResult{}, invalidInput("%s", err.Error())
		}
		run, err := service.Status(ctx.Context, runID)
		if err != nil {
			return ActionResult{}, workflowFailure(err)
		}
		return singleOutput(spec, workflowRunView(run)), nil
	}), nil
}

// workflowFailure keeps a platform failure a WorkflowService already produced,
// maps an orchestrator HTTP status onto a category, and treats anything else as
// an orchestrator outage.
func workflowFailure(err error) error {
	var failure intent.Failure
	if errors.As(err, &failure) {
		return err
	}
	var callErr *httpCallError
	if errors.As(err, &callErr) && callErr.status > 0 {
		return upstreamFailure(err, callErr.status)
	}
	return unavailable("the workflow orchestrator call failed: %v", err)
}

// httpWorkflow implements WorkflowService over REST.
type httpWorkflow struct {
	service                           *HTTPService
	startPath, signalPath, statusPath string
}

func openHTTPWorkflow(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("workflow.http", spec.Config, "service", "start_path", "signal_path", "status_path"); err != nil {
		return nil, nil, err
	}
	name, err := requiredString(spec.Config, "service")
	if err != nil {
		return nil, nil, fmt.Errorf("workflow.http %q: %w", spec.Name, err)
	}
	resolved, ok := spec.resolved[name]
	if !ok {
		return nil, nil, fmt.Errorf("workflow.http %q: config.service names unknown resource %q", spec.Name, name)
	}
	service, ok := resolved.(*HTTPService)
	if !ok {
		return nil, nil, fmt.Errorf("workflow.http %q: resource %q is not a service.http resource", spec.Name, name)
	}
	return &httpWorkflow{
		service:    service,
		startPath:  configString(spec.Config, "start_path", "/workflows/{workflow}/runs"),
		signalPath: configString(spec.Config, "signal_path", "/runs/{run_id}/signals/{signal}"),
		statusPath: configString(spec.Config, "status_path", "/runs/{run_id}"),
	}, nil, nil
}

func expandPath(pattern string, values map[string]string) string {
	for key, value := range values {
		pattern = strings.ReplaceAll(pattern, "{"+key+"}", url.PathEscape(value))
	}
	return pattern
}

func (w *httpWorkflow) call(ctx context.Context, method, path string, body any, headers map[string]string) (HTTPResponse, error) {
	request := HTTPRequest{Method: method, URL: path, Headers: headers}
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return HTTPResponse{}, err
		}
		request.Body = encoded
	}
	return w.service.Do(ctx, request)
}

func decodeRun(response HTTPResponse, fallbackWorkflow string) WorkflowRun {
	body, _ := response.JSON.(map[string]any)
	run := WorkflowRun{ID: firstString(body, "id", "run_id"), Workflow: firstString(body, "workflow", "name"), Status: Stringify(body["status"]), Result: firstValue(body, "result", "output")}
	if v, ok := ToFloat(body["version"]); ok {
		run.Version = int64(v)
	}
	if run.Workflow == "" {
		run.Workflow = fallbackWorkflow
	}
	return run
}

// Start implements WorkflowService.
func (w *httpWorkflow) Start(ctx context.Context, workflow string, input any, opts WorkflowStartOptions) (WorkflowRun, error) {
	headers := map[string]string{"Content-Type": "application/json"}
	if opts.IdempotencyKey != "" {
		headers["Idempotency-Key"] = opts.IdempotencyKey
	}
	if opts.TenantID != "" {
		headers["X-Tenant-ID"] = opts.TenantID
	}
	response, err := w.call(ctx, http.MethodPost, expandPath(w.startPath, map[string]string{"workflow": workflow}), map[string]any{"input": input}, headers)
	if err != nil {
		return WorkflowRun{}, err
	}
	run := decodeRun(response, workflow)
	if run.ID == "" {
		return WorkflowRun{}, fmt.Errorf("the orchestrator did not return a run id")
	}
	return run, nil
}

// Signal implements WorkflowService.
func (w *httpWorkflow) Signal(ctx context.Context, runID, signal string, payload any) error {
	_, err := w.call(ctx, http.MethodPost, expandPath(w.signalPath, map[string]string{"run_id": runID, "signal": signal}),
		map[string]any{"payload": payload}, map[string]string{"Content-Type": "application/json"})
	return err
}

// Status implements WorkflowService.
func (w *httpWorkflow) Status(ctx context.Context, runID string) (WorkflowRun, error) {
	response, err := w.call(ctx, http.MethodGet, expandPath(w.statusPath, map[string]string{"run_id": runID}), nil, nil)
	if err != nil {
		return WorkflowRun{}, err
	}
	run := decodeRun(response, "")
	if run.ID == "" {
		run.ID = runID
	}
	return run, nil
}

var _ WorkflowService = (*httpWorkflow)(nil)
