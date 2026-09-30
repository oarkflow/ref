import { describe, expect, it } from "vitest";
import { FOCUS, FULL } from "./fixtures";
import { DEFAULT_FILTERS, buildJourney } from "./model";
import { traceStart } from "./JourneysPage";

describe("where 'Follow the user' starts", () => {
  const whole = buildJourney(FULL, { ...DEFAULT_FILTERS, showOrphans: true });
  const row = whole.byId.get("page:pages/todos/new")!.rows[0]!.id;
  it("uses the selected page, or the page of the selected button", () => {
    expect(traceStart(whole, "page:pages/todos/list", undefined)).toBe("page:pages/todos/list");
    expect(traceStart(whole, row, undefined)).toBe("page:pages/todos/new");
  });
  it("falls back to the focused page", () => {
    const f = buildJourney(FOCUS, DEFAULT_FILTERS);
    expect(traceStart(f, null, "page:pages/todos/new")).toBe("page:pages/todos/new");
    expect(traceStart(f, "route:web.todos_create", "page:pages/todos/new")).toBe("page:pages/todos/new");
  });
  it("starts nowhere in particular otherwise", () => {
    expect(traceStart(whole, null, undefined)).toBeNull();
    expect(traceStart(whole, "intent:todo.create", "route:web.todos_create")).toBeNull();
  });
});
