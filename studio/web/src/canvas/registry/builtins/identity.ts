import { cfg, define, f, pick, section } from "./dsl";

define(["auth"], {
  label: "Check who is asking", icon: "shield", category: "identity",
  blurb: "Works out who is making the request, using a sign-in method you have set up.",
  sections: [section("auth", "How to sign in", [f.resource("resource", "Sign-in method", { kinds: ["auth."], required: true })])],
  summaryRows: (s) => pick(s, ["Method", ["resource"], { code: true }]),
});

define(["authz"], {
  label: "Check permission", icon: "shield", category: "identity",
  blurb: "Lets the request through only for the roles or permissions you choose.",
  sections: [
    section("access", "Who may continue", [
      f.resource("resource", "Permission system", { kinds: ["authz."] }),
      f.strs("roles", "Any of these roles", { help: "Someone with at least one of them may continue." }),
      f.strs("permissions", "Or these permissions"),
      f.strs("scopes", "Or these scopes", { advanced: true }),
      f.strs("deny_roles", "Never these roles", { help: "Always turned away, even if they have another role.", advanced: true }),
      f.bool("require_all", "They must have all of them, not just one"),
      f.cond("condition", "Only when", { lead: "Only check when", help: "Skip this check unless the condition is true." }),
      f.text("message", "If refused, tell the caller", { placeholder: "You may not do this." }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Roles", cfg("roles")], ["Permissions", cfg("permissions")]),
});

define(["session"], {
  label: "Read the session", icon: "lock", category: "identity",
  blurb: "Reads a value remembered for the signed-in visitor.",
  sections: [
    section("session", "What to read", [
      f.resource("resource", "Session store", { kinds: ["session."], required: true }),
      f.text("key", "Value to read", { required: true, placeholder: "e.g. cart" }),
      f.bool("required", "Fail when it is not there"),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Reads", cfg("key")], ["From", ["resource"], { code: true }]),
});
