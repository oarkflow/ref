import { describe, expect, it } from "vitest";
import { shortcutFor } from "./Workspace";

const key = (key: string, o: Partial<KeyboardEvent> = {}, tag = "BODY") =>
  ({ key, ctrlKey: false, metaKey: false, shiftKey: false, target: { tagName: tag, isContentEditable: false } as unknown as EventTarget, ...o });

describe("keyboard shortcuts", () => {
  it("maps undo, redo and validate", () => {
    expect(shortcutFor(key("z", { ctrlKey: true }))).toBe("undo");
    expect(shortcutFor(key("z", { metaKey: true }))).toBe("undo");
    expect(shortcutFor(key("Z", { ctrlKey: true, shiftKey: true }))).toBe("redo");
    expect(shortcutFor(key("y", { ctrlKey: true }))).toBe("redo");
    expect(shortcutFor(key("s", { ctrlKey: true }))).toBe("validate");
  });
  it("leaves undo/redo to text inputs, but always takes Ctrl+S", () => {
    expect(shortcutFor(key("z", { ctrlKey: true }, "INPUT"))).toBeNull();
    expect(shortcutFor(key("y", { ctrlKey: true }, "TEXTAREA"))).toBeNull();
    expect(shortcutFor(key("s", { ctrlKey: true }, "INPUT"))).toBe("validate");
  });
  it("ignores unmodified keys", () => {
    expect(shortcutFor(key("z"))).toBeNull();
  });
});
