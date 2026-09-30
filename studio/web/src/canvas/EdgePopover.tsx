import { useEffect, useMemo, useRef, useState } from "react";
import type { Catalog, Op } from "../api/types";
import { FormProvider, type FormContext } from "../forms/ctx";
import { childPath } from "../lib/paths";
import { isDuration, quote, unquote } from "../lib/bcl";
import { useStudio } from "../state/context";
import { humanDuration, needsField, processEdgeLabel } from "./edgeLabels";
import { edgeLabel } from "./labels";
import type { EdgeBlockInfo } from "./model";
import { ConditionBuilder } from "./registry/ConditionBuilder";

/** Fields that define the wiring itself; the popover changes what a connection says, not where it goes. */
const WIRING = new Set(["kind", "from", "to", "sources", "targets", "id", "condition"]);

/** Settings the platform reads as a duration; anything else typed here would fail when the app runs. */
const DURATION_FIELDS = new Set(["timeout", "window"]);

/** A number, a duration, true/false or a plain word is written as it is; anything else is quoted. */
export function emitEdgeValue(text: string): string {
  const t = text.trim();
  if (/^-?\d+(\.\d+)?$/.test(t) || isDuration(t) || t === "true" || t === "false") return t;
  return quote(t);
}

const FIELD_LABELS: Record<string, { label: string; help?: string; placeholder?: string }> = {
  timeout: { label: "How long", help: "Use seconds, minutes or hours, like 30s, 15m or 48h. Days aren't supported.", placeholder: "48h" },
  event: { label: "Wait for this event", help: "The name of the event that moves the run on.", placeholder: "payment.received" },
  attempts: { label: "Number of attempts", placeholder: "3" },
  priority: { label: "Priority", help: "Lower numbers are tried first.", placeholder: "1" },
  weight: { label: "Weight", help: "A bigger weight is chosen more often.", placeholder: "1" },
  threshold: { label: "Amount", placeholder: "1000" },
  limit: { label: "Requests allowed", placeholder: "10" },
  window: { label: "Per time window", help: "Like 30s, 1m or 1h.", placeholder: "1m" },
  items_path: { label: "Repeat for each item in", placeholder: "todo.items" },
  batch_size: { label: "Batch size", placeholder: "50" },
  quorum: { label: "How many must finish", placeholder: "2" },
  strategy: { label: "Wait for", placeholder: "all" },
  fail_fast: { label: "Stop everything if one fails", placeholder: "false" },
};
const fieldMeta = (name: string) =>
  FIELD_LABELS[name] ?? { label: name.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase()) };

export interface EdgePopoverProps {
  anchor: DOMRect;
  file: string;
  block: EdgeBlockInfo;
  catalog: Catalog | null | undefined;
  readOnly?: boolean;
  /** Every connection of the process, so "otherwise" can be told apart from "add a condition". */
  siblings: readonly EdgeBlockInfo[];
  /** Names the condition builder can suggest. */
  facts: readonly string[];
  edit(ops: Op[]): void;
  /** Open the full settings for this connection. */
  onMore(): void;
  onClose(): void;
}

function TextSetting({ name, raw, disabled, onCommit }: { name: string; raw: string | undefined; disabled?: boolean; onCommit(next: string): void }) {
  const meta = fieldMeta(name);
  const shown = raw === undefined ? "" : (unquote(raw) ?? raw.trim());
  const [text, setText] = useState(shown);
  useEffect(() => setText(shown), [shown]);
  const isDur = DURATION_FIELDS.has(name);
  const bad = isDur && text.trim() !== "" && !isDuration(text.trim());
  // A value the app could not read is never written; the message says how to fix it.
  const commit = () => { if (!bad && text.trim() !== shown) onCommit(text); };
  const id = `ep-${name}`;
  return (
    <div className="cv-field">
      <label htmlFor={id}>{meta.label}</label>
      <input id={id} value={text} disabled={disabled} placeholder={meta.placeholder} aria-invalid={bad || undefined}
        onChange={(e) => setText(e.target.value)} onBlur={commit}
        onKeyDown={(e) => { if (e.key === "Enter") { e.currentTarget.blur(); } }} />
      {bad && <small className="bad" role="alert">That isn’t a length of time the app can read. Try 30s, 15m or 48h. Days aren’t supported.</small>}
      {!bad && isDur && isDuration(text.trim()) && <small>{humanDuration(text.trim())}</small>}
      {!bad && !(isDur && isDuration(text.trim())) && meta.help && <small>{meta.help}</small>}
    </div>
  );
}

