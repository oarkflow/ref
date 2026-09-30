import { ArrowRight, CheckCircle2, Eye, OctagonAlert, Plus, TriangleAlert } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import type { DraftDiff, RevisionSummary } from "../api/types";
import { ChangeList } from "../components/ChangeList";
import { NoWorkingCopy } from "../components/NoWorkingCopy";
import { CATEGORIES, count, friendlyProblem, groupProblems, problemWhere, statusInfo } from "../labels";
import { categoryCounts, collectItems } from "../nav/model";
import { ago } from "../revisions/shared";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";
import { uiStore } from "../state/ui";
import { Icon } from "../ui/Icon";
import { AnimatedNumber, Skeleton, Stagger, StaggerItem } from "../ui/motion-components";
import { Badge, Card, EmptyState, PageHeader } from "../ui/primitives";

export function Overview() {
  const store = useStudioStore();
  const navigate = useNavigate();
  const meta = useStudio((s) => s.meta);
  const draft = useStudio((s) => s.draft);
  const nav = useStudio((s) => s.nav);
  const trees = useStudio((s) => s.trees);
  const diagnostics = useStudio((s) => s.diagnostics);
  const previewOn = useStudio((s) => s.previewOn);
  const [revs, setRevs] = useState<RevisionSummary[] | null>(null);
  const [diff, setDiff] = useState<DraftDiff | null>(null);
  const canEdit = hasRole(meta, "editor");

  const items = useMemo(() => collectItems(nav, trees), [nav, trees]);
  const counts = useMemo(() => categoryCounts(items), [items]);
  const errors = diagnostics.filter((d) => d.severity === "error");
  const warnings = diagnostics.length - errors.length;

  useEffect(() => {
    let live = true;
    store.getState().api.listRevisions(6).then((r) => live && setRevs(r)).catch(() => live && setRevs([]));
    return () => { live = false; };
  }, [store]);
  useEffect(() => {
    if (!draft) return;
    let live = true;
    const t = setTimeout(() => store.getState().api.diff(draft.id).then((d) => live && setDiff(d)).catch(() => live && setDiff({ files: [], changes: [] })), 200);
    return () => { live = false; clearTimeout(t); };
  }, [store, draft?.id, draft?.version]);

  if (!canEdit) return <div className="view"><EmptyState icon="lock" title="You can view versions, not edit">Your role doesn’t include editing. Open <Link to="/revisions">Versions &amp; reviews</Link> to follow what’s changing.</EmptyState></div>;
  if (!draft) return <div className="view"><NoWorkingCopy /></div>;

  const live = revs?.find((r) => r.status === "active");
  const changeCount = diff?.changes.length ?? 0;
  const attention = groupProblems(diagnostics).slice(0, 4);

  return (
    <div className="view overview">
      <PageHeader
        title={meta?.app ?? "Overview"}
        subtitle={<>You’re working in <strong>{draft.name}</strong>. {draft.dirty ? "It has changes that haven’t been sent for review." : "It matches the live version so far."}</>}
        actions={
          <>
            <button type="button" className="btn" onClick={() => uiStore.getState().openAdd({})}><Plus size={16} /> Add something</button>
          </>
        }
      />

      <section className="status-strip" aria-label="Status">
        <div className="status-cell">
          <span className="status-k">Working copy</span>
          <span className="status-v">{draft.name}</span>
          <span className="status-s">{draft.dirty ? (changeCount ? `${count(changeCount, "change")} not sent yet` : "Unsent changes") : "Same as the live version"}</span>
        </div>
        <div className="status-cell">
          <span className="status-k">Live version</span>
          {live ? (
            <>
              <span className="status-v"><Link to={`/revisions/${live.id}`}>#{live.seq}</Link> <Badge tone="ok" className="dot">Live</Badge></span>
              <span className="status-s">{live.message || "No description"}{live.activated_at ? ` · since ${ago(live.activated_at)}` : ""}</span>
            </>
          ) : (
            <>
              <span className="status-v">{revs ? "Nothing yet" : "…"}</span>
              <span className="status-s">Submit a working copy to publish the first version.</span>
            </>
          )}
        </div>
        <div className="status-cell">
          <span className="status-k">Problems</span>
          {diagnostics.length === 0 ? (
            <>
              <span className="status-v ok"><CheckCircle2 size={16} /> All clear</span>
              <span className="status-s">Everything checks out.</span>
            </>
          ) : (
            <>
              <span className="status-v">
                {errors.length > 0 ? <Badge tone="bad" className="dot">{errors.length} to fix</Badge> : <Badge tone="ok" className="dot">Nothing to fix</Badge>}
                {warnings > 0 && <Badge tone="warn" className="dot">{warnings} to look at</Badge>}
              </span>
              <button type="button" className="link-btn status-s" onClick={() => uiStore.getState().showRight("problems")}>Open problems</button>
            </>
          )}
        </div>
      </section>

      <Stagger className="glance" as="ul">
        {CATEGORIES.filter((c) => c.id !== "other" || counts.other > 0).map((c) => (
          <StaggerItem key={c.id} as="li">
            <Link to={`/c/${c.id}`} className="glance-cell" aria-label={`${c.label}: ${counts[c.id]}`}>
              <span className="glance-num"><AnimatedNumber value={counts[c.id]} /></span>
              <span className="glance-label"><Icon name={c.icon} size={14} /> {c.label}</span>
            </Link>
          </StaggerItem>
        ))}
      </Stagger>

      <div className="ov-grid">
        <div className="ov-col">
          <Card title="Needs your attention" actions={diagnostics.length > 0 ? <button type="button" className="link-btn" onClick={() => uiStore.getState().showRight("problems")}>Open problems</button> : undefined}>
            {attention.length === 0 ? (
              <div className="ov-ok"><CheckCircle2 size={20} /> <div><strong>Everything checks out</strong><p className="muted">No problems in this working copy.</p></div></div>
            ) : (
              <ul className="ov-problems">
                {attention.map((e, i) => e.kind === "group" ? (
                  <li key={`g${i}`}>
                    <button type="button" className="ov-problem" onClick={() => uiStore.getState().showRight("problems")}>
                      <TriangleAlert size={16} className="ov-sev warn" />
                      <span className="ov-problem-text"><span>{e.title}</span><span className="muted">{e.help}</span></span>
                      <ArrowRight size={14} className="ov-go" />
                    </button>
                  </li>
                ) : (
                  <li key={`s${i}`}>
                    <button type="button" className="ov-problem" onClick={() => { void store.getState().selectFromDiagnostic(e.d); navigate("/edit"); }}>
                      {e.d.severity === "error" ? <OctagonAlert size={16} className="ov-sev bad" /> : <TriangleAlert size={16} className="ov-sev warn" />}
                      <span className="ov-problem-text"><span>{friendlyProblem(e.d).text}</span>{problemWhere(e.d) && <span className="muted">{problemWhere(e.d)}</span>}</span>
                      <ArrowRight size={14} className="ov-go" />
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card title="Your changes" actions={changeCount > 0 ? <button type="button" className="link-btn" onClick={() => uiStore.getState().showRight("changes")}>See all</button> : undefined}>
            {!diff ? <Skeleton lines={3} /> : changeCount === 0 ? <p className="muted">No changes yet. Pick something on the left, or add something new.</p> : <ChangeList changes={diff.changes.slice(0, 5)} />}
          </Card>
        </div>

        <div className="ov-col">
          <Card title="Recent versions" actions={<Link to="/revisions" className="link-btn">All versions</Link>}>
            {!revs ? <Skeleton lines={4} /> : revs.length === 0 ? <p className="muted">Nothing has been submitted yet.</p> : (
              <ol className="timeline">
                {revs.slice(0, 5).map((r) => {
                  const st = statusInfo(r.status);
                  return (
                    <li key={r.id} className={`tl-item tl-${st.tone}`}>
                      <span className="tl-dot" aria-hidden="true" />
                      <Link to={`/revisions/${r.id}`} className="tl-body">
                        <span className="tl-title"><strong>#{r.seq}</strong> {r.message || "No description"}</span>
                        <span className="tl-meta"><Badge tone={st.tone} className="dot">{st.label}</Badge> {r.author} · {ago(r.created_at)}</span>
                      </Link>
                    </li>
                  );
                })}
              </ol>
            )}
          </Card>

          <Card title="Quick start">
            <div className="quick-grid-x">
              <button type="button" className="quick" onClick={() => uiStore.getState().openAdd({ category: "pages", type: "route" })}>
                <span className="quick-icon"><Icon name="globe" size={16} /></span>
                <span className="quick-text"><strong>Add a page or API endpoint</strong><span>A new address people or systems can visit.</span></span>
              </button>
              <button type="button" className="quick" onClick={() => uiStore.getState().openAdd({ category: "connections", type: "resource" })}>
                <span className="quick-icon"><Icon name="plug" size={16} /></span>
                <span className="quick-text"><strong>Add a connection</strong><span>A database, cache, email or another service.</span></span>
              </button>
              <button type="button" className="quick" disabled={!meta?.features.preview} onClick={() => store.getState().setPreview(!previewOn)}>
                <span className="quick-icon"><Eye size={16} /></span>
                <span className="quick-text"><strong>{previewOn ? "Hide live preview" : "Open live preview"}</strong><span>See your changes running before anyone reviews them.</span></span>
              </button>
            </div>
          </Card>
        </div>
      </div>

    </div>
  );
}
