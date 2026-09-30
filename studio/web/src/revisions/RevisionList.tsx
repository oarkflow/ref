import { ChevronRight } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import type { RevisionSummary } from "../api/types";
import { count } from "../labels";
import { useStudio, useStudioStore } from "../state/context";
import { useUi } from "../state/ui";
import { Skeleton } from "../ui/motion-components";
import { Badge, EmptyState, ErrorState, PageHeader } from "../ui/primitives";

export { StatusPill, ago, fmtTime } from "./shared";
import { StatusPill, ago, fmtTime } from "./shared";

export function RevisionList() {
  const store = useStudioStore();
  const navigate = useNavigate();
  const meta = useStudio((s) => s.meta);
  const dev = useUi((s) => s.devView);
  const [revs, setRevs] = useState<RevisionSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    store.getState().api.listRevisions().then((r) => live && setRevs(r)).catch((e) => live && setError(String(e.message ?? e)));
    return () => { live = false; };
  }, [store]);

  const waiting = revs?.filter((r) => r.status === "pending").length ?? 0;

  return (
    <div className="view">
      <PageHeader
        title="Versions & reviews"
        subtitle={waiting > 0 ? `${count(waiting, "version")} waiting for review.` : "Every change goes live as a version, after someone else approves it."}
      />
      {error && <ErrorState title="Couldn’t load versions">{error}</ErrorState>}
      {!revs && !error && <Skeleton lines={6} />}
      {revs && revs.length === 0 && <EmptyState icon="history" title="No versions yet">When you submit a working copy for review, it appears here.</EmptyState>}
      {revs && revs.length > 0 && (
        <table className="table versions-table">
          <thead>
            <tr><th className="col-num">Version</th><th>Description</th><th>Status</th><th>Author</th><th>Created</th><th aria-hidden="true" /></tr>
          </thead>
          <tbody>
            {revs.map((r) => {
              const live = meta?.activeRevision === r.id;
              return (
                <tr key={r.id} className={`row-link${live ? " live" : ""}`} onClick={() => navigate(`/revisions/${r.id}`)}>
                  <td className="col-num"><span className="v-num">#{r.seq}</span></td>
                  <td>
                    <Link to={`/revisions/${r.id}`} className="v-title" onClick={(e) => e.stopPropagation()}>{r.message || <span className="muted">No description</span>}</Link>
                    {dev && r.changed_files?.length ? <span className="v-files mono">{r.changed_files.map((f) => f.path).join(", ")}</span> : null}
                  </td>
                  <td>{live ? <Badge tone="ok" className="dot">Live now</Badge> : <StatusPill status={r.status} />}</td>
                  <td className="muted">{r.author}</td>
                  <td className="muted" title={fmtTime(r.created_at)}>{ago(r.created_at)}</td>
                  <td className="col-go"><ChevronRight size={16} className="v-go" /></td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      <p className="visually-hidden"><Link to="/overview">Back to overview</Link></p>
    </div>
  );
}