/** Says what one connection means, and lets you change it without opening the full settings. */
export function EdgePopover({ anchor, file, block, catalog, readOnly, siblings, facts, edit, onMore, onClose }: EdgePopoverProps) {
  const ref = useRef<HTMLDivElement>(null);
  const diagnostics = useStudio((s) => s.diagnostics);
  const schemas = useStudio((s) => s.schemas);

  useEffect(() => {
    const away = (e: PointerEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) onClose(); };
    const key = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("pointerdown", away, true);
    document.addEventListener("keydown", key);
    return () => { document.removeEventListener("pointerdown", away, true); document.removeEventListener("keydown", key); };
  }, [onClose]);

  const typeInfo = catalog?.edge_types.find((t) => t.name === block.kind);
  const label = useMemo(() => processEdgeLabel(block, { siblings: [...siblings], typeInfo }), [block, siblings, typeInfo]);

  // Only the kinds of connection that are wired the same way can be swapped in place.
  const sameShape = useMemo(
    () => (catalog?.edge_types ?? []).filter((t) => !!t.multi === !!typeInfo?.multi).sort((a, b) => a.name.localeCompare(b.name)),
    [catalog, typeInfo],
  );
  const reads = typeInfo?.fields ?? [];
  const takesCondition = reads.includes("condition") || needsField(block.kind) === "condition" || !!block.condition;
  const settingNames = reads.filter((f) => !WIRING.has(f));
  const first = needsField(block.kind);

  const ctx: FormContext = { file, schemas, catalog: catalog ?? null, diagnostics, focusPath: null, overlay: {}, readOnly, edit };
  const setRaw = (name: string, raw: string | null) =>
    edit([raw === null || raw === ""
      ? { op: "removeField", file, path: childPath(block.path, name) }
      : { op: "setField", file, path: childPath(block.path, name), value: raw }]);

  const W = 340;
  const H = 480;
  const left = Math.max(8, Math.min(window.innerWidth - W - 8, anchor.left + anchor.width / 2 - W / 2));
  const below = anchor.bottom + 10 + H < window.innerHeight;
  const top = below ? anchor.bottom + 10 : Math.max(8, anchor.top - H - 10);

  const condition = (
    <ConditionBuilder label="Follow this when" value={block.condition} path={childPath(block.path, "condition")}
      suggestions={facts} readOnly={readOnly}
      help="Leave it empty for “otherwise” when another connection from this step has a condition."
      onChange={(f) => setRaw("condition", f === undefined ? null : quote(f))} />
  );
  const settings = settingNames.map((name) => (
    <TextSetting key={name} name={name} raw={block.settings[name]} disabled={readOnly}
      onCommit={(next) => setRaw(name, next.trim() === "" ? null : emitEdgeValue(next))} />
  ));

  return (
    <div ref={ref} className={`cv-popover cv-edgepop ${below ? "below" : "above"}`} role="dialog" aria-label="Connection settings"
      style={{ left, top, width: W, maxHeight: H }}>
      <h3>{label.text || edgeLabel(block.kind)}</h3>
      <p className="cv-edgepop-sub">
        {block.from ?? block.sources.raws.join(", ")} <span aria-hidden="true">→</span> {block.to ?? block.targets.raws.join(", ")}
        {label.tone === "todo" && <span className="cv-chip todo"> needs a value</span>}
      </p>
      <FormProvider value={ctx}>
        <div className="cv-edgepop-body">
          <div className="cv-field">
            <label htmlFor="ep-kind">How it connects</label>
            <select id="ep-kind" value={block.kind} disabled={readOnly}
              onChange={(e) => setRaw("kind", e.target.value)}>
              {!sameShape.some((t) => t.name === block.kind) && <option value={block.kind}>{edgeLabel(block.kind)}</option>}
              {sameShape.map((t) => <option key={t.name} value={t.name}>{edgeLabel(t.name)}</option>)}
            </select>
            {typeInfo?.summary && <small>{typeInfo.summary}</small>}
          </div>
          {first === "condition" && takesCondition && condition}
          {first !== "condition" && settings}
          {first !== "condition" && takesCondition && condition}
          {first === "condition" && settings}
        </div>
      </FormProvider>
      <div className="cv-edgepop-actions">
        <button type="button" onClick={onMore}>More settings</button>
        <button type="button" className="primary" onClick={onClose}>Done</button>
      </div>
    </div>
  );
}
