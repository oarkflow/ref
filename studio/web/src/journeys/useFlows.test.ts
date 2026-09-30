import { describe, expect, it } from "vitest";
import { focusHref, parseDepth, serverDepth } from "./useFlows";

describe("reach", () => {
  it("counts a page's buttons as inside it", () => {
    expect(serverDepth("page:pages/todos/new", 2)).toBe(3);
    expect(serverDepth("route:web.todos_create", 2)).toBe(2);
    expect(serverDepth(undefined, 2)).toBe(2);
  });
  it("reads 1 to 3 from the address, else 2", () => {
    expect(parseDepth("1")).toBe(1);
    expect(parseDepth("3")).toBe(3);
    expect(parseDepth("4")).toBe(2);
    expect(parseDepth("x")).toBe(2);
    expect(parseDepth(null)).toBe(2);
  });
  it("builds the address of a focused view", () => {
    expect(focusHref("page:pages/todos/new", 3)).toBe("/journeys?focus=page%3Apages%2Ftodos%2Fnew&depth=3");
    expect(focusHref("intent:todo.create")).toBe("/journeys?focus=intent%3Atodo.create&depth=2");
  });
});
