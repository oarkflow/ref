import { lazy, Suspense, useEffect, useState } from "react";
import { HashRouter, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { RightPanel } from "./components/RightPanel";
import { Sidebar } from "./components/Sidebar";
import { Toasts } from "./components/Toasts";
import { TopBar } from "./components/TopBar";
import { usePreviewLayout } from "./preview/layout";
import { useStudio, useStudioStore } from "./state/context";
import { CategoryView } from "./views/CategoryView";
import { EditorView } from "./views/EditorView";
import { Overview } from "./views/Overview";
import { MotionRoot, PageTransition, Skeleton } from "./ui/motion-components";
import { useShortcuts } from "./Workspace";

// Routes that aren't needed for the first paint are split out and fetched on demand.
const CanvasPage = lazy(() => import("./canvas/CanvasPage").then((m) => ({ default: m.CanvasPage })));
const JourneysPage = lazy(() => import("./journeys/JourneysPage").then((m) => ({ default: m.JourneysPage })));
const PageList = lazy(() => import("./pages/PageList").then((m) => ({ default: m.PageList })));
const TemplateEditor = lazy(() => import("./pages/TemplateEditor").then((m) => ({ default: m.TemplateEditor })));
const FilesView = lazy(() => import("./views/FilesView").then((m) => ({ default: m.FilesView })));
const RevisionList = lazy(() => import("./revisions/RevisionList").then((m) => ({ default: m.RevisionList })));
const RevisionDetail = lazy(() => import("./revisions/RevisionDetail").then((m) => ({ default: m.RevisionDetail })));
const AuditView = lazy(() => import("./revisions/AuditView").then((m) => ({ default: m.AuditView })));
const PreviewPanel = lazy(() => import("./preview/PreviewPanel").then((m) => ({ default: m.PreviewPanel })));
const CommandPalette = lazy(() => import("./components/CommandPalette").then((m) => ({ default: m.CommandPalette })));
const AddWizardHost = lazy(() => import("./components/AddWizard").then((m) => ({ default: m.AddWizardHost })));

/** Same shape as the pages it stands in for, so nothing jumps when they arrive. */
function RouteSkeleton() {
  return (
    <div className="view" aria-busy="true" aria-label="Loading">
      <div className="skel-title"><Skeleton lines={2} /></div>
      <div className="table-skel"><Skeleton lines={8} /></div>
    </div>
  );
}

function useTheme(): [string, () => void] {
  const [theme, setTheme] = useState(() => {
    try {
      return localStorage.getItem("studio.theme") ?? (window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light");
    } catch {
      return "light";
    }
  });
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    try { localStorage.setItem("studio.theme", theme); } catch { /* private mode */ }
  }, [theme]);
  return [theme, () => setTheme((t) => (t === "dark" ? "light" : "dark"))];
}

export function SignIn() {
  const store = useStudioStore();
  const error = useStudio((s) => s.authError);
  const loading = useStudio((s) => s.loading);
  const [token, setToken] = useState("");
  return (
    <main className="signin">
      <form onSubmit={(e) => { e.preventDefault(); void store.getState().signIn(token.trim()); }} className="form-stack">
        <div className="signin-brand"><span className="brand-mark" aria-hidden="true">R</span><h1>REF Studio</h1></div>
        <p className="sub">Sign in with your access token to manage your app.</p>
        <label className="fld">
          <span className="fld-label">Access token</span>
          <input type="password" value={token} onChange={(e) => setToken(e.target.value)} autoComplete="off" autoFocus />
        </label>
        {error && <p className="problem error" role="alert">{error}</p>}
        <button type="submit" className="btn primary" disabled={!token.trim() || loading}>Sign in</button>
      </form>
    </main>
  );
}

/** Routes that have their own full-bleed layout and no side panel. */
const NO_SIDE_PANEL = (p: string) => p.startsWith("/canvas") || p.startsWith("/journeys") || p.startsWith("/pages") || p.startsWith("/revisions") || p === "/audit";

function Shell({ theme, onTheme }: { theme: string; onTheme(): void }) {
  const loc = useLocation();
  const draft = useStudio((s) => s.draft);
  const previewOn = useStudio((s) => s.previewOn);
  const dock = usePreviewLayout((l) => l.dock);
  const previewSize = usePreviewLayout((l) => (l.dock === "right" ? l.sizeRight : l.sizeBottom));
  useShortcuts();
  const showPreview = previewOn && !!draft;
  const routeKey = loc.pathname.startsWith("/canvas") ? "/canvas" : loc.pathname;
  const canvas = loc.pathname.startsWith("/canvas");

  return (
    <div className="app">
      <Sidebar theme={theme} onTheme={onTheme} />
      <div className="app-col">
        <TopBar />
        <div className="body">
          <div className="stage" style={showPreview ? ({ "--preview-size": `${previewSize}px` } as React.CSSProperties) : undefined}>
            <div className="stage-row">
              <main className="main" aria-label="Content">
                <PageTransition id={routeKey} className="route-wrap">
                  <Suspense fallback={<RouteSkeleton />}>
                  <Routes>
                    <Route path="/" element={<Navigate to="/overview" replace />} />
                    <Route path="/overview" element={<Overview />} />
                    <Route path="/c/:id" element={<CategoryView />} />
                    <Route path="/edit" element={<EditorView />} />
                    <Route path="/files/:file" element={<FilesView />} />
                    <Route path="/canvas" element={<CanvasPage />} />
                    <Route path="/journeys" element={<JourneysPage />} />
                    <Route path="/pages" element={<PageList />} />
                    <Route path="/pages/edit" element={<TemplateEditor />} />
                    <Route path="/revisions" element={<RevisionList />} />
                    <Route path="/revisions/:id" element={<RevisionDetail />} />
                    <Route path="/audit" element={<AuditView />} />
                    <Route path="*" element={<div className="view"><p className="empty">That page doesn’t exist.</p></div>} />
                  </Routes>
                  </Suspense>
                </PageTransition>
              </main>
              {showPreview && dock === "right" && <Suspense fallback={null}><PreviewPanel /></Suspense>}
              <RightPanel hidden={NO_SIDE_PANEL(loc.pathname) || canvas} />
            </div>
            {showPreview && dock === "bottom" && <Suspense fallback={null}><PreviewPanel /></Suspense>}
          </div>
        </div>
      </div>
      <Suspense fallback={null}>
        <CommandPalette onTheme={onTheme} />
        <AddWizardHost />
      </Suspense>
    </div>
  );
}

export function App() {
  const store = useStudioStore();
  const meta = useStudio((s) => s.meta);
  const token = useStudio((s) => s.token);
  const authError = useStudio((s) => s.authError);
  const [theme, toggleTheme] = useTheme();

  useEffect(() => {
    if (token) void store.getState().init();
  }, [store, token]);

  if (!meta) {
    return (
      <MotionRoot>
        {token && !authError ? <main className="page"><p aria-busy="true">Connecting…</p></main> : <SignIn />}
        <Toasts />
      </MotionRoot>
    );
  }

  return (
    <MotionRoot>
      <HashRouter>
        <Shell theme={theme} onTheme={toggleTheme} />
        <Toasts />
      </HashRouter>
    </MotionRoot>
  );
}
