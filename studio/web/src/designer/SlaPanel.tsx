import { useState } from "react";
import type { BlockNode, Op } from "../api/types";
import { Text } from "./Fields";
import {
  BREACH_ACTIONS, DURATION_RE, addEscalationOp, addSlaOp, parseCalendars, parseSla, parseStages, setCalendarOp, setEscalationOp, setSlaOp, splitList, stageActions,
  type DesignCalendar, type DesignSla,
} from "./model";

interface Props { file: string; pipeline: BlockNode; canEdit: boolean; apply(ops: Op[]): void }

/** Rough minutes in a span like "1d4h30m" (a day counts as 24h here; with a calendar it is a working day). */
export function spanMinutes(s: string): number | null {
  if (!DURATION_RE.test(s)) return null;
  let total = 0;
  for (const m of s.matchAll(/(\d+(?:\.\d+)?)(ns|us|ms|s|m|h|d)/g)) {
    const n = Number(m[1]);
    total += n * ({ ns: 0, us: 0, ms: 0, s: 1 / 60, m: 1, h: 60, d: 1440 } as Record<string, number>)[m[2]!]!;
  }
  return total;
}

const fmt = (min: number) => (min >= 1440 && min % 1440 === 0 ? `${min / 1440}d` : min >= 60 ? `${Math.round((min / 60) * 10) / 10}h` : `${Math.round(min)}m`);

/** Deadlines, warnings, breach actions and escalation for each stage. */
export function SlaPanel({ file, pipeline, canEdit, apply }: Props) {
  const stages = parseStages(pipeline);
  const calendars = parseCalendars(pipeline);
  const [name, setName] = useState("");
  const stage = stages.find((s) => s.name === name) ?? stages[0];
  if (!stage) return <p className="empty">This pipeline has no stages.</p>;
  const sla = parseSla(stage.node);
  const actions = stageActions(stage.node);

  return (
    <div className="dz-layout">
      <div className="dz-stagebar">
        <label>Stage
          <select value={stage.name} onChange={(e) => setName(e.target.value)}>
            {stages.map((s) => <option key={s.name} value={s.name}>{s.title || s.name}{parseSla(s.node) ? ` · ${parseSla(s.node)!.duration}` : " · no SLA"}</option>)}
          </select>
        </label>
        <span className="dz-hint">How long the stage may take, when to warn, and what happens when time runs out. The app applies these when the SLA sweep runs.</span>
      </div>
      {!sla ? (
        <div className="dz-nopage">
          <p>This stage has no deadline.</p>
          {canEdit && <button type="button" className="dz-primary" onClick={() => apply([addSlaOp(file, stage)])}>Add an SLA (24h, notify on breach)</button>}
        </div>
      ) : (
        <div className="dz-layoutgrid">
          <section className="dz-groups" aria-label="SLA settings">
            <SlaForm key={sla.path} file={file} sla={sla} calendars={calendars} stages={stages.map((s) => s.name).filter((n) => n !== stage.name)} actions={actions} canEdit={canEdit} apply={apply} onRemove={() => apply([{ op: "removeBlock", file, path: sla.path }])} />
          </section>
          <section className="dz-lpreview" aria-label="SLA timeline">
            <h3>Timeline</h3>
            <Timeline sla={sla} />
            <h3>Calendars</h3>
            {calendars.length === 0 && <p className="dz-hint">No calendars: time is counted around the clock.</p>}
            {calendars.map((c) => <CalendarCard key={c.path} file={file} cal={c} used={sla.calendar === c.name} canEdit={canEdit} apply={apply} />)}
          </section>
        </div>
      )}
    </div>
  );
}

