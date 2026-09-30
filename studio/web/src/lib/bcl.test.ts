import { describe, expect, it } from "vitest";
import {
  detectMode, emitEnv, emitList, emitLiteral, isDuration, parseEnv, parseList, parseLiteral, quote, scalarKindOf, unquote,
} from "./bcl";

describe("quote/unquote", () => {
  it("escapes quotes, backslashes, newlines and tabs", () => {
    expect(quote('say "hi"')).toBe('"say \\"hi\\""');
    expect(quote("a\\b")).toBe('"a\\\\b"');
    expect(quote("a\nb\tc")).toBe('"a\\nb\\tc"');
  });
  it("round-trips", () => {
    for (const s of ["", "plain", 'q"uote', "back\\slash", "line1\nline2", "tab\t", "unicode ✓"]) {
      expect(unquote(quote(s))).toBe(s);
    }
  });
  it("uses a raw string for control characters", () => {
    expect(quote("a\rb")).toBe("`a\rb`");
    expect(() => quote("a\r`b")).toThrow();
  });
  it("rejects non-literals", () => {
    expect(unquote('env("X")')).toBeNull();
    expect(unquote('"a" "b"')).toBeNull();
    expect(unquote("bare")).toBeNull();
  });
});

describe("env()", () => {
  it("emits with and without default", () => {
    expect(emitEnv("PORT", "8080")).toBe('env("PORT","8080")');
    expect(emitEnv("PORT")).toBe('env("PORT")');
    expect(emitEnv("PORT", "")).toBe('env("PORT")');
  });
  it("parses what it emits, and hand-written spacing", () => {
    expect(parseEnv('env("PORT","8080")')).toEqual({ name: "PORT", def: "8080" });
    expect(parseEnv('env( "DB_DSN" , "file:x.db?a=1,b=2" )')).toEqual({ name: "DB_DSN", def: "file:x.db?a=1,b=2" });
    expect(parseEnv('env("ONLY")')).toEqual({ name: "ONLY" });
  });
  it("leaves other calls alone", () => {
    expect(parseEnv("env(NAME)")).toBeNull();
    expect(parseEnv('upper(env("X"))')).toBeNull();
    expect(parseEnv('env("X", 5)')).toBeNull();
  });
});

describe("lists", () => {
  it("parses raw items at the top level only", () => {
    expect(parseList('["user", "admin"]')).toEqual(['"user"', '"admin"']);
    expect(parseList("[]")).toEqual([]);
    expect(parseList('[env("A","b,c"), "x"]')).toEqual(['env("A","b,c")', '"x"']);
    expect(parseList('["a,b", "c"]')).toEqual(['"a,b"', '"c"']);
    expect(parseList("[\n  \"a\",\n  \"b\"\n]")).toEqual(['"a"', '"b"']);
  });
  it("refuses what it cannot rewrite safely", () => {
    expect(parseList("roles")).toBeNull();
    expect(parseList('["a" # note\n, "b"]')).toBeNull();
    expect(parseList('["a" "b"]')).toBeNull();
  });
  it("emits", () => {
    expect(emitList(['"a"', '"b"'])).toBe('["a", "b"]');
    expect(emitList([])).toBe("[]");
  });
});

describe("literals", () => {
  it("validates durations", () => {
    for (const d of ["30s", "5m", "1h30m", "500ms", "1.5h"]) expect(isDuration(d)).toBe(true);
    for (const d of ["30", "m", "5 m", "abc", ""]) expect(isDuration(d)).toBe(false);
    expect(emitLiteral("30m", "duration")).toEqual({ raw: "30m" });
    expect(emitLiteral("30", "duration").error).toBeTruthy();
  });
  it("emits each kind", () => {
    expect(emitLiteral("hello", "string")).toEqual({ raw: '"hello"' });
    expect(emitLiteral("POST", "ident")).toEqual({ raw: "POST" });
    expect(emitLiteral("has space", "ident").error).toBeTruthy();
    expect(emitLiteral("42", "int")).toEqual({ raw: "42" });
    expect(emitLiteral("4.2", "int").error).toBeTruthy();
    expect(emitLiteral("4.2", "number")).toEqual({ raw: "4.2" });
    expect(emitLiteral("true", "bool")).toEqual({ raw: "true" });
    expect(emitLiteral("7", "any")).toEqual({ raw: "7" });
    expect(emitLiteral("seven", "any")).toEqual({ raw: '"seven"' });
  });
  it("reads them back", () => {
    expect(parseLiteral('"a b"', "string")).toBe("a b");
    expect(parseLiteral("GET", "ident")).toBe("GET");
    expect(parseLiteral("30m", "duration")).toBe("30m");
    expect(parseLiteral('env("X")', "string")).toBeNull();
    expect(parseLiteral("8080", "string")).toBeNull();
  });
});

describe("detectMode", () => {
  it("infers literal, env and expression", () => {
    expect(detectMode(undefined, "string")).toBe("literal");
    expect(detectMode('"x"', "string")).toBe("literal");
    expect(detectMode('env("X","y")', "string")).toBe("env");
    expect(detectMode("input.name + 1", "string")).toBe("expr");
    expect(detectMode("30m", "duration")).toBe("literal");
    expect(detectMode('env("T","30m")', "duration")).toBe("env");
  });
});

describe("scalarKindOf", () => {
  it("maps schema and catalog type names", () => {
    expect(scalarKindOf("string")).toBe("string");
    expect(scalarKindOf("int64")).toBe("int");
    expect(scalarKindOf("duration")).toBe("duration");
    expect(scalarKindOf("bool")).toBe("bool");
    expect(scalarKindOf("object")).toBeNull();
    expect(scalarKindOf("[]string")).toBeNull();
  });
});
