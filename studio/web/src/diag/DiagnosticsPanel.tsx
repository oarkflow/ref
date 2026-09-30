import { AlertTriangle, CheckCircle2, ChevronRight, Info, OctagonAlert } from "lucide-react";
import { useMemo, useState } from "react";
import type { Diagnostic } from "../api/types";
import { friendlyProblem, groupProblems, problemWhere } from "../labels";
import { useStudio, useStudioStore } from "../state/context";
import { useUi } from "../state/ui";
import { Enter, Presence, Row, Skeleton } from "../ui/motion-components";

type Filter = "all" | "error" | "warning";

function relevant(d: Diagnostic, sel: { file: string; path: string } | null): boolean {
  if (!sel || !sel.path) return true;
  if (d.file && d.file !== sel.file) return false;
  return !!d.path && (d.path === sel.path || d.path.startsWith(sel.path + "/"));
}

/** The Problems list. `onOpen` lets the host navigate to the editor after selecting a block. */
export function DiagnosticsPanel({ onOpen }: { onOpen?: () => void }) {
  const diagnostics = useStudio((s) => s.diagnostics);
  const validating = useStudio((s) => s.validating);
  const store = useStudioStore();
  const dev = useUi((s) => s.devView);
  const selection = useStudio((s) => s.selection);
  const [filter, setFilter] = useState<Filter>("all");
  const [onlyHere, setOnlyHere] = useState(false);
  const [openRaw, setOpenRaw] = useState<number | null>(null);
  const [openGroups, setOpenGroups] = useState<Set<string>>(() => new Set());

  const counts = useMemo(() => {
    let error = 0;
    let warning = 0;
    for (const d of diagnostics) d.severity === "error" ? error++ : warning++;
    return { error, warning };
  }, [diagnostics]);
  const shown = useMemo(
    () => diagnostics
      .filter((d) => filter === "all" || (filter === "error" ? d.severity === "error" : d.severity !== "error"))
      .filter((d) => !onlyHere || relevant(d, selection)),
    [diagnostics, filter, onlyHere, selection],
  );
  const entries = useMemo(() => groupProblems(shown), [shown]);

  return (
    <section className="problems-panel" aria-label="Problems">
      <header className="panel-toolbar">
        <div className="seg" role="group" aria-label="Filter problems">
          <button type="button" aria-pressed={filter === "all"} onClick={() => setFilter("all")}>All {diagnostics.length}</button>
          <button type="button" aria-pressed={filter === "error"} onClick={() => setFilter("error")}>Errors {counts.error}</button>
          <button type="button" aria-pressed={filter === "warning"} onClick={() => setFilter("warning")}>Warnings {counts.warning}</button>
        </div>
        {selection && selection.path && (
          <button type="button" className={`btn sm scope${onlyHere ? " on" : ""}`} aria-pressed={onlyHere} onClick={() => setOnlyHere((v) => !v)} title="Only show problems in the item you have open">
            This item
          </button>
        )}
        <button type="button" className="btn sm" onClick={() => void store.getState().validate()} disabled={validating}>
          {validating ? "Checking…" : "Check again"}
        </button>
      </header>
      <div className="progress" hidden={!validating} aria-hidden="true" />
      {validating && shown.length === 0 && <div className="panel-pad"><Skeleton lines={3} /></div>}
      {!validating && shown.length === 0 ? (
        <Enter className="all-clear">
          <CheckCircle2 size={28} />
          <strong>{diagnostics.length === 0 ? "No problems found" : "Nothing matches this filter"}</strong>
          {diagnostics.length === 0 && <span className="muted">Everything in this working copy checks out.</span>}
        </Enter>
      ) : (
        <ul className="problem-list">
          <Presence mode="popLayout">
            {entries.map((e, gi) => {
              if (e.kind === "group") {
                const open = openGroups.has(e.key);
                return (
                  <Row key={`g:${e.key}`} className={`problem-row group ${e.severity}`}>
                    <li>
                      <button type="button" className={`diag group ${e.severity}`} aria-expanded={open} onClick={() => setOpenGroups((s) => { const n = new Set(s); n.has(e.key) ? n.delete(e.key) : n.add(e.key); return n; })}>
                        <span className="sev"><AlertTriangle size={16} /></span>
                        <span className="msg">{e.title}</span>
                        <span className="loc">{e.help}</span>
                        <ChevronRight size={16} className="group-chev" />
                      </button>
                      {open && (
                        <ul className="group-items">
                          {e.items.map(({ d, name }, i) => (
                            <li key={`${name}:${i}`}>
                              <button type="button" className="group-item" onClick={() => { void store.getState().selectFromDiagnostic(d); onOpen?.(); }}>
                                <code>{name}</code>
                                {problemWhere(d) && <span className="muted">{problemWhere(d)}</span>}
                              </button>
                            </li>
                          ))}
                        </ul>
                      )}
                    </li>
                  </Row>
                );
              }
              const d = e.d;
              const i = gi;
              const fp = friendlyProblem(d);
              const where = problemWhere(d);
              const key = `${d.severity}|${d.file ?? ""}|${d.path ?? ""}|${d.message}|${i}`;
              return (
                <Row key={key} className={`problem-row ${d.severity}`}>
                  <li>
                    <button
                      type="button"
                      className={`diag ${d.severity}`}
                      onClick={() => {
                        void store.getState().selectFromDiagnostic(d);
                        onOpen?.();
                      }}
                    >
                      <span className="sev" aria-label={d.severity}>{d.severity === "error" ? <OctagonAlert size={16} /> : d.severity === "info" ? <Info size={16} /> : <AlertTriangle size={16} />}</span>
                      <span className="msg">{fp.text}</span>
                      {where && <span className="loc">{where}</span>}
                    </button>
                    {(fp.rewritten || dev) && (
                      <button type="button" className="link-btn raw-toggle" aria-expanded={openRaw === i} onClick={() => setOpenRaw(openRaw === i ? null : i)}>
                        {openRaw === i ? "Hide details" : "Details"}
                      </button>
                    )}
                    {openRaw === i && <pre className="raw-msg">{d.message}{dev && location(d) ? `\n${location(d)}` : ""}</pre>}
                  </li>
                </Row>
              );
            })}
          </Presence>
        </ul>
      )}
    </section>
  );
}

/** File:line · path — used in Developer view. */
export function location(d: Diagnostic): string {
  const parts: string[] = [];
  if (d.file) parts.push(d.line ? `${d.file}:${d.line}` : d.file);
  if (d.path) parts.push(d.path);
  return parts.join(" · ");
}
