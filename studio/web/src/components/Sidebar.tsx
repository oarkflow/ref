import { Bell, ChevronsLeft, ChevronsRight, Code2, FileCode2, History, LayoutDashboard, LogOut, Moon, Plus, ScrollText, Sun } from "lucide-react";
import { memo, useMemo } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { CATEGORIES, roleLabel, type CategoryId } from "../labels";
import { categoryCounts, collectItems } from "../nav/model";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";
import { DEFAULT_PREFS, LIMITS, uiStore, useUi } from "../state/ui";
import { Icon } from "../ui/Icon";
import { Menu, MenuItem, MenuSep } from "../ui/Menu";
import { motionOff, SPRING } from "../ui/motion";
import { LayoutGroup, motion } from "../ui/motion-components";
import { useNarrow } from "../ui/useNarrow";
import { useResize } from "../ui/useResize";
import type { IconName } from "../labels";

interface NavEntry {
  id: string;
  to: string;
  label: string;
  icon: IconName | "audit" | "history" | "dash";
  count?: number;
  active: boolean;
  add?: CategoryId;
}

/** How the categories are grouped in the sidebar: what you build, what runs it, what ships it. */
const GROUPS: { label: string; ids: CategoryId[] }[] = [
  { label: "Build", ids: ["pages", "flows", "data", "connections"] },
  { label: "Operate", ids: ["automation", "access", "settings", "other"] },
];

export function Sidebar({ theme, onTheme }: { theme: string; onTheme(): void }) {
  const store = useStudioStore();
  const navigate = useNavigate();
  const nav = useStudio((s) => s.nav);
  const trees = useStudio((s) => s.trees);
  const draft = useStudio((s) => s.draft);
  const meta = useStudio((s) => s.meta);
  const selection = useStudio((s) => s.selection);
  const connected = useStudio((s) => s.connected);
  const narrow = useNarrow();
  const collapsedPref = useUi((s) => s.sidebarCollapsed);
  const collapsed = collapsedPref || narrow;
  const width = useUi((s) => s.sidebarWidth);
  const dev = useUi((s) => s.devView);
  const loc = useLocation();
  const canEdit = hasRole(meta, "editor");

  const items = useMemo(() => collectItems(nav, trees), [nav, trees]);
  const counts = useMemo(() => categoryCounts(items), [items]);
  const selCategory = useMemo(() => items.find((i) => i.file === selection?.file && i.path === selection?.path)?.category, [items, selection]);

  const { dragging, handleProps } = useResize(
    () => uiStore.getState().sidebarWidth,
    (n) => uiStore.getState().resizeSidebar(n),
    1,
    () => uiStore.getState().set({ sidebarWidth: DEFAULT_PREFS.sidebarWidth }),
  );

  const path = loc.pathname;
  const overview: NavEntry | null = canEdit && draft ? { id: "overview", to: "/overview", label: "Overview", icon: "dash", active: path === "/overview" || path === "/" } : null;
  const groups = canEdit && draft
    ? GROUPS.map((g) => ({
        label: g.label,
        entries: g.ids
          .map((id) => CATEGORIES.find((c) => c.id === id)!)
          .filter((c) => !(c.id === "other" && counts.other === 0))
          .map<NavEntry>((c) => ({
            id: c.id, to: `/c/${c.id}`, label: c.label, icon: c.icon, count: counts[c.id], add: c.id === "other" ? undefined : c.id,
            active: path === `/c/${c.id}` || (path === "/edit" && selCategory === c.id),
          })),
      }))
    : [];
  if (groups[0]) {
    // "Page designs" sits right under "Pages & APIs": the addresses first, then how they look.
    const at = groups[0].entries.findIndex((e) => e.id === "pages");
    groups[0].entries.splice(at + 1, 0, { id: "page-designs", to: "/pages", label: "Page designs", icon: "layout-template", active: path.startsWith("/pages") });
    // ...and "User journeys" shows how those pages lead to each other and to the work behind them.
    groups[0].entries.splice(at + 2, 0, { id: "journeys", to: "/journeys", label: "User journeys", icon: "route", active: path.startsWith("/journeys") });
  }
  const release: NavEntry[] = [{ id: "versions", to: "/revisions", label: "Versions & reviews", icon: "history", active: path.startsWith("/revisions") }];
  if (hasRole(meta, "admin")) release.push({ id: "audit", to: "/audit", label: "Activity log", icon: "audit", active: path === "/audit" });

  const w = collapsed ? LIMITS.sidebar.rail : width;
  const off = motionOff();
  const Aside = off ? "aside" : motion.aside;
  const anim = off ? { style: { width: w } } : { initial: false, animate: { width: w }, transition: dragging ? { duration: 0 } : SPRING.panel };
  const initial = (meta?.identity.name ?? "?").slice(0, 1).toUpperCase();

  return (
    <Aside className={`sidebar${collapsed ? " collapsed" : ""}`} aria-label="Main navigation" {...(anim as object)}>
      <Link to="/" className="side-head" aria-label={`${meta?.app ?? "REF Studio"} home`}>
        <span className="brand-mark" aria-hidden="true">{(meta?.app ?? "R").slice(0, 1).toUpperCase()}</span>
        {!collapsed && (
          <span className="side-ws">
            <strong>{meta?.app ?? "REF Studio"}</strong>
            <small>REF Studio</small>
          </span>
        )}
      </Link>

      <LayoutGroup id="sidebar">
        <nav className="side-nav">
          {overview && <NavRow e={overview} collapsed={collapsed} canAdd={false} />}
          {groups.map((g) => (
            <div className="side-group" key={g.label} role="group" aria-label={g.label}>
              {collapsed ? <div className="side-sep" role="separator" /> : <h3 className="side-label">{g.label}</h3>}
              {g.entries.map((e) => <NavRow key={e.id} e={e} collapsed={collapsed} canAdd={canEdit && !!e.add} />)}
            </div>
          ))}
          <div className="side-group" role="group" aria-label="Release">
            {collapsed ? <div className="side-sep" role="separator" /> : <h3 className="side-label">Release</h3>}
            {release.map((e) => <NavRow key={e.id} e={e} collapsed={collapsed} canAdd={false} />)}
          </div>
        </nav>

        {dev && draft && !collapsed && (
          <div className="side-files" aria-label="Files">
            <h3 className="side-label">Files</h3>
            <ul>
              {[...draft.files].sort().map((f) => (
                <li key={f}>
                  <Link to={`/files/${encodeURIComponent(f)}`} className={`side-file${path === `/files/${encodeURIComponent(f)}` ? " active" : ""}`}>
                    <FileCode2 size={14} /> <span>{f}</span>
                  </Link>
                </li>
              ))}
            </ul>
          </div>
        )}
      </LayoutGroup>

      <footer className="side-foot">
        {!collapsed && draft && (
          <span className={`conn side-status ${connected ? "on" : ""}`} role="status">
            <span className="dot" /> {connected ? "Live updates on" : "Reconnecting…"}
          </span>
        )}
        <div className="side-user">
          <Menu label="Account menu" up className="user-btn" trigger={
            <>
              <span className="avatar" aria-hidden="true">{initial}</span>
              {!collapsed && (
                <span className="user-text">
                  <strong>{meta?.identity.name}</strong>
                  <small>{meta?.identity.roles.map(roleLabel).join(", ")}</small>
                </span>
              )}
            </>
          }>
            {() => (
              <>
                <div className="menu-user">
                  <strong>{meta?.identity.name}</strong>
                  <span className="muted">{meta?.identity.roles.map(roleLabel).join(", ")}</span>
                </div>
                <MenuSep />
                <MenuItem onClick={onTheme} hint={theme === "dark" ? "Dark" : "Light"}>{theme === "dark" ? <Sun size={16} /> : <Moon size={16} />} Switch theme</MenuItem>
                <MenuItem onClick={() => uiStore.getState().toggleDev()} hint={dev ? "On" : "Off"}><Code2 size={16} /> Developer view</MenuItem>
                <MenuItem onClick={() => navigate("/revisions")}><Bell size={16} /> Versions &amp; reviews</MenuItem>
                <MenuSep />
                <MenuItem onClick={() => store.getState().signOut()}><LogOut size={16} /> Sign out</MenuItem>
              </>
            )}
          </Menu>
          {!narrow && (
            <button type="button" className="icon-btn side-collapse" onClick={() => uiStore.getState().toggleSidebar()} aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"} data-tip={collapsed ? "Expand" : "Collapse"}>
              {collapsed ? <ChevronsRight size={16} /> : <ChevronsLeft size={16} />}
            </button>
          )}
        </div>
      </footer>

      {!collapsed && !narrow && (
        <div
          className={`resize-edge${dragging ? " active" : ""}`}
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize sidebar"
          aria-valuenow={width}
          aria-valuemin={LIMITS.sidebar.min}
          aria-valuemax={LIMITS.sidebar.max}
          tabIndex={0}
          {...handleProps}
        />
      )}
    </Aside>
  );
}

