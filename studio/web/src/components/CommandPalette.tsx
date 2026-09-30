import { CornerDownLeft, FileCode2, History, LayoutDashboard, Play, ScrollText, Search } from "lucide-react";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { CATEGORIES, blockInfo } from "../labels";
import { groupCommands, searchCommands, type Command } from "../nav/commands";
import { collectItems } from "../nav/model";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";
import { uiStore, useUi } from "../state/ui";
import { Icon } from "../ui/Icon";
import { motionOff } from "../ui/motion";
import { Kbd } from "../ui/primitives";
import { Pop, Presence, motion } from "../ui/motion-components";

export function CommandPalette({ onTheme }: { onTheme(): void }) {
  const open = useUi((s) => s.paletteOpen);
  return <Presence mode="sync">{open && <Palette key="palette" onTheme={onTheme} />}</Presence>;
}

function Palette({ onTheme }: { onTheme(): void }) {
  const store = useStudioStore();
  const navigate = useNavigate();
  const nav = useStudio((s) => s.nav);
  const trees = useStudio((s) => s.trees);
  const draft = useStudio((s) => s.draft);
  const meta = useStudio((s) => s.meta);
  const dev = useUi((s) => s.devView);
  const [q, setQ] = useState("");
  const [active, setActive] = useState(0);
  const listId = useId();
  const listRef = useRef<HTMLUListElement>(null);
  const close = () => uiStore.getState().setPalette(false);
  const canEdit = hasRole(meta, "editor");

  const commands = useMemo<Command[]>(() => {
    const go = (to: string) => () => { close(); navigate(to); };
    const list: Command[] = [];
    if (canEdit && draft) {
      list.push({ id: "go-overview", title: "Overview", group: "Go to", icon: "dash", run: go("/overview") });
      for (const c of CATEGORIES) list.push({ id: `go-${c.id}`, title: c.label, subtitle: c.blurb, group: "Go to", icon: c.icon, run: go(`/c/${c.id}`) });
      list.push({ id: "go-page-designs", title: "Page designs", subtitle: "How every screen looks: pages, layouts and reusable pieces.", group: "Go to", icon: "layout-template", keywords: "templates layouts components html css static files", run: go("/pages") });
    }
    if (canEdit && draft) {
      list.push({ id: "go-journeys", title: "User journeys", subtitle: "See what each button and link does, and where visitors end up.", group: "Go to", icon: "route", keywords: "flow map buttons links navigation pages api requests follow the user trace", run: go("/journeys") });
    }
    list.push({ id: "go-versions", title: "Versions & reviews", group: "Go to", icon: "history", keywords: "revisions history approve", run: go("/revisions") });
    if (hasRole(meta, "admin")) list.push({ id: "go-audit", title: "Activity log", group: "Go to", icon: "audit", keywords: "audit", run: go("/audit") });
    if (canEdit && draft) {
      for (const it of collectItems(nav, trees)) {
        const info = blockInfo(it.type);
        list.push({
          id: `item-${it.key}`, title: it.name, subtitle: info.label, group: "Things in this app", icon: info.icon, keywords: `${it.type} ${it.file}`,
          run: () => { close(); void store.getState().select(it.file, it.path); navigate("/edit"); },
        });
      }
      list.push(
        { id: "add-page", title: "Add a page or API endpoint", group: "Actions", icon: "action", keywords: "new create route", run: () => { close(); uiStore.getState().openAdd({ category: "pages", type: "route" }); } },
        { id: "add-conn", title: "Add a connection", group: "Actions", icon: "action", keywords: "new create resource database", run: () => { close(); uiStore.getState().openAdd({ category: "connections", type: "resource" }); } },
        { id: "add-any", title: "Add something new…", group: "Actions", icon: "action", keywords: "new create", run: () => { close(); uiStore.getState().openAdd({}); } },
        { id: "validate", title: "Check for problems", group: "Actions", icon: "action", keywords: "validate", run: () => { close(); void store.getState().validate(); uiStore.getState().showRight("problems"); } },
        { id: "undo", title: "Undo", group: "Actions", icon: "action", run: () => { close(); void store.getState().undo(); } },
        { id: "redo", title: "Redo", group: "Actions", icon: "action", run: () => { close(); void store.getState().redo(); } },
      );
      if (meta?.features.preview) list.push({ id: "preview", title: "Toggle live preview", group: "Actions", icon: "action", run: () => { close(); store.getState().setPreview(!store.getState().previewOn); } });
    }
    list.push(
      { id: "dev", title: dev ? "Turn off Developer view" : "Turn on Developer view", subtitle: "Show raw names, paths and source", group: "Actions", icon: "action", keywords: "technical raw bcl", run: () => { close(); uiStore.getState().toggleDev(); } },
      { id: "theme", title: "Switch light / dark theme", group: "Actions", icon: "action", run: () => { close(); onTheme(); } },
    );
    return list;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nav, trees, draft?.id, canEdit, dev, meta]);

  const results = useMemo(() => searchCommands(commands, q), [commands, q]);
  const groups = useMemo(() => groupCommands(results), [results]);
  useEffect(() => setActive(0), [q]);
  useEffect(() => {
    listRef.current?.querySelector<HTMLElement>('[aria-selected="true"]')?.scrollIntoView?.({ block: "nearest" });
  }, [active, results]);

  const flat = groups.flatMap(([, cs]) => cs);
  const run = (c?: Command) => c?.run();
  const off = motionOff();
  const Back = off ? "div" : motion.div;

  return (
    <Back className="backdrop palette-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) close(); }} {...(off ? {} : { initial: { opacity: 0 }, animate: { opacity: 1 }, exit: { opacity: 0 }, transition: { duration: 0.14 } })}>
      <Pop className="palette" role="dialog" aria-modal="true" aria-label="Search or jump to anything">
        <div className="palette-input">
          <Search size={18} />
          <input
            autoFocus
            role="combobox"
            aria-expanded="true"
            aria-controls={listId}
            aria-activedescendant={flat[active] ? `${listId}-${flat[active]!.id}` : undefined}
            aria-label="Search"
            placeholder="Search pages, connections, settings… or type a command"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") { e.preventDefault(); close(); }
              else if (e.key === "ArrowDown") { e.preventDefault(); setActive((a) => Math.min(flat.length - 1, a + 1)); }
              else if (e.key === "ArrowUp") { e.preventDefault(); setActive((a) => Math.max(0, a - 1)); }
              else if (e.key === "Enter") { e.preventDefault(); run(flat[active]); }
            }}
          />
          <Kbd>Esc</Kbd>
        </div>
        <ul className="palette-list" id={listId} role="listbox" ref={listRef}>
          {flat.length === 0 && <li className="palette-empty">Nothing matches “{q}”.</li>}
          {groups.map(([group, cs]) => (
            <li key={group} role="presentation">
              <div className="palette-group">{group}</div>
              <ul role="presentation">
                {cs.map((c) => {
                  const idx = flat.indexOf(c);
                  return (
                    <li key={c.id} id={`${listId}-${c.id}`} role="option" aria-selected={idx === active} className={`palette-item${idx === active ? " active" : ""}`} onMouseMove={() => setActive(idx)} onClick={() => run(c)}>
                      <span className="pi-icon">{paletteIcon(c)}</span>
                      <span className="pi-title">{c.title}</span>
                      {c.subtitle && <span className="pi-sub">{c.subtitle}</span>}
                      {idx === active && <CornerDownLeft size={14} className="pi-enter" />}
                    </li>
                  );
                })}
              </ul>
            </li>
          ))}
        </ul>
        <footer className="palette-foot"><span><Kbd>↑</Kbd> <Kbd>↓</Kbd> to move</span><span><Kbd>↵</Kbd> to open</span></footer>
      </Pop>
    </Back>
  );
}

function paletteIcon(c: Command) {
  switch (c.icon) {
    case "action": return <Play size={16} />;
    case "history": return <History size={16} />;
    case "audit": return <ScrollText size={16} />;
    case "dash": return <LayoutDashboard size={16} />;
    case "files": return <FileCode2 size={16} />;
    default: return <Icon name={c.icon} size={16} />;
  }
}
