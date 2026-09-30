import { ChevronRight, Plus, Search } from "lucide-react";
import { memo, useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { CATEGORIES, blockInfo, count, lc, type CategoryId } from "../labels";
import { collectItems, summarize, type Item } from "../nav/model";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";
import { uiStore, useUi } from "../state/ui";
import { NoWorkingCopy } from "../components/NoWorkingCopy";
import { Presence, Skeleton, Stagger, StaggerItem } from "../ui/motion-components";
import { Badge, EmptyState, PageHeader } from "../ui/primitives";
import { typesIn } from "../labels";

export function CategoryView() {
  const { id = "" } = useParams();
  const cat = CATEGORIES.find((c) => c.id === id);
  const store = useStudioStore();
  const navigate = useNavigate();
  const nav = useStudio((s) => s.nav);
  const trees = useStudio((s) => s.trees);
  const draft = useStudio((s) => s.draft);
  const meta = useStudio((s) => s.meta);
  const diagnostics = useStudio((s) => s.diagnostics);
  const dev = useUi((s) => s.devView);
  const [q, setQ] = useState("");
  const [limit, setLimit] = useState(PAGE);
  const canEdit = hasRole(meta, "editor");

  useEffect(() => { void store.getState().ensureTrees(); }, [store, draft?.id, draft?.files.length]);
  useEffect(() => { setQ(""); setLimit(PAGE); }, [id]);
  const open = useCallback((it: Item) => { void store.getState().select(it.file, it.path); navigate("/edit"); }, [store, navigate]);

  const all = useMemo(() => collectItems(nav, trees).filter((i) => i.category === id), [nav, trees, id]);
  const loaded = !draft || draft.files.every((f) => trees[f]);
  const needle = q.trim().toLowerCase();
  const shown = useMemo(
    () => (needle ? all.filter((i) => `${i.name} ${blockInfo(i.type).label} ${summarize(i).chips.map((c) => c.text).join(" ")} ${summarize(i).line ?? ""}`.toLowerCase().includes(needle)) : all),
    [all, needle],
  );
  const problemsBy = useMemo(() => {
    const m = new Map<string, { e: number; w: number }>();
    for (const d of diagnostics) {
      if (!d.path) continue;
      const [t, ...rest] = d.path.split("/");
      const key = `${t}/${rest[0] ?? ""}`;
      const cur = m.get(key) ?? { e: 0, w: 0 };
      d.severity === "error" ? cur.e++ : cur.w++;
      m.set(key, cur);
    }
    return m;
  }, [diagnostics]);

  if (!cat) return <div className="view"><EmptyState icon="circle-help" title="That section doesn’t exist" /></div>;
  if (canEdit && !draft) return <div className="view"><NoWorkingCopy /></div>;
  const types = typesIn(cat.id as CategoryId);
  const many = new Set(all.map((i) => i.type)).size > 1 || types.length > 1;
  const main = types[0];

  return (
    <div className="view">
      <PageHeader
        icon={cat.icon}
        title={cat.label}
        subtitle={cat.blurb}
        actions={canEdit && cat.id !== "other" && (
          <button type="button" className="btn primary" onClick={() => uiStore.getState().openAdd(types.length === 1 || !many ? { category: cat.id, type: main } : { category: cat.id })}>
            <Plus size={16} /> Add {types.length === 1 ? lc(blockInfo(main).label) : "new"}
          </button>
        )}
      />

      <div className="list-toolbar">
        <label className="search-field"><Search size={15} /><input type="search" aria-label={`Search ${cat.label}`} placeholder={`Search ${lc(cat.label)}…`} value={q} onChange={(e) => setQ(e.target.value)} /></label>
        <span className="muted list-count">{count(shown.length, "item")}{needle && all.length !== shown.length ? ` of ${all.length}` : ""}</span>
      </div>

      {!loaded && all.length === 0 ? (
        <div className="table-skel"><Skeleton lines={6} /></div>
      ) : shown.length === 0 ? (
        <EmptyState icon={cat.icon} title={needle ? "Nothing matches your search" : `No ${lc(cat.label)} yet`} action={!needle && canEdit && cat.id !== "other" ? <button type="button" className="btn primary" onClick={() => uiStore.getState().openAdd({ category: cat.id })}><Plus size={16} /> Add one</button> : undefined}>
          {needle ? "Try different words." : cat.blurb}
        </EmptyState>
      ) : (
        <Stagger className="item-list" as="ul">
          <Presence mode="popLayout">
            {shown.slice(0, limit).map((it) => (
              <StaggerItem key={it.key} as="li">
                <ItemRow item={it} showType={many} primary={it.type === main} dev={dev} problems={problemsBy.get(`${it.type}/${it.id}`)} onOpen={open} />
              </StaggerItem>
            ))}
          </Presence>
        </Stagger>
      )}
      {shown.length > limit && (
        <div className="list-more"><button type="button" className="btn" onClick={() => setLimit((n) => n + PAGE)}>Show {Math.min(PAGE, shown.length - limit)} more</button></div>
      )}
    </div>
  );
}

/** Long lists render in pages so a big configuration never blocks the first paint. */
const PAGE = 200;

const ItemRow = memo(function ItemRow({ item, showType, primary, dev, problems, onOpen }: { item: Item; showType: boolean; primary: boolean; dev: boolean; problems?: { e: number; w: number }; onOpen(item: Item): void }) {
  const info = blockInfo(item.type);
  const s = summarize(item);
  return (
    <button type="button" className="item-row" onClick={() => onOpen(item)}>
      <span className="item-main">
        <span className="item-title">
          <strong>{item.name}</strong>
          {showType && !primary && <Badge tone="muted">{info.label}</Badge>}
          {dev && <code className="raw-name">{item.type} · {item.file}</code>}
        </span>
        {s.line && <span className="item-line">{s.line}</span>}
      </span>
      <span className="item-chips">
        {s.chips.map((c, i) => <Badge key={i} tone={c.tone} title={c.title} className={`${c.mono ? "mono" : ""}${c.dot ? " dot" : ""}`}>{c.text}</Badge>)}
      </span>
      <span className="item-flags">
        {problems && problems.e > 0 && <Badge tone="bad" className="dot">{count(problems.e, "problem")}</Badge>}
        {problems && problems.e === 0 && problems.w > 0 && <Badge tone="warn" className="dot">{count(problems.w, "warning")}</Badge>}
        <ChevronRight size={16} className="item-go" aria-hidden="true" />
      </span>
    </button>
  );
});
