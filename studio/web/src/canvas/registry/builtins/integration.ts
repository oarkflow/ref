import { cfg, define, f, pick, section } from "./dsl";

const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"].map((m) => ({ value: m, label: m }));

const callFields = (kinds: readonly string[] | undefined, urlHelp: string) => [
  f.resource("resource", "Through the client", { kinds, help: "The outbound connection to use." }),
  f.select("method", "Method", METHODS, { bare: true, allowNone: false, default: "GET" }),
  f.long("url", "Address", { required: true, rows: 2, help: urlHelp, placeholder: "https://… or a path" }),
  f.kv("query", "Query values", { keyLabel: "Name", valueLabel: "Value" }),
  f.kv("headers", "Headers", { keyLabel: "Header", valueLabel: "Value" }),
  f.fact("body_fact", "Send this as the body", { help: "A fact to send. Or set fields below.", visibleWhen: (v) => !["GET", "HEAD"].includes(v.text("config.method") ?? v.raw("config.method") ?? "GET") }),
  f.kv("body", "Body fields", { keyLabel: "Field", valueLabel: "Value", advanced: true }),
  f.list("expect_status", "Treat these replies as success", { item: "number", help: "Empty means any 2xx." }),
  f.bool("raw", "Keep the reply as plain text"),
];

const callRows = (s: Parameters<NonNullable<Parameters<typeof define>[1]["summaryRows"]>>[0]) => {
  const url = s.text(cfg("url"));
  return url ? [{ label: "Calls", value: `${s.text(cfg("method")) ?? "GET"} ${url}`.slice(0, 50), code: true }] : [];
};

define(["http"], {
  label: "Call a web service", icon: "plug", category: "integration",
  blurb: "Sends an HTTP request to another system and uses its reply.",
  sections: [section("call", "The call", callFields(["service."], "Can include values from the facts this step needs."))],
  summaryRows: callRows,
});

define(["service"], {
  label: "Call a service", icon: "plug", category: "integration",
  blurb: "Calls a service through a connection you have set up, so its address and sign-in are kept in one place.",
  sections: [
    section("call", "The call", [
      f.resource("resource", "Through the connection", { kinds: ["service."], required: true }),
      f.select("method", "Method", METHODS, { bare: true, allowNone: false, default: "GET" }),
      f.long("url", "Path", { rows: 1, help: "Added to the connection's address, e.g. /v1/orders/{{ id }}.", placeholder: "/v1/…" }),
      f.kv("query", "Query values", { keyLabel: "Name", valueLabel: "Value" }),
      f.fact("body_fact", "Send this as the body", { visibleWhen: (v) => !["GET", "HEAD"].includes(v.text("config.method") ?? v.raw("config.method") ?? "GET") }),
      f.kv("headers", "Headers", { keyLabel: "Header", valueLabel: "Value", advanced: true }),
      f.list("expect_status", "Treat these replies as success", { item: "number", advanced: true }),
      f.bool("raw", "Keep the reply as plain text", { advanced: true }),
    ]),
  ],
  summaryRows: callRows,
});

define(["tool"], {
  label: "Use a tool", icon: "plug", category: "integration",
  blurb: "Calls a tool over HTTP, for example one an AI step may use.",
  sections: [section("call", "The tool call", callFields(undefined, "Where the tool listens."))],
  summaryRows: callRows,
});

define(["websocket"], {
  label: "WebSocket", icon: "plug", category: "integration",
  blurb: "Talks to a live connection. The first request is sent over HTTP; the rest are in the settings.",
  sections: [section("call", "The connection", callFields(["service."], "The address to connect to."))],
  summaryRows: callRows,
});

define(["graphql"], {
  label: "GraphQL call", icon: "plug", category: "integration",
  blurb: "Sends a GraphQL query and uses the answer.",
  sections: [
    section("query", "The query", [
      f.resource("resource", "Through the client", { kinds: ["service."] }),
      f.long("url", "Address", { rows: 2, help: "Leave empty to use the client's address." }),
      f.code("query", "Query", { required: true, language: "text", rows: 6, placeholder: "query { … }" }),
      f.text("operation_name", "Operation name", { advanced: true }),
      f.fact("variables_fact", "Variables", { help: "A fact holding the variables." }),
      f.kv("headers", "Headers", { keyLabel: "Header", valueLabel: "Value", advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Query", cfg("query"), { code: true, max: 34 }]),
});

define(["grpc"], {
  label: "gRPC call", icon: "plug", category: "integration",
  blurb: "Calls a method on a gRPC service.",
  sections: [
    section("call", "The call", [
      f.resource("resource", "Through the client", { kinds: ["service."] }),
      f.text("service", "Service", { required: true, placeholder: "e.g. billing.Invoices" }),
      f.text("method", "Method", { required: true, placeholder: "e.g. Create" }),
      f.long("url", "Address", { rows: 2, help: "Leave empty to use the client's address." }),
      f.fact("request_fact", "Send", {}),
      f.kv("headers", "Headers", { keyLabel: "Header", valueLabel: "Value", advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Calls", cfg("method"), { code: true }], ["On", cfg("service"), { code: true }]),
});

define(["connector"], {
  label: "Connector", icon: "plug", category: "integration",
  blurb: "A ready-made link to another system. Its settings come from the connector you pick.",
  sections: [section("connector", "Connection", [f.resource("resource", "Connect through", { kinds: ["service."] })])],
  summaryRows: (s) => pick(s, ["Through", ["resource"], { code: true }]),
});
