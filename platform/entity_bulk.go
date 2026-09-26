package platform

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/oarkflow/ref/intent"
)

// Bulk entity operations. An entity declared `bulk true` accepts
//
//	POST {path}/-/bulk
//	{"create": [{...}], "update": [{"id": "...", "version": 3, ...}], "delete": ["id", ...], "atomic": true}
//
// Every item is validated and access-checked exactly as the single create,
// update or delete it stands for. Atomic (the default) is all or nothing: the
// items are prepared and applied in one transaction, with their search tokens
// and durable hook events, and any failure rolls the whole batch back and
// reports every failed item. With atomic false each item commits on its own
// and the response reports each outcome.

// bulkItem is one create, update or delete of a bulk request.
type bulkItem struct {
	op     string // create, update or delete
	index  int    // position in its list
	id     string
	body   map[string]any
	ch     entityChange
	record map[string]any
	err    error
}

func (rt *entityRuntime) bulk(ctx *ActionContext) (any, error) {
	body, err := rt.body(ctx)
	if err != nil {
		return nil, err
	}
	items, atomic, err := rt.bulkItems(body)
	if err != nil {
		return nil, err
	}
	// The op-level rules (roles) hold for the whole request; row conditions
	// are checked per record as each item is prepared.
	for _, op := range []string{"create", "update", "delete"} {
		if slices.ContainsFunc(items, func(it bulkItem) bool { return it.op == op }) {
			if err := rt.allowed(ctx, op, nil); err != nil {
				return nil, err
			}
		}
	}
	if atomic {
		err = rt.bulkAtomic(ctx, items)
	} else {
		rt.bulkEach(ctx, items)
	}
	if err != nil {
		return nil, err
	}
	return bulkReport(items, atomic), nil
}

// bulkItems reads a bulk request: creates, then updates, then deletes.
func (rt *entityRuntime) bulkItems(body map[string]any) ([]bulkItem, bool, error) {
	atomic := true
	var items []bulkItem
	for key, raw := range body {
		switch key {
		case "atomic":
			b, ok := raw.(bool)
			if !ok {
				return nil, false, invalidInput("atomic must be true or false")
			}
			atomic = b
		case "create", "update", "delete":
			if _, ok := raw.([]any); !ok && raw != nil {
				return nil, false, invalidInput("%s must be a list", key)
			}
		default:
			return nil, false, invalidInput("unknown bulk field %q (use create, update, delete and atomic)", key)
		}
	}
	list := func(key string) []any { l, _ := body[key].([]any); return l }
	for i, raw := range list("create") {
		rec, ok := raw.(map[string]any)
		if !ok {
			return nil, false, invalidInput("create[%d] must be an object", i)
		}
		items = append(items, bulkItem{op: "create", index: i, body: rec})
	}
	seen := map[string]string{}
	claim := func(op string, i int, id string) error {
		if id == "" {
			return invalidInput("%s[%d] needs an id", op, i)
		}
		if prior, dup := seen[id]; dup {
			return invalidInput("%s[%d]: record %s is already in the request (%s)", op, i, id, prior)
		}
		seen[id] = fmt.Sprintf("%s[%d]", op, i)
		return nil
	}
	for i, raw := range list("update") {
		rec, ok := raw.(map[string]any)
		if !ok {
			return nil, false, invalidInput("update[%d] must be an object with an id", i)
		}
		id, _ := rec["id"].(string)
		if err := claim("update", i, id); err != nil {
			return nil, false, err
		}
		changes := make(map[string]any, len(rec))
		for k, v := range rec {
			if k != "id" {
				changes[k] = v
			}
		}
		items = append(items, bulkItem{op: "update", index: i, id: id, body: changes})
	}
	for i, raw := range list("delete") {
		id, _ := raw.(string)
		if m, ok := raw.(map[string]any); ok {
			id, _ = m["id"].(string)
		}
		if err := claim("delete", i, id); err != nil {
			return nil, false, err
		}
		items = append(items, bulkItem{op: "delete", index: i, id: id})
	}
	if len(items) == 0 {
		return nil, false, invalidInput("a bulk request needs create, update or delete items")
	}
	if len(items) > rt.plan.spec.BulkMax {
		return nil, false, invalidInput("a bulk request takes at most %d items (got %d)", rt.plan.spec.BulkMax, len(items))
	}
	return items, atomic, nil
}