const NavRow = memo(function NavRow({ e, collapsed, canAdd }: { e: NavEntry; collapsed: boolean; canAdd: boolean }) {
  const icon =
    e.icon === "audit" ? <ScrollText size={16} /> :
    e.icon === "history" ? <History size={16} /> :
    e.icon === "dash" ? <LayoutDashboard size={16} /> :
    <Icon name={e.icon} size={16} />;
  const off = motionOff();
  return (
    <div className={`nav-row${e.active ? " active" : ""}`}>
      {e.active && (off ? <span className="nav-pill" aria-hidden="true" /> : <motion.span layoutId="nav-pill" className="nav-pill" transition={SPRING.layout} aria-hidden="true" />)}
      <Link to={e.to} className="nav-link" aria-current={e.active ? "page" : undefined} data-tip={collapsed ? e.label : undefined} aria-label={collapsed ? e.label : undefined}>
        <span className="nav-icon">{icon}</span>
        {!collapsed && <span className="nav-label">{e.label}</span>}
        {!collapsed && e.count !== undefined && <span className="nav-count">{e.count}</span>}
      </Link>
      {canAdd && !collapsed && e.add && (
        <button type="button" className="nav-add icon-btn" aria-label={`Add to ${e.label}`} data-tip="Add" onClick={() => uiStore.getState().openAdd({ category: e.add })}>
          <Plus size={14} />
        </button>
      )}
    </div>
  );
});