function SlaForm({ file, sla, calendars, stages, actions, canEdit, apply, onRemove }: {
  file: string; sla: DesignSla; calendars: DesignCalendar[]; stages: string[]; actions: string[]; canEdit: boolean; apply(o: Op[]): void; onRemove(): void;
}) {
  const dis = !canEdit;
  const bad = (v: string) => v !== "" && !DURATION_RE.test(v);
  const breach = [...BREACH_ACTIONS, ...actions.filter((a) => !BREACH_ACTIONS.includes(a))];
  return (
    <div className="dz-propform">
      <div className="dz-two">
        <Text label="Time allowed" value={sla.duration} disabled={dis} hint={bad(sla.duration) ? "Use a span like 24h, 2d or 90m" : "e.g. 24h, 2d (working days when a calendar is set)"} onCommit={(v) => apply([setSlaOp(file, sla, "duration", v)])} />
        <Text label="Warn before the deadline" value={sla.warnBefore} disabled={dis} hint={bad(sla.warnBefore) ? "Use a span like 4h" : undefined} onCommit={(v) => apply([setSlaOp(file, sla, "warn_before", v)])} />
      </div>
      <label>Working calendar
        <select disabled={dis} value={sla.calendar} onChange={(e) => apply([setSlaOp(file, sla, "calendar", e.target.value)])}>
          <option value="">none (around the clock)</option>
          {calendars.map((c) => <option key={c.name} value={c.name}>{c.name}</option>)}
          {sla.calendar && !calendars.some((c) => c.name === sla.calendar) && <option value={sla.calendar}>{sla.calendar} (undefined)</option>}
        </select>
      </label>
      <label>When the deadline is missed
        <select disabled={dis} value={sla.onBreach || "notify"} onChange={(e) => apply([setSlaOp(file, sla, "on_breach", e.target.value)])}>
          {breach.map((a) => <option key={a} value={a}>{BREACH_ACTIONS.includes(a) ? ({ notify: "Notify only", reassign: "Reassign to someone else", release: "Release back to the queue", return: "Return to an earlier stage" } as Record<string, string>)[a] : `Take the “${a}” action automatically`}</option>)}
        </select>
      </label>
      {sla.onBreach === "return" && (
        <label>Return to
          <select disabled={dis} value={sla.returnTo} onChange={(e) => apply([setSlaOp(file, sla, "return_to", e.target.value)])}>
            <option value="">the previous stage</option>
            {stages.map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </label>
      )}
      <div className="dz-two">
        <Text label="Pickup timeout (unclaimed work)" value={sla.pickupTimeout} disabled={dis} hint={bad(sla.pickupTimeout) ? "Use a span like 1h" : undefined} onCommit={(v) => apply([setSlaOp(file, sla, "pickup_timeout", v)])} />
        <label>If nobody picks it up
          <select disabled={dis || !sla.pickupTimeout} value={sla.onPickupTimeout || "notify"} onChange={(e) => apply([setSlaOp(file, sla, "on_pickup_timeout", e.target.value === "notify" ? "" : e.target.value)])}>
            <option value="notify">Notify only</option><option value="reassign">Reassign</option>
          </select>
        </label>
      </div>
      <h3>Escalation after a breach</h3>
      {sla.escalations.length === 0 && <p className="dz-hint">No escalation levels.</p>}
      {sla.escalations.map((e) => (
        <div key={e.path} className="dz-esc">
          <b>{e.name}</b>
          <Text label="After the breach" value={e.after} disabled={dis} hint={bad(e.after) ? "Use a span like 12h" : undefined} onCommit={(v) => apply([setEscalationOp(file, e, "after", v)])} />
          <Text label="Assign to roles" value={e.assignRoles.join(", ")} disabled={dis} onCommit={(v) => apply([setEscalationOp(file, e, "assign_roles", splitList(v))])} />
          <Text label="Tell (people or groups)" value={e.notify.join(", ")} disabled={dis} onCommit={(v) => apply([setEscalationOp(file, e, "notify", splitList(v))])} />
          {canEdit && <button type="button" className="dz-link danger" onClick={() => apply([{ op: "removeBlock", file, path: e.path }])}>Delete level</button>}
        </div>
      ))}
      {canEdit && (
        <div className="dz-actions">
          <button type="button" onClick={() => apply([addEscalationOp(file, sla)])}>+ Escalation level</button>
          <button type="button" className="danger" onClick={onRemove}>Remove SLA</button>
        </div>
      )}
    </div>
  );
}

function Timeline({ sla }: { sla: DesignSla }) {
  const due = spanMinutes(sla.duration);
  if (due === null) return <p className="dz-hint">Enter a valid time allowed to see the timeline.</p>;
  const events: { at: number; label: string; kind: string }[] = [{ at: 0, label: "Stage starts", kind: "start" }];
  const warn = spanMinutes(sla.warnBefore);
  if (warn) events.push({ at: Math.max(due - warn, 0), label: "Warning", kind: "warn" });
  const pick = spanMinutes(sla.pickupTimeout);
  if (pick) events.push({ at: pick, label: `Pickup timeout (${sla.onPickupTimeout || "notify"})`, kind: "warn" });
  events.push({ at: due, label: `Deadline: ${sla.onBreach || "notify"}`, kind: "bad" });
  for (const e of sla.escalations) { const a = spanMinutes(e.after); if (a !== null) events.push({ at: due + a, label: `Escalate: ${e.name}${e.assignRoles.length ? " → " + e.assignRoles.join(", ") : ""}`, kind: "esc" }); }
  events.sort((a, b) => a.at - b.at);
  return (
    <ol className="dz-timeline">
      {events.map((e, i) => <li key={i} className={e.kind}><b>{fmt(e.at)}</b> {e.label}</li>)}
    </ol>
  );
}

function CalendarCard({ file, cal, used, canEdit, apply }: { file: string; cal: DesignCalendar; used: boolean; canEdit: boolean; apply(o: Op[]): void }) {
  return (
    <div className={`dz-cal${used ? " used" : ""}`}>
      <b>{cal.name}{used ? " (used here)" : ""}</b>
      <Text label="Time zone" value={cal.timezone} disabled={!canEdit} onCommit={(v) => apply([setCalendarOp(file, cal, "timezone", v)])} />
      <Text label="Working hours" value={cal.hours} disabled={!canEdit} hint="e.g. mon-fri 08:00-18:00" onCommit={(v) => apply([setCalendarOp(file, cal, "hours", v)])} />
      {cal.holidays.length > 0 && <small className="dz-hint">Holidays: {cal.holidays.join(", ")}</small>}
    </div>
  );
}
