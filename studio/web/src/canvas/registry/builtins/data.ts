import type { NodeTypeDef } from "../types";
import { cfg, define, f, pick, section } from "./dsl";

const DB = ["database."];

const QUERY: Pick<NodeTypeDef, "icon" | "category" | "sections" | "summaryRows"> = {
  icon: "database", category: "data",
  sections: [
    section("query", "The query", [
      f.resource("resource", "Database", { kinds: DB, required: true }),
      f.code("statement", "SQL", { required: true, language: "sql", rows: 5, help: "Use $1, $2… for values, filled from the list below." }),
      f.facts("args", "Fill $1, $2… with", { help: "In order." }),
      f.int("max_rows", "At most this many rows", { placeholder: "e.g. 100" }),
      f.bool("require_rows", "Fail when nothing comes back"),
      f.text("not_found_message", "If nothing comes back, say", { visibleWhen: (v) => v.raw("config.require_rows") === "true", placeholder: "Not found." }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Query", cfg("statement"), { code: true, max: 34 }], ["On", ["resource"], { code: true }]),
};

define(["database"], { ...QUERY, label: "Database query", blurb: "Runs a SQL statement and publishes the rows it returns." });
define(["db"], { ...QUERY, label: "Quick database query", blurb: "The short way to run a SQL statement and use the rows it returns." });

define(["crud"], {
  label: "Save or load a record", icon: "database", category: "data",
  blurb: "Reads or changes rows of one table, following the rules you set.",
  sections: [
    section("table", "The table", [
      f.resource("resource", "Database", { kinds: DB, required: true }),
      f.select("operation", "What to do", ["list", "get", "create", "update", "delete", "upsert", "count"].map((v) => ({ value: v, label: v })), { required: true, bare: false }),
      f.text("table", "Table", { required: true, placeholder: "e.g. todos" }),
      f.strs("columns", "Columns it may read", { required: true }),
      f.strs("writable", "Columns it may change", { visibleWhen: (v) => v.text("config.operation") !== "list" && v.text("config.operation") !== "get" }),
    ]),
    section("scope", "Who sees which rows", [
      f.text("id_column", "Row id column", { placeholder: "id" }),
      f.text("tenant_column", "Belongs to a customer (column)", { help: "Rows are limited to the caller's customer." }),
      f.text("owner_column", "Belongs to a person (column)", { help: "Rows are limited to the caller." }),
      f.text("soft_delete_column", "Marks a deleted row (column)", { help: "Deleting sets this instead of removing the row." }),
    ], { open: false }),
  ],
  summaryRows: (s) => pick(s, ["Does", cfg("operation")], ["Table", cfg("table"), { code: true }]),
});

define(["cache"], {
  label: "Look up in cache", icon: "database", category: "data",
  blurb: "Reads a remembered value from a cache.",
  sections: [
    section("cache", "What to look up", [
      f.resource("resource", "Cache", { kinds: ["cache."], required: true }),
      f.fact("key_fact", "Key", { required: true, help: "The fact that names what to look for." }),
      f.text("prefix", "Key prefix", { placeholder: "optional" }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Key", cfg("key_fact"), { code: true }], ["In", ["resource"], { code: true }]),
});

define(["file"], {
  label: "Read a file", icon: "doc", category: "data",
  blurb: "Reads a stored file.",
  sections: [
    section("file", "Which file", [
      f.resource("resource", "File storage", { kinds: ["storage."], required: true }),
      f.long("key", "Path", { required: true, rows: 2, help: "Can include values, e.g. reports/{{ id }}.pdf." }),
      f.text("encoding", "Read it as", { placeholder: "text, base64…" }),
      f.bool("optional", "It is fine if the file is missing"),
      f.int("max_bytes", "Largest size", { placeholder: "bytes", advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["File", cfg("key"), { code: true }]),
});

define(["storage"], {
  label: "Save a file", icon: "database", category: "data",
  blurb: "Stores content as a file.",
  sections: [
    section("storage", "What to save", [
      f.resource("resource", "File storage", { kinds: ["storage."], required: true }),
      f.long("key", "Save as", { required: true, rows: 2, help: "The path, e.g. uploads/{{ user }}/photo.png." }),
      f.fact("content_fact", "The content", { required: true }),
      f.text("content_type", "Content type", { placeholder: "e.g. image/png" }),
      f.text("encoding", "It is written as", { advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Save as", cfg("key"), { code: true }], ["From", cfg("content_fact"), { code: true }]),
});

define(["search"], {
  label: "Search", icon: "eye", category: "data",
  blurb: "Looks things up in a search index.",
  sections: [
    section("search", "What to find", [
      f.resource("resource", "Search index", { kinds: ["search."], required: true }),
      f.text("collection", "Collection", { required: true, placeholder: "e.g. articles" }),
      f.long("text", "Look for", { rows: 2, help: "Text to search for. Or use a fact below." }),
      f.fact("text_fact", "…or the text in", {}),
      f.kv("filters", "Only where", { keyLabel: "Field", valueLabel: "Equals" }),
      f.int("limit", "At most this many", { placeholder: "e.g. 20" }),
      f.fact("offset_fact", "Start after", { advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["In", cfg("collection")], ["For", cfg("text"), { max: 30 }]),
});
