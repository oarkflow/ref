import { cfg, define, f, pick, section } from "./dsl";

define(["rate_limit"], {
  label: "Limit how often", icon: "gauge", category: "coordination",
  blurb: "Turns requests away when someone asks too often.",
  sections: [
    section("limit", "The limit", [
      f.resource("resource", "Limiter", { kinds: ["ratelimit."], required: true }),
      f.expr("key", "Count separately for each", { required: true, rows: 2, placeholder: "e.g. principal.id", help: "Each different value has its own count." }),
      f.int("limit", "Allowed", { required: true, placeholder: "e.g. 10" }),
      f.dur("window", "Within", { required: true, placeholder: "e.g. 1m" }),
      f.text("message", "If over the limit, say", { placeholder: "Too many requests." }),
      f.bool("required", "Fail when the limiter cannot be reached"),
    ]),
  ],
  summaryRows: (s) => {
    const l = s.text(cfg("limit"));
    const w = s.text(cfg("window"));
    return l && w ? [{ label: "Allows", value: `${l} per ${w}` }, ...pick(s, ["Per", cfg("key"), { code: true, max: 30 }])] : pick(s, ["Per", cfg("key"), { code: true }]);
  },
});

define(["lock"], {
  label: "Take a lock", icon: "lock", category: "coordination",
  blurb: "Makes sure only one thing works on the same item at a time.",
  sections: [
    section("lock", "The lock", [
      f.resource("resource", "Lock store", { kinds: ["lock."], required: true }),
      f.expr("key", "Lock on", { required: true, rows: 2, placeholder: "e.g. order.id" }),
      f.dur("ttl", "Hold it for at most", { placeholder: "e.g. 30s" }),
      f.dur("wait", "Wait for it up to", { placeholder: "e.g. 5s" }),
      f.bool("required", "Fail when it cannot be taken"),
    ]),
  ],
  summaryRows: (s) => pick(s, ["On", cfg("key"), { code: true }], ["Holds", cfg("ttl")]),
});

define(["idempotency"], {
  label: "Prevent duplicates", icon: "shield", category: "coordination",
  blurb: "Recognises a request it has already handled and does not do the work twice.",
  sections: [
    section("dupes", "Recognising a repeat", [
      f.expr("key", "Two requests are the same when this matches", { required: true, rows: 2, placeholder: "e.g. request.idempotency_key" }),
      f.dur("ttl", "Remember for", { placeholder: "e.g. 24h" }),
      f.select("on_duplicate", "When it is a repeat", [{ value: "replay", label: "Give the earlier answer" }, { value: "reject", label: "Turn it away" }], { bare: false }),
      f.text("prefix", "Key prefix", { advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Same if", cfg("key"), { code: true, max: 30 }], ["For", cfg("ttl")]),
});

define(["circuit_breaker"], {
  label: "Stop calling a failing service", icon: "gauge", category: "coordination",
  blurb: "Stops trying a service that keeps failing, so it can recover.",
  sections: [
    section("breaker", "The breaker", [
      f.expr("key", "One breaker for each", { required: true, rows: 2, placeholder: "e.g. 'billing-api'", help: "Requests with the same value share the breaker." }),
      f.text("message", "While it is open, say", { placeholder: "Try again in a little while." }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["For", cfg("key"), { code: true }]),
});
