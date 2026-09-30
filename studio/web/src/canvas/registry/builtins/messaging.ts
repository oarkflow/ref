import { cfg, define, f, pick, section } from "./dsl";

define(["email"], {
  label: "Send an email", icon: "mail", category: "messaging",
  blurb: "Sends an email through a mail connection.",
  sections: [
    section("email", "The message", [
      f.resource("resource", "Send through", { kinds: ["service."], required: true }),
      f.strs("to", "To", { help: "Addresses, or templates like {{ user.email }}." }),
      f.fact("to_fact", "…or the address in", {}),
      f.text("subject", "Subject", { required: true }),
      f.long("body", "Message", { rows: 5, help: "Plain text. Write {{ name }} where a value should go." }),
      f.long("html", "Message (formatted)", { rows: 5, advanced: true }),
    ]),
    section("more", "More recipients", [
      f.strs("cc", "Copy to"), f.strs("bcc", "Hidden copy to"),
      f.text("from", "From"), f.text("reply_to", "Replies go to"),
      f.kv("headers", "Extra headers", { keyLabel: "Header", valueLabel: "Value" }),
    ], { open: false }),
  ],
  summaryRows: (s) => pick(s, ["To", cfg("to")], ["Subject", cfg("subject")]),
});

define(["notification"], {
  label: "Send a notification", icon: "bell", category: "messaging",
  blurb: "Notifies someone through a channel you have set up (SMS, push, chat…).",
  sections: [
    section("notify", "The notification", [
      f.resource("channel", "Channel", { kinds: undefined, required: true, at: ["config", "channel"], help: "One of your notification channels." }),
      f.text("target", "Send to", { help: "A person or address, e.g. {{ user.phone }}." }),
      f.fact("target_fact", "…or the recipient in", {}),
      f.text("subject", "Title"),
      f.long("body", "Message", { rows: 4 }),
      f.fact("data_fact", "Attach", { advanced: true }),
      f.text("job_type", "Delivered as", { advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Via", cfg("channel"), { code: true }], ["To", cfg("target")]),
});

const QUEUE_FIELDS = (extra: readonly ReturnType<typeof f.text>[] = []) => [
  f.resource("resource", "Queue", { kinds: ["queue."] }),
  f.text("job_type", "Kind of job", { required: true, placeholder: "e.g. send_welcome_email" }),
  f.fact("payload_fact", "Send", { help: "The fact that becomes the job's data." }),
  f.int("priority", "Priority", { placeholder: "0", help: "Higher goes first." }),
  f.expr("concurrency_key", "Only one at a time per", { rows: 2, help: "Jobs with the same value run one after another.", advanced: true }),
  f.kv("headers", "Extra headers", { keyLabel: "Header", valueLabel: "Value", advanced: true }),
  ...extra,
];

define(["queue"], {
  label: "Add to a queue", icon: "send", category: "messaging",
  blurb: "Puts a job on a queue to be done later, in the background.",
  sections: [section("queue", "The job", QUEUE_FIELDS())],
  summaryRows: (s) => pick(s, ["Job", cfg("job_type")], ["Queue", ["resource"], { code: true }]),
});

define(["event"], {
  label: "Publish an event", icon: "send", category: "messaging",
  blurb: "Announces that something happened, for other parts of the system to react to.",
  sections: [section("event", "The event", QUEUE_FIELDS())],
  summaryRows: (s) => pick(s, ["Event", cfg("job_type")]),
});

define(["outbox"], {
  label: "Queue for delivery", icon: "send", category: "messaging",
  blurb: "Saves a message together with your data, and delivers it reliably afterwards.",
  sections: [
    section("outbox", "The message", [
      f.text("topic", "Topic", { required: true, placeholder: "e.g. orders.created" }),
      f.fact("payload_fact", "Send"),
      f.kv("headers", "Extra headers", { keyLabel: "Header", valueLabel: "Value", advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Topic", cfg("topic"), { code: true }]),
});

define(["inbox"], {
  label: "Receive once", icon: "send", category: "messaging",
  blurb: "Makes sure a message arriving more than once is only acted on once.",
  sections: [
    section("inbox", "Duplicates", [
      f.text("source", "Where it came from", { required: true, placeholder: "e.g. billing-webhook" }),
      f.fact("key_fact", "Its unique id"),
      f.fact("payload_fact", "Its content", { advanced: true }),
      f.select("on_duplicate", "When it is a repeat", [{ value: "skip", label: "Skip it quietly" }, { value: "fail", label: "Fail the request" }], { bare: false }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["From", cfg("source")], ["Id", cfg("key_fact"), { code: true }]),
});

define(["stream"], {
  label: "Stream", icon: "send", category: "messaging",
  blurb: "Sends a live update to whoever is watching.",
  sections: [
    section("stream", "The update", [
      f.text("event", "Kind of update", { placeholder: "e.g. progress" }),
      f.fact("data_fact", "Send"),
      f.long("data", "…or this text", { rows: 3 }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Event", cfg("event")]),
});

define(["webhook"], {
  label: "Send a webhook", icon: "send", category: "messaging",
  blurb: "Tells another system about something by calling its web address, signed with a secret.",
  sections: [
    section("webhook", "Where and what", [
      f.resource("resource", "Send through", { kinds: ["service."] }),
      f.long("url", "Address", { required: true, rows: 2, placeholder: "https://…" }),
      f.long("event_type", "Kind of event", { rows: 1, placeholder: "e.g. order.paid" }),
      f.fact("payload_fact", "Send", { required: true }),
      f.secret("secret", "Sign it with", { required: true }),
      f.fact("id_fact", "Its unique id", { advanced: true }),
      f.kv("headers", "Extra headers", { keyLabel: "Header", valueLabel: "Value", advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["To", cfg("url"), { code: true }], ["Event", cfg("event_type")]),
});

define(["worker"], {
  label: "Background job", icon: "gear", category: "messaging",
  blurb: "Hands work to a background worker. What it does is set in the worker itself.",
  sections: [section("worker", "Result", [f.bool("unwrap", "Pass on a single result as itself")])],
  summaryRows: () => [],
});