// prepareItem validates and access-checks one item, reading records
// through q.
func (rt *entityRuntime) prepareItem(ctx *ActionContext, q execer, it *bulkItem) {
	switch it.op {
	case "create":
		it.ch, it.err = rt.prepareCreate(ctx, it.body)
		it.id = it.ch.id
	case "update":
		it.ch, it.err = rt.prepareUpdate(ctx, q, it.id, it.body)
	case "delete":
		it.ch, it.err = rt.prepareDelete(ctx, q, it.id)
	}
}

// bulkAtomic prepares every item, then applies them all, in one
// transaction. Any failure rolls back everything and is reported with the
// items that failed.
func (rt *entityRuntime) bulkAtomic(ctx *ActionContext, items []bulkItem) error {
	err := rt.transact(ctx, true, func(q execer) ([]EntityEvent, error) {
		failed := false
		for i := range items {
			rt.prepareItem(ctx, q, &items[i])
			failed = failed || items[i].err != nil
		}
		if failed {
			return nil, bulkRejected(items)
		}
		var events []EntityEvent
		for i := range items {
			it := &items[i]
			record, more, err := rt.apply(ctx, q, it.ch)
			if err != nil {
				// A failed statement aborts a PostgreSQL transaction, so the
				// rest are not tried.
				it.err = err
				return nil, bulkRejected(items)
			}
			it.record = record
			events = append(events, more...)
		}
		return events, nil
	})
	if err != nil {
		return err
	}
	for _, it := range items {
		rt.fire(ctx, it.ch.event, it.record, it.ch.prev)
	}
	return nil
}

// bulkEach commits every item on its own, as the single operation would.
func (rt *entityRuntime) bulkEach(ctx *ActionContext, items []bulkItem) {
	for i := range items {
		it := &items[i]
		if rt.prepareItem(ctx, rt.db.Reader(), it); it.err == nil {
			it.record, it.err = rt.commitOne(ctx, it.ch)
		}
		if status, _ := failureView(it.err); it.err != nil && status >= 500 {
			slog.Warn("entity bulk item failed", "entity", rt.plan.spec.Name, "op", it.op, "index", it.index, "error", it.err)
		}
	}
}

// bulkItemError is how one failed item is reported: its position and the
// error body a single request would have returned.
func bulkItemError(it bulkItem) map[string]any {
	status, body := failureView(it.err)
	out := map[string]any{"op": it.op, "index": it.index, "status": status, "code": body["code"], "message": body["message"]}
	if it.id != "" {
		out["id"] = it.id
	}
	if d, ok := body["details"]; ok {
		out["details"] = d
	}
	return out
}

// bulkRejected is the failure of an atomic batch: the category of its first
// failed item, and every failed item in details.
func bulkRejected(items []bulkItem) error {
	var (
		details []any
		first   *bulkItem
	)
	for i := range items {
		if items[i].err == nil {
			continue
		}
		if first == nil {
			first = &items[i]
		}
		details = append(details, bulkItemError(items[i]))
	}
	status, body := failureView(first.err)
	if status >= 500 {
		return first.err // not the client's to fix: keep it a server error
	}
	category := intent.CategoryInvalidInput
	var failure intent.Failure
	if errors.As(first.err, &failure) {
		category = failure.Category
	}
	return intent.Failure{Code: "BULK_REJECTED", Category: category, Meta: map[string]any{"details": details},
		Message: fmt.Sprintf("%d of %d items failed and nothing was applied; %s[%d]: %s", len(details), len(items), first.op, first.index, body["message"])}
}

// bulkReport is a bulk response: counts and one result per item, in request
// order.
func bulkReport(items []bulkItem, atomic bool) map[string]any {
	counts := map[string]int{}
	results := make([]any, len(items))
	for i, it := range items {
		if it.err != nil {
			counts["failed"]++
			r := bulkItemError(it)
			r["ok"] = false
			results[i] = r
			continue
		}
		counts[it.op]++
		r := map[string]any{"op": it.op, "index": it.index, "id": it.id, "ok": true}
		if it.op != "delete" {
			r["record"] = it.record
		}
		results[i] = r
	}
	return map[string]any{"atomic": atomic, "created": counts["create"], "updated": counts["update"],
		"deleted": counts["delete"], "failed": counts["failed"], "results": results}
}
