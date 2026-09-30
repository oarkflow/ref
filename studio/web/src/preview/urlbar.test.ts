import { describe, expect, it } from "vitest";
import { appPathOf, confinePreviewUrl } from "./urlbar";

const ORIGIN = "http://studio.test:5173";
const PREFIX = "/studio/preview/dr_1/";
const c = (input: string) => confinePreviewUrl(input, PREFIX, ORIGIN);

describe("confinePreviewUrl", () => {
  it("treats a path as a path of the previewed app", () => {
    expect(c("/todos")).toEqual({ href: `${ORIGIN}${PREFIX}todos`, appPath: "/todos" });
    expect(c("todos/1?x=2#top")).toEqual({ href: `${ORIGIN}${PREFIX}todos/1?x=2#top`, appPath: "/todos/1?x=2#top" });
    expect(c("")?.appPath).toBe("/");
    expect(c("/")?.href).toBe(`${ORIGIN}${PREFIX}`);
  });

  it("accepts addresses that are already inside the prefix without doubling it", () => {
    expect(c(`${PREFIX}todos`)?.href).toBe(`${ORIGIN}${PREFIX}todos`);
    expect(c(`${ORIGIN}${PREFIX}todos?a=1`)?.appPath).toBe("/todos?a=1");
    expect(c("/studio/preview/dr_1")?.href).toBe(`${ORIGIN}${PREFIX}`);
  });

  it("resolves query and hash against the preview root", () => {
    expect(c("?page=2")?.appPath).toBe("/?page=2");
    expect(c("#top")?.appPath).toBe("/#top");
  });

  it("refuses anything that would leave the prefix", () => {
    expect(c("https://evil.example/x")).toBeNull();
    expect(c(`http://other.test:5173${PREFIX}x`)).toBeNull();
    expect(c("//evil.example/x")).toBeNull();
    expect(c("javascript:alert(1)")).toBeNull();
    expect(c("data:text/html,hi")).toBeNull();
    expect(c("/../../api/v1/meta")).toBeNull();
    expect(c("../../../api")).toBeNull();
    expect(c("/a/../../../../etc/passwd")).toBeNull();
  });

  it("refuses encoded traversal, which servers decode but URL() does not", () => {
    expect(c("/%2e%2e/%2e%2e/api")).toBeNull();
    expect(c("/a%2f..%2f..%2fb")).toBeNull();
  });

  it("allows .. that stays inside", () => {
    expect(c("/a/b/../c")?.appPath).toBe("/a/c");
  });

  it("works with a relative prefix URL", () => {
    expect(confinePreviewUrl("/x", "/preview/d/", ORIGIN)?.href).toBe(`${ORIGIN}/preview/d/x`);
  });
});

describe("appPathOf", () => {
  it("maps a frame location to an app path, or null outside the preview", () => {
    expect(appPathOf(`${ORIGIN}${PREFIX}todos?q=1`, PREFIX, ORIGIN)).toBe("/todos?q=1");
    expect(appPathOf(`${ORIGIN}/studio/`, PREFIX, ORIGIN)).toBeNull();
    expect(appPathOf("http://elsewhere.test/", PREFIX, ORIGIN)).toBeNull();
  });
});
