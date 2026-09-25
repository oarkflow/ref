package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/oarkflow/ref/runtime"
)

// Incremental streaming for `mode "stream"` routes.
//
// A node running under a stream route can push server-sent events while the
// rest of the intent is still executing — progress for a long import, partial
// LLM output, each row of a report as it is computed. The final result still
// arrives as the closing `event: result`.
//
// The emitter is bounded: when the client reads slower than the intent
// produces, stream.emit blocks until there is room or the request is
// cancelled. A client that disconnects cancels the dispatch context, so no
// producer can block forever and no goroutine outlives the request.
//
// Outside a stream route stream.emit is a no-op, so one intent serves JSON,
// queue and stream transports unchanged.

type streamEmitterKey struct{}

// streamEmitter carries SSE frames from nodes to the response writer.
type streamEmitter struct {
	events chan []byte
}

const streamBuffer = 64

func withStreamEmitter(ctx context.Context, e *streamEmitter) context.Context {
	return context.WithValue(ctx, streamEmitterKey{}, e)
}

func streamEmitterFrom(ctx context.Context) (*streamEmitter, bool) {
	e, ok := ctx.Value(streamEmitterKey{}).(*streamEmitter)
	return e, ok && e != nil
}

// emit queues one SSE frame, waiting for room or cancellation.
func (e *streamEmitter) emit(ctx context.Context, frame []byte) error {
	select {
	case e.events <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func sseFrame(event string, data []byte) []byte {
	var b bytes.Buffer
	if event != "" {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteByte('\n')
	}
	// A data field cannot contain a newline; each line becomes its own data:
	// field, which SSE clients re-join.
	for _, line := range strings.Split(string(data), "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.Bytes()
}

var sseEventName = func(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func registerStreamActions(r *Registry) {
	mustAction(r, "stream.emit", ActionFactoryFunc(buildStreamEmit), ActionInfo{
		Family:   "messaging",
		Summary:  "Push a server-sent event to the client of a stream route while the intent keeps running (no-op elsewhere)",
		Provides: "An object with emitted (false outside a stream route)",
		Kind:     "effect",
		Config: []ConfigField{
			{Name: "event", Type: "string", Default: "message", Summary: "SSE event name"},
			{Name: "data_fact", Type: "fact", Summary: "Fact sent as JSON"},
			{Name: "data", Type: "template", Summary: "Text sent when data_fact is not set"},
		},
	})
}

func buildStreamEmit(_ BuildContext, spec NodeSpec) (Action, error) {
	event := configString(spec.Config, "event", "message")
	if !sseEventName(event) {
		return nil, fmt.Errorf("node %q: event must be 1-64 characters of [A-Za-z0-9_.-]", spec.Name)
	}
	dataFact := configString(spec.Config, "data_fact", "")
	text, err := configTemplate(spec.Config, "data", "")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if dataFact == "" && text == nil {
		return nil, fmt.Errorf("node %q: stream.emit needs config.data_fact or config.data", spec.Name)
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		emitter, ok := streamEmitterFrom(ctx.Context)
		if !ok {
			return acknowledgement(spec, map[string]any{"emitted": false}), nil
		}
		var payload []byte
		if dataFact != "" {
			value, _ := resolvePath(ctx.Inputs, dataFact)
			encoded, err := json.Marshal(value)
			if err != nil {
				return ActionResult{}, invalidInput("the stream event cannot be serialised: %v", err)
			}
			payload = encoded
		} else {
			rendered, err := text.Render(actionEnv(ctx))
			if err != nil {
				return ActionResult{}, err
			}
			payload = []byte(rendered)
		}
		if err := emitter.emit(ctx.Context, sseFrame(event, payload)); err != nil {
			return ActionResult{}, err
		}
		return acknowledgement(spec, map[string]any{"emitted": true}), nil
	}), nil
}

// dispatchOutcome is a finished dispatch handed from the producer goroutine.
type dispatchOutcome struct {
	result *runtime.DispatchResult
	err    error
}

// sseReader turns emitted frames plus the final outcome into the response
// body. It is read by fh on the request goroutine.
type sseReader struct {
	first    []byte
	events   chan []byte
	done     <-chan dispatchOutcome
	finished bool
	pending  bytes.Buffer
	body     bytes.Buffer // everything written, for idempotent replay
	onFinal  func(dispatchOutcome) []byte
	final    *dispatchOutcome
	mu       sync.Mutex
}

func (r *sseReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.pending.Len() == 0 {
		if r.first != nil {
			r.write(r.first)
			r.first = nil
			continue
		}
		if r.finished {
			return 0, io.EOF
		}
		select {
		case frame := <-r.events:
			r.write(frame)
		case out := <-r.done:
			// Drain whatever was emitted before the intent finished, in order,
			// then close with the result.
			for drained := false; !drained; {
				select {
				case frame := <-r.events:
					r.write(frame)
				default:
					drained = true
				}
			}
			r.final = &out
			r.write(r.onFinal(out))
			r.finished = true
		}
	}
	return r.pending.Read(p)
}

func (r *sseReader) write(frame []byte) {
	r.pending.Write(frame)
	r.body.Write(frame)
}
