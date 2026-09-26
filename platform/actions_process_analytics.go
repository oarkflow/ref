package platform

import (
	"fmt"
	"sort"
	"time"

	"github.com/oarkflow/ref/process"
)

// process.analytics reports on durable runs for an operations view: exact
// run counts by status (one COUNT per status), where the active runs are
// parked or queued, how long finished runs took, and per-step executions and
// durations. The durations come from the newest runs only (max_runs, and
// step_runs for the step histories, which cost a read per run), so the report
// stays cheap on a store with millions of runs; `scanned` and `truncated` say
// how much it saw.

func registerProcessAnalyticsAction(r *Registry) {
	mustAction(r, "process.analytics", processAnalyticsAction, ActionInfo{
		Family:   "process",
		Summary:  "Report run counts by status, active runs by step, and average run and step durations",
		Provides: "{by_status, active_by_step, durations, steps, scanned, truncated}",
		Kind:     "read",
		Config: []ConfigField{
			{Name: "process", Type: "process", Summary: "Which process; omit for every process of the engine"},
			{Name: "max_runs", Type: "int", Default: "1000", Summary: "Newest runs read for active steps and run durations"},
			{Name: "step_runs", Type: "int", Default: "200", Summary: "Newest runs whose step history is read for step figures; 0 turns them off"},
		},
	})
}

// durationStat is a count and an average, in seconds.
type durationStat struct {
	n     int
	total float64
}

func (d *durationStat) add(dur time.Duration) {
	d.n++
	d.total += dur.Seconds()
}

func (d durationStat) view() map[string]any {
	out := map[string]any{"count": d.n, "avg_seconds": 0.0}
	if d.n > 0 {
		out["avg_seconds"] = d.total / float64(d.n)
	}
	return out
}

var processAnalyticsAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	engine, err := resolveEngine(build, spec)
	if err != nil {
		return nil, err
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("process.analytics", spec.Config, "process", "max_runs", "step_runs"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	maxRuns, err := configInt(spec.Config, "max_runs", 1000)
	if err != nil || maxRuns < 1 {
		return nil, fmt.Errorf("node %q: max_runs must be a positive integer", spec.Name)
	}
	stepRuns, err := configInt(spec.Config, "step_runs", 200)
	if err != nil || stepRuns < 0 || stepRuns > maxRuns {
		return nil, fmt.Errorf("node %q: step_runs must be between 0 and max_runs", spec.Name)
	}
	name := configString(spec.Config, "process", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		store := engine.Store()
		// A tenant-scoped caller only ever sees its own runs.
		base := process.RunFilter{Process: name, TenantID: ctx.TenantID}
		byStatus, total := map[string]any{}, 0
		for _, status := range knownProcessStatuses {
			f := base
			f.Status = process.Status(status)
			n, err := store.CountRuns(ctx.Context, f)
			if err != nil {
				return ActionResult{}, processFailure(err)
			}
			byStatus[status] = n
			total += n
		}

		activeByStep := map[string]int{}
		durations := map[string]*durationStat{}
		type stepStat struct {
			executions, completed, failed int
			dur                           durationStat
		}
		steps := map[string]*stepStat{}
		scanned := 0
		for scanned < maxRuns {
			f := base
			f.Limit, f.Offset = min(200, maxRuns-scanned), scanned
			runs, err := store.ListRuns(ctx.Context, f)
			if err != nil {
				return ActionResult{}, processFailure(err)
			}
			for _, run := range runs {
				scanned++
				switch {
				case run.Status.Terminal():
					if run.CompletedAt != nil {
						start := run.CreatedAt
						if run.StartedAt != nil {
							start = *run.StartedAt
						}
						if durations[string(run.Status)] == nil {
							durations[string(run.Status)] = &durationStat{}
						}
						durations[string(run.Status)].add(run.CompletedAt.Sub(start))
					}
				case run.Waiting != nil && run.Waiting.Step != "":
					activeByStep[run.Waiting.Step]++
				case len(run.Frames) > 0:
					activeByStep[run.Frames[0].Step]++
				}
				if scanned > stepRuns {
					continue
				}
				states, err := store.ListSteps(ctx.Context, run.ID)
				if err != nil {
					return ActionResult{}, processFailure(err)
				}
				for _, st := range states {
					s := steps[st.Step]
					if s == nil {
						s = &stepStat{}
						steps[st.Step] = s
					}
					s.executions++
					switch st.Status {
					case process.StepCompleted:
						s.completed++
					case process.StepFailed:
						s.failed++
					}
					if st.FinishedAt != nil {
						s.dur.add(st.FinishedAt.Sub(st.StartedAt))
					}
				}
			}
			if len(runs) < f.Limit {
				break
			}
		}

		durationView := map[string]any{}
		for status, d := range durations {
			durationView[status] = d.view()
		}
		names := make([]string, 0, len(steps))
		for step := range steps {
			names = append(names, step)
		}
		sort.Strings(names)
		stepView := make([]any, 0, len(names))
		for _, step := range names {
			s := steps[step]
			view := s.dur.view()
			view["step"], view["executions"], view["completed"], view["failed"] = step, s.executions, s.completed, s.failed
			delete(view, "count")
			stepView = append(stepView, view)
		}
		return singleOutput(spec, map[string]any{
			"process": name, "runs": total, "by_status": byStatus, "active_by_step": activeByStep,
			"durations": durationView, "steps": stepView, "scanned": scanned, "truncated": scanned < total,
		}), nil
	}), nil
})
