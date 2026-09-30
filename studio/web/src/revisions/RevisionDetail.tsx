import { ArrowLeft, Check, History, Rocket, Undo2, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import type { Revision } from "../api/types";
import { diffLines, withContext } from "../lib/diff";
import { useStudio, useStudioStore } from "../state/context";
import { ChangeList, ChangeSummary } from "../components/ChangeList";
import { hasRole } from "../state/store";
import { useUi } from "../state/ui";
import { Disclosure } from "../ui/Disclosure";
import { Skeleton } from "../ui/motion-components";
import { Badge, Card, ErrorState, PageHeader } from "../ui/primitives";
import { statusInfo } from "../labels";
import { DiffView } from "./DiffView";
import { StatusPill, ago, fmtTime } from "./shared";

/** Per-file changes between a revision and its base. */
export function fileDiffs(rev: Revision, base: Revision | null) {
  const cur = filesOf(rev);
  const old = base ? filesOf(base) : new Map<string, string>();
  const names = [...new Set([...cur.keys(), ...old.keys()])].sort();
  const out: { name: string; status: "added" | "modified" | "removed"; lines: ReturnType<typeof diffLines> }[] = [];
  for (const n of names) {
    const a = old.get(n);
    const b = cur.get(n);
    if (a === b) continue;
    out.push({
      name: n,
      status: a === undefined ? "added" : b === undefined ? "removed" : "modified",
      lines: withContext(diffLines(a ?? "", b ?? ""), 3),
    });
  }
  return out;
}

function filesOf(r: Revision): Map<string, string> {
  const m = new Map<string, string>();
  if (r.files?.length) for (const f of r.files) m.set(f.path, f.content);
  else if (r.source !== undefined) m.set("main.bcl", r.source);
  return m;
}

export function RevisionDetail() {
  const { id = "" } = useParams();
  const store = useStudioStore();
  const meta = useStudio((s) => s.meta);
  const dev = useUi((s) => s.devView);
  const [rev, setRev] = useState<Revision | null>(null);
  const [base, setBase] = useState<Revision | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [comment, setComment] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const api = store.getState().api;
      const r = await api.getRevision(id);
      setRev(r);
      setBase(r.base_id ? await api.getRevision(r.base_id).catch(() => null) : null);
      setError(null);
    } catch (e) {
      setError(String((e as Error).message ?? e));
    }
  }, [id, store]);
  useEffect(() => { void load(); }, [load]);

  const diffs = useMemo(() => (rev ? fileDiffs(rev, base) : []), [rev, base]);

  if (error) return <div className="view"><ErrorState title="Couldn’t load this version" onRetry={() => void load()}>{error}</ErrorState></div>;
  if (!rev) return <div className="view"><Skeleton title lines={6} /></div>;

  const me = meta?.identity.name;
  const isAuthor = me === rev.author;
  const reviewer = hasRole(meta, "reviewer");
  const admin = hasRole(meta, "admin");
  const act = async (name: string, fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
      store.getState().notify("success", `${name} done.`);
      await load();
      void store.getState().init().catch(() => {});
    } catch (e) {
      store.getState().notify("error", `${name} didn’t work: ${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };
  const api = store.getState().api;
  const st = statusInfo(rev.status);
  const canReview = (reviewer || admin) && (rev.status === "pending" || rev.status === "approved" || (rev.status === "superseded" && admin));

  return (
    <div className="view revision">
      <PageHeader
        icon="history"
        crumbs={<><Link to="/revisions"><ArrowLeft size={14} /> All versions</Link></>}
        title={<>Version #{rev.seq} <StatusPill status={rev.status} /></>}
        subtitle={<>{rev.message || <span className="muted">No description</span>}</>}
      />
      <p className="rev-meta muted">Submitted by {rev.author} · {ago(rev.created_at)}{rev.activated_at ? ` · live since ${fmtTime(rev.activated_at)}` : ""}</p>
      {rev.failure && <p className="problem error" role="alert">This version couldn’t start: {rev.failure}. The previous version is still running.</p>}

      <div className="rev-grid">
        <div className="rev-main">
          <Card title="What changed">
            {rev.changes?.length ? <><ChangeSummary changes={rev.changes} /><ChangeList changes={rev.changes} limit={8} /></> : <p className="muted">No changes to settings in this version.</p>}
          </Card>

          {(dev || diffs.length > 0) && (
            <Card title={`Files${base ? ` compared with version #${base.seq}` : ""}`} className="rev-files">
              {diffs.length === 0 ? <p className="muted">No file changes.</p> : diffs.map((d) => <DiffView key={d.name} name={d.name} status={d.status} lines={d.lines} />)}
            </Card>
          )}
        </div>

        <aside className="rev-side">
          <Card title="Review">
            <p className="rev-status"><Badge tone={st.tone}>{st.label}</Badge> <span className="muted">{st.help}</span></p>
            {canReview && (
              <div className="rev-actions">
                {rev.status === "pending" && (
                  <>
                    <label className="fld">
                      <span className="fld-label">Comment (optional)</span>
                      <input value={comment} onChange={(e) => setComment(e.target.value)} placeholder="Add a note for the author" />
                    </label>
                    <div className="progress" hidden={!busy} aria-hidden="true" />
                    <button type="button" className="btn primary" disabled={busy || !reviewer || isAuthor} title={isAuthor ? "You can’t approve your own version" : undefined}
                      onClick={() => void act("Approve", () => api.approve(rev.id, comment))}><Check size={16} /> Approve</button>
                    <button type="button" className="btn danger" disabled={busy || !reviewer} onClick={() => void act("Decline", () => api.reject(rev.id, comment))}><X size={16} /> Decline</button>
                  </>
                )}
                {rev.status === "approved" && (
                  <button type="button" className="btn primary" disabled={busy || !reviewer} onClick={() => void act("Publish", () => api.activate(rev.id))}><Rocket size={16} /> Make it live</button>
                )}
                {rev.status === "superseded" && admin && (
                  <button type="button" className="btn" disabled={busy} onClick={() => void act("Go back", () => api.rollback(rev.id, comment || "rollback from studio"))}><Undo2 size={16} /> Go back to this version</button>
                )}
              </div>
            )}
            {rev.status === "pending" && isAuthor && <p className="hint">Someone other than you needs to approve this before it can go live.</p>}
            {rev.rejection && <p className="problem error">Declined by {rev.rejection.by}{rev.rejection.comment ? `: ${rev.rejection.comment}` : ""}</p>}
          </Card>

          {rev.approvals?.length ? (
            <Card title="Approved by">
              <ul className="approvals">{rev.approvals.map((a, i) => <li key={i}><Check size={15} className="ok-icon" /> <span><strong>{a.by}</strong> · {ago(a.at)}{a.comment ? <span className="muted"> — {a.comment}</span> : null}</span></li>)}</ul>
            </Card>
          ) : null}

          {dev && (
            <Card title="Technical details">
              <dl className="tech">
                <dt>Checksum</dt><dd className="mono">{rev.checksum ?? "—"}</dd>
                <dt>Based on</dt><dd className="mono">{rev.base_id ?? "—"}</dd>
                <dt>ID</dt><dd className="mono">{rev.id}</dd>
              </dl>
            </Card>
          )}
          {!dev && rev.warnings?.length ? (
            <Disclosure title={<><History size={15} /> Notes from the checker</>} defaultOpen={false}>
              <ul>{rev.warnings.map((w, i) => <li key={i}>{w}</li>)}</ul>
            </Disclosure>
          ) : null}
        </aside>
      </div>
    </div>
  );
}
