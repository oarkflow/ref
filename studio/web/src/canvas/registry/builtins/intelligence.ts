import { cfg, define, f, pick, section } from "./dsl";

const CHAT = [
  f.text("model", "Model", { placeholder: "leave empty for the default" }),
  f.long("system", "Instructions", { rows: 4, help: "Tell the model who it is and how to answer." }),
  f.long("prompt", "The question", { rows: 4, help: "Write {{ name }} where a value should go." }),
  f.fact("messages_fact", "Conversation so far", { advanced: true }),
  f.num("temperature", "Creativity", { placeholder: "0 to 1", help: "Lower is steadier, higher is more varied.", advanced: true }),
  f.int("max_tokens", "Longest answer", { placeholder: "e.g. 512", advanced: true }),
];

define(["llm"], {
  label: "Ask an AI model", icon: "spark", category: "intelligence",
  blurb: "Asks a language model and uses its answer.",
  sections: [section("ask", "The request", [f.resource("resource", "Through", { kinds: ["service."] }), ...CHAT, f.bool("json", "Expect the answer as structured data")])],
  summaryRows: (s) => pick(s, ["Model", cfg("model")], ["Asks", cfg("prompt"), { max: 34 }]),
});

define(["classifier"], {
  label: "Classify", icon: "spark", category: "intelligence",
  blurb: "Puts something into one of the categories you describe.",
  sections: [
    section("classify", "The categories", [
      f.resource("resource", "Through", { kinds: ["service."] }),
      f.long("system", "The categories", { rows: 4, required: true, help: "List them and say how to choose, e.g. “billing, bug, question”." }),
      f.long("prompt", "What to classify", { rows: 3, required: true, help: "Write {{ name }} where the text should go." }),
      f.text("model", "Model", { placeholder: "leave empty for the default" }),
      f.bool("json", "Answer as structured data", { default: "true" }),
      f.num("temperature", "Creativity", { placeholder: "0", advanced: true }),
      f.int("max_tokens", "Longest answer", { advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Classifies", cfg("prompt"), { max: 34 }]),
});

define(["embedding"], {
  label: "Embed text", icon: "spark", category: "intelligence",
  blurb: "Turns text into numbers that can be compared for meaning.",
  sections: [
    section("embed", "The text", [
      f.resource("resource", "Through", { kinds: ["service."] }),
      f.fact("input_fact", "Embed", { required: true }),
      f.text("model", "Model", { placeholder: "leave empty for the default" }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Embeds", cfg("input_fact"), { code: true }]),
});

define(["rag"], {
  label: "Find related documents", icon: "spark", category: "intelligence",
  blurb: "Finds passages from your documents that are close in meaning to a question.",
  sections: [
    section("rag", "What to find", [
      f.text("collection", "In the collection", { required: true, placeholder: "e.g. help-articles" }),
      f.long("text", "Look for", { rows: 2 }),
      f.fact("text_fact", "…or the text in"),
      f.int("limit", "At most this many", { placeholder: "e.g. 5" }),
      f.kv("filters", "Only where", { keyLabel: "Field", valueLabel: "Equals" }),
      f.text("content_field", "Passage field", { advanced: true }),
      f.text("title_field", "Title field", { advanced: true }),
      f.int("max_characters", "Longest passage", { advanced: true }),
      f.long("instruction", "Note added before the passages", { rows: 2, advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["In", cfg("collection")], ["For", cfg("text"), { max: 30 }]),
});
