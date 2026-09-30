import { useEffect, useState } from "react";
import type { AuditEntry } from "../api/types";
import { humanize } from "../labels";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";
import { useUi } from "../state/ui";
import { Skeleton } from "../ui/motion-components";
import { EmptyState, ErrorState, PageHeader } from "../ui/primitives";
import { ago, fmtTime } from "./shared";

export function AuditView() {
  const store = useStudioStore();
  const dev = useUi((s) => s.devView);
  const [rows, setRows] = useState<AuditEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const meta = useStudio((s) => s.meta);
  const allowed = hasRole(meta, "admin");
  useEffect(() => {
    if (!allowed) return;
    let live = true;
    store.getState().api.audit().then((r) => live && setRows(r)).catch((e) => live && setError(String(e.message ?? e)));
    return () => { live = false; };
  }, [store]);
  if (!allowed) return <div className="view"><EmptyState icon="lock" title="Only admins can see the activity log">Ask an admin if you need to know who changed something.</EmptyState></div>;
  return (
    <div className="view">
      <PageHeader title="Activity log" subtitle="Who changed what, and when." />
      {error && <ErrorState title="Couldn’t load the activity log">{error}</ErrorState>}
      {!rows && !error && <Skeleton lines={6} />}
      {rows && rows.length === 0 && <EmptyState icon="file-text" title="Nothing recorded yet" />}
      {rows && rows.length > 0 && (
        <table className="table">
          <thead><tr><th>When</th><th>Who</th><th>What happened</th><th>About</th>{dev && <th>Raw action</th>}</tr></thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={i}>
                <td title={fmtTime(r.at)}>{ago(r.at)}</td>
                <td>{r.who}</td>
                <td>{humanize(r.action.replace(/[.:]/g, " "))}{r.detail ? <span className="muted"> — {r.detail}</span> : null}</td>
                <td className="mono">{r.target}</td>
                {dev && <td><code>{r.action}</code></td>}
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
