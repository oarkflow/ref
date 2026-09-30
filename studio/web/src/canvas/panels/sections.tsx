import { useMemo } from "react";
import { INPUT_FACT, itemRaw, type IntentGraph } from "../model";
import { AddName, Choice, NameChips, Row, Section, Setting, Toggle, useStringList, type BlockIO } from "./kit";

// ---------------------------------------------------------------------------
// Needs / Produces

export function FactsSection({ io, graph, selfId }: { io: BlockIO; graph?: IntentGraph; selfId: string }) {
  const req = useStringList(io.field("requires"));
  const prov = useStringList(io.field("provides"));
  const available = useMemo(() => {
    const all = new Set<string>([INPUT_FACT]);
    for (const n of graph?.nodes ?? []) if (n.id !== selfId) for (const f of n.provides) all.add(f);
    return [...all].filter((f) => !req.names.includes(f));
  }, [graph, selfId, req.names]);
  const producedHere = new Set(graph?.nodes.flatMap((n) => n.provides) ?? []);
  const unresolved = (n: string) => (n === INPUT_FACT || producedHere.has(n) ? undefined : "bad");

  return (
    <Section title="What it works with" id="facts">
      <div className="cv-field">
        <label>Needs <small>(facts it reads before it runs)</small></label>
        {req.editable ? (
          <>
            <NameChips names={req.names} tone={unresolved} readOnly={io.readOnly} empty="Needs nothing" onRemove={(n) => req.write(req.raws.filter((_, i) => req.names[i] !== n))} />
            <AddName label="Add something it needs" placeholder="+ Needs…" options={available} readOnly={io.readOnly} onAdd={(n) => req.write([...req.raws, itemRaw(n)])} />
            {req.names.some((n) => unresolved(n)) && <small className="bad">Red items are not produced by any step. Connect a step that produces them, or remove them.</small>}
          </>
        ) : (
          <Setting io={io.field("requires")} label="Needs" kind="any" doc="This list is written as an expression; edit it here as text." />
        )}
      </div>
      <div className="cv-field">
        <label>Produces <small>(facts other steps can use)</small></label>
        {prov.editable ? (
          <>
            <NameChips names={prov.names} readOnly={io.readOnly} empty="Produces nothing" onRemove={(n) => prov.write(prov.raws.filter((_, i) => prov.names[i] !== n))} />
            <AddName free label="Name a new result" placeholder="name of a result" readOnly={io.readOnly} onAdd={(n) => prov.write([...prov.raws, itemRaw(n)])} />
          </>
        ) : (
          <Setting io={io.field("provides")} label="Produces" kind="any" doc="This list is written as an expression; edit it here as text." />
        )}
      </div>
    </Section>
  );
}

// ---------------------------------------------------------------------------
// "If it goes wrong": timeout, retry, bulkhead, on_error

const STRATEGIES = [
  { value: "fixed", label: "Same wait every time" },
  { value: "linear", label: "A little longer each time" },
  { value: "exponential", label: "Doubling wait" },
  { value: "exponential_jitter", label: "Doubling wait, with some randomness" },
  { value: "decorrelated_jitter", label: "Randomised wait" },
];
const ON_ERROR = [
  { value: "fail", label: "Stop the whole flow", hint: "The default: the request fails." },
  { value: "continue", label: "Carry on with a fallback value", hint: "Needs a fallback value for everything this step produces." },
  { value: "fallback", label: "Carry on with a fallback value", hint: "Same as “carry on”, named for readability." },
];
const RETRY_ON = ["invalid_input", "not_found", "conflict", "permission", "auth", "rate_limit", "unavailable", "timeout", "internal"];
const RETRY_ON_LABEL: Record<string, string> = {
  invalid_input: "bad input", not_found: "not found", conflict: "conflict", permission: "no permission", auth: "not signed in",
  rate_limit: "rate limited", unavailable: "service unavailable", timeout: "took too long", internal: "internal error",
};

export function ReliabilitySection({ io, kind }: { io: BlockIO; kind: "step" | "node" }) {
  const retry = io.has("retry");
  const bulkhead = io.has("bulkhead");
  const retryOn = useStringList(io.inner("retry", "retry_on"));
  return (
    <Section title="If it goes wrong" id="reliability" open={false}>
      <Setting io={io.field("timeout")} label="Give up after" kind="duration" placeholder="e.g. 5s" doc="How long this step may take before it counts as failed." />
      <div className="cv-group">
        <Toggle io={{ path: "", raw: retry ? "true" : undefined, set: (v) => (v ? io.addBlock("retry") : io.removeBlock("retry")) }} label="Try again when it fails" readOnly={io.readOnly} />
        {retry && (
          <div className="cv-indent">
            <Row>
              <Setting io={io.inner("retry", "max_attempts")} label="Tries in total" kind="int" placeholder="3" />
              <Choice io={io.inner("retry", "strategy")} label="Wait between tries" options={STRATEGIES} readOnly={io.readOnly} />
            </Row>
            <Row>
              <Setting io={io.inner("retry", "initial_delay")} label="First wait" kind="duration" placeholder="200ms" />
              <Setting io={io.inner("retry", "max_delay")} label="Longest wait" kind="duration" placeholder="5s" />
            </Row>
            <Toggle io={io.inner("retry", "jitter")} label="Add some randomness to the wait" readOnly={io.readOnly} />
            <div className="cv-field">
              <label>Only try again after <small>(empty means temporary problems only)</small></label>
              <NameChips names={retryOn.names.map((n) => RETRY_ON_LABEL[n] ?? n)} readOnly={io.readOnly} empty="Temporary problems only" onRemove={(label) => {
                const key = Object.keys(RETRY_ON_LABEL).find((k) => RETRY_ON_LABEL[k] === label) ?? label;
                retryOn.write(retryOn.raws.filter((_, i) => retryOn.names[i] !== key));
              }} />
              <AddName label="Add a kind of failure" placeholder="+ Kind of failure…" readOnly={io.readOnly} options={RETRY_ON.filter((k) => !retryOn.names.includes(k))} onAdd={(k) => retryOn.write([...retryOn.raws, itemRaw(k)])} />
            </div>
          </div>
        )}
      </div>
      {kind === "node" && (
        <>
          <div className="cv-group">
            <Toggle io={{ path: "", raw: bulkhead ? "true" : undefined, set: (v) => (v ? io.addBlock("bulkhead") : io.removeBlock("bulkhead")) }} label="Limit how many run at the same time" readOnly={io.readOnly} />
            {bulkhead && (
              <div className="cv-indent">
                <Row>
                  <Setting io={io.inner("bulkhead", "limit")} label="At most" kind="int" placeholder="10" />
                  <Setting io={io.inner("bulkhead", "name")} label="Shared with" placeholder="(only this step)" doc="Steps that use the same name share one limit." />
                </Row>
              </div>
            )}
          </div>
          <Choice io={io.field("on_error")} label="When it still fails" options={ON_ERROR} readOnly={io.readOnly} allowNone />
        </>
      )}
      {kind === "step" && (
        <>
          <Setting io={io.field("compensate")} label="Undo with this flow" placeholder="flow that reverses this step" doc="Runs if a later step fails for good." />
          <Setting io={io.field("circuit_breaker")} label="Stop calling when it keeps failing" placeholder="breaker name" />
        </>
      )}
    </Section>
  );
}
