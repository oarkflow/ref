import { useMemo, useState } from "react";
import type { Op } from "../api/types";
import { defFromNew, defFromRaw, setDeps, testSet, type SetDef, type TestResult } from "./model";
import { useStudio } from "../state/context";
import { addLookupOp, addSetOp, connectSourceOp, createServiceOps, createSourceOps, findCasesResource, hostOf, parseConnections, type DesignForm, type LookupSourceModel, type NewSet, type SourceKind, type WorkspaceModel, type WorkspaceNode } from "./model";
import type { BlockNode } from "../api/types";

interface Props { workspaces: WorkspaceModel[]; sources: LookupSourceModel[]; trees: Record<string, BlockNode[]>; pipelineName: string; pipelineFile: string; forms: DesignForm[]; canEdit: boolean; apply(ops: Op[]): void }

/** The workspace hierarchy, the lookups defined on it, and which fields use each lookup. */
export function WorkspacePanel({ workspaces, sources, trees, pipelineName, pipelineFile, forms, canEdit, apply }: Props) {
  return (
    <div className="dz-layout">
      <SourcesSection sources={sources} trees={trees} pipelineName={pipelineName} pipelineFile={pipelineFile} forms={forms} canEdit={canEdit} apply={apply} />
      {workspaces.length === 0
        ? <p className="dz-hint">This app has no workspace hierarchy (an <code>org.hierarchy</code> resource), so there are no workspaces to manage; lookups can still come from the sources above.</p>
        : workspaces.map((w) => <Workspace key={w.resourcePath + w.file} w={w} forms={forms} canEdit={canEdit} apply={apply} />)}
    </div>
  );
}

const KIND_LABEL: Record<string, string> = { static: "Fixed list", sql: "Database query", http: "HTTP API" };

/** Where lookup choices can come from: a fixed list, a database query or an HTTP API. */
function SourcesSection({ sources, trees, pipelineName, pipelineFile, forms, canEdit, apply }: Omit<Props, "workspaces">) {
  const conns = useMemo(() => parseConnections(trees), [trees]);
  const [target, setTarget] = useState(sources[0]?.name ?? "");
  const [kind, setKind] = useState<SourceKind>("static");
  const [f, setF] = useState({ name: "", entries: "", query: "", args: "", path: "", itemsPath: "", valueField: "", labelField: "", ttl: "", method: "GET", params: "", apiName: "", apiUrl: "", apiPrivate: false });
  const [dbPick, setDbPick] = useState("");
  const [svcPick, setSvcPick] = useState("");
  const [tested, setTested] = useState<{ key: string; ok: boolean } | null>(null);
  const set = (k: keyof typeof f, v: string | boolean) => setF((x) => ({ ...x, [k]: v }));
  const src = sources.find((s) => s.name === target) ?? sources[0];
  const cases = findCasesResource(trees, pipelineName);
  const usedBy = (s: string) => forms.flatMap((fm) => fm.inputs.filter((i) => i.lookup === s).map((i) => `${fm.title || fm.name} › ${i.label || i.name}`));
  const fieldNames = [...new Set(forms.flatMap((fm) => fm.inputs.map((i) => i.name)))];

  // What a set of this kind needs connected: an existing connection of the source, or one chosen here.
  const dbName = src?.database || dbPick || conns.databases[0] || "";
  const newApi = svcPick === "__new" || (!src?.service && conns.services.length === 0);
  const svcName = src?.service || (newApi ? f.apiName.trim() : svcPick || conns.services[0]?.name || "");
  const apiOk = !newApi || (/^[a-z][a-z0-9_]*$/.test(f.apiName.trim()) && !!hostOf(f.apiUrl));
  const connOk = kind === "static" || (kind === "sql" && !!dbName) || (kind === "http" && !!svcName && apiOk);

  const draft = (): NewSet | null => {
    const n = f.name.trim().toLowerCase().replace(/[^a-z0-9_]+/g, "_");
    if (!n) return null;
    const base = { name: n, type: kind, ttl: f.ttl.trim() || undefined };
    if (kind === "static") {
      const list = f.entries.split("\n").map((l) => l.trim()).filter(Boolean).map((l) => { const [v, ...rest] = l.split("="); return { value: v!.trim(), label: rest.join("=").trim() }; });
      return list.length ? { ...base, entries: list } : null;
    }
    if (kind === "sql") return f.query.trim() ? { ...base, query: f.query.trim(), args: f.args.split(",").map((x) => x.trim()).filter(Boolean) } : null;
    const params = f.params.split("\n").map((l) => l.trim()).filter(Boolean).map((l) => { const [k, ...rest] = l.split("="); return { name: k!.trim(), value: rest.join("=").trim() }; }).filter((p) => /^[A-Za-z_][\w.-]*$/.test(p.name));
    return f.path.trim() ? { ...base, path: f.path.trim(), itemsPath: f.itemsPath.trim(), valueField: f.valueField.trim(), labelField: f.labelField.trim(), method: f.method, params } : null;
  };
  const d = draft();
  // A test result belongs to the exact set and connections it was run with.
  const testKey = d ? JSON.stringify([defFromNew(d), kind === "sql" ? dbName : "", kind === "http" ? svcName : ""]) : "";
  const status: "ok" | "failed" | "untested" = tested && tested.key === testKey ? (tested.ok ? "ok" : "failed") : "untested";
  const submit = () => {
    if (!d || !connOk) return;
    const ops: Op[] = [];
    if (kind === "http" && newApi) ops.push(...createServiceOps(pipelineFile, f.apiName.trim(), f.apiUrl, f.apiPrivate));
    if (src) {
      if (kind === "sql" && !src.database) ops.push(connectSourceOp(src, "database", dbName));
      if (kind === "http" && !src.service) ops.push(connectSourceOp(src, "service", svcName));
      ops.push(addSetOp(src, d));
    } else if (cases) {
      ops.push(...createSourceOps(pipelineFile, "lookup_catalog", cases, d, { database: kind === "sql" ? dbName : undefined, service: kind === "http" ? svcName : undefined }));
    }
    apply(ops);
    setF((x) => ({ ...x, name: "", entries: "", query: "", args: "", path: "", itemsPath: "", valueField: "", labelField: "", params: "", apiName: "", apiUrl: "" }));
    setSvcPick("");
  };
  return (
    <div className="dz-layoutgrid">
      <section className="dz-groups" aria-label="Lookup sources">
        <h3>Lookup sources</h3>
        <p className="dz-hint">Fields with a lookup get their choices from these sets. A set can be a fixed list, a database query or an HTTP API, and can be narrowed by the fields it depends on. Sets not defined here fall back to the workspace lookups below.</p>
        {sources.length === 0 && <p className="dz-hint">No lookup source is set up for this app yet.{cases ? " Add a set to create one for this pipeline." : " It needs a pipeline.cases resource that lists this pipeline."}</p>}
        {sources.map((s) => (
          <div key={s.file + s.name} className="dz-lookup">
            <h4>{s.name} <small className="dz-hint">{[s.database && `database: ${s.database}`, s.service && `API: ${s.service}`].filter(Boolean).join(" · ") || "fixed lists only"}</small></h4>
            <table>
              <thead><tr><th>Set</th><th>Kind</th><th>Detail</th><th>Used by</th><th /></tr></thead>
              <tbody>
                {s.sets.map((x) => (
                  <SetRow key={x.name} source={s} set={x} usedBy={usedBy(x.name).join("; ") || "—"} />
                ))}
              </tbody>
            </table>
          </div>
        ))}
      </section>
      {canEdit && (src || cases) && (
        <section className="dz-lpreview" aria-label="Add a lookup set">
          <h3>Add a lookup set</h3>
          {sources.length > 1 && (
            <label className="dz-inline">Into <select value={src?.name} onChange={(e) => setTarget(e.target.value)}>{sources.map((s) => <option key={s.name} value={s.name}>{s.name}</option>)}</select></label>
          )}
          <div className="dz-propform">
            <label>Name<input value={f.name} onChange={(e) => set("name", e.target.value)} placeholder="payers" /></label>
            <label>Source
              <select value={kind} onChange={(e) => setKind(e.target.value as SourceKind)}>
                {(Object.keys(KIND_LABEL) as SourceKind[]).map((k) => <option key={k} value={k}>{KIND_LABEL[k]}</option>)}
              </select>
            </label>
            {kind === "static" && <label>Entries (one per line: value = label)<textarea rows={5} value={f.entries} onChange={(e) => set("entries", e.target.value)} placeholder={"F = Female\nM = Male"} /></label>}
            {kind === "sql" && (
              <>
                {src?.database ? <p className="dz-hint">Reads from the database connection <code>{src.database}</code>.</p> : conns.databases.length === 0
                  ? <p className="dz-hint err">This app has no database connection to query.</p>
                  : <label>Database connection<select value={dbName} onChange={(e) => setDbPick(e.target.value)}>{conns.databases.map((n) => <option key={n} value={n}>{n}</option>)}</select></label>}
                <label>Query (one SELECT returning columns named value and label)<textarea rows={4} value={f.query} onChange={(e) => set("query", e.target.value)} placeholder="SELECT code AS value, label FROM em_codes WHERE mdm_level = ?" /></label>
                <label>Query parameters, in order: the fields it depends on (comma separated)<input list="dz-fields" value={f.args} onChange={(e) => set("args", e.target.value)} placeholder="mdm_level" /></label>
                <p className="dz-hint">Only reads are allowed. Put <code>?</code> where each parameter goes (or <code>$1</code> for PostgreSQL).</p>
              </>
            )}
            {kind === "http" && (
              <>
                {src?.service ? <p className="dz-hint">Calls the API connection <code>{src.service}</code>; its allowed hosts apply.</p> : (
                  <label>API connection
                    <select value={newApi ? "__new" : svcName} onChange={(e) => setSvcPick(e.target.value)}>
                      {conns.services.map((s) => <option key={s.name} value={s.name}>{s.name}{s.baseUrl ? ` · ${s.baseUrl}` : ""}</option>)}
                      <option value="__new">Connect a new API…</option>
                    </select>
                  </label>
                )}
                {newApi && !src?.service && (
                  <div className="dz-esc">
                    <b>New API connection</b>
                    <label>Name<input value={f.apiName} onChange={(e) => set("apiName", e.target.value)} placeholder="payer_api" /></label>
                    <label>Base URL<input value={f.apiUrl} onChange={(e) => set("apiUrl", e.target.value)} placeholder="https://api.example.com/v1" /></label>
                    <small className="dz-hint">Only <code>{hostOf(f.apiUrl) || "the host of this URL"}</code> will be reachable. Credentials are not stored here.</small>
                    <label className="dz-inline"><input type="checkbox" checked={f.apiPrivate} onChange={(e) => set("apiPrivate", e.target.checked)} /> Allow a private or local address (development only)</label>
                    {!apiOk && (f.apiName || f.apiUrl) && <small className="dz-hint err">Use a lowercase name (letters, digits, underscores) and an http(s) URL.</small>}
                  </div>
                )}
                <div className="dz-two">
                  <label>Request<select value={f.method} onChange={(e) => set("method", e.target.value)}><option>GET</option><option>POST</option></select></label>
                  <label>Path<input value={f.path} onChange={(e) => set("path", e.target.value)} placeholder="/payers" /></label>
                </div>
                <label>Query parameters (one per line: name = value; use {"{field}"} for a field it depends on)<textarea rows={3} value={f.params} onChange={(e) => set("params", e.target.value)} placeholder={"region = {region}"} /></label>
                <label>The list is at (dotted path; empty if the response is the list)<input value={f.itemsPath} onChange={(e) => set("itemsPath", e.target.value)} placeholder="data.items" /></label>
                <div className="dz-two">
                  <label>Value field<input value={f.valueField} onChange={(e) => set("valueField", e.target.value)} placeholder="id" /></label>
                  <label>Label field<input value={f.labelField} onChange={(e) => set("labelField", e.target.value)} placeholder="name" /></label>
                </div>
              </>
            )}
            {kind !== "static" && <label>Cache results for<input value={f.ttl} onChange={(e) => set("ttl", e.target.value)} placeholder="5m" /></label>}
            <datalist id="dz-fields">{fieldNames.map((n) => <option key={n} value={n} />)}</datalist>
            {d && kind !== "static" && (newApi && kind === "http"
              ? <p className="dz-hint">Make the new API connection live first to test it.</p>
              : <TestPanel def={defFromNew(d)} database={kind === "sql" ? dbName : undefined} service={kind === "http" ? svcName : undefined} onOutcome={(ok) => setTested({ key: testKey, ok })} />)}
            {status === "failed" && <p className="dz-hint err" role="alert">The last test of this set failed. Fix it, or add it anyway and expect empty choices.</p>}
            {status === "untested" && kind !== "static" && d && !(newApi && kind === "http") && <p className="dz-hint">Not tested yet. Test it first to catch a wrong column or path.</p>}
            <button type="button" className={`dz-primary${status === "failed" ? " dz-warnbtn" : ""}`} disabled={!d || !connOk} onClick={submit}>{status === "failed" ? "Add anyway" : "Add set"}</button>
          </div>
        </section>
      )}
    </div>
  );
}

function tree(nodes: WorkspaceNode[]): { n: WorkspaceNode; depth: number }[] {
  const out: { n: WorkspaceNode; depth: number }[] = [];
  const walk = (parent: string, depth: number, seen: Set<string>) => {
    for (const n of nodes.filter((x) => x.parent === parent && !seen.has(x.id))) { out.push({ n, depth }); walk(n.id, depth + 1, new Set([...seen, n.id])); }
  };
  walk("", 0, new Set());
  return out;
}

function Workspace({ w, forms, canEdit, apply }: { w: WorkspaceModel; forms: DesignForm[]; canEdit: boolean; apply(o: Op[]): void }) {
  const sets = [...new Set(w.lookups.map((l) => l.set))];
  const usedBy = (set: string) => forms.flatMap((f) => f.inputs.filter((i) => i.lookup === set).map((i) => `${f.title || f.name} › ${i.label || i.name}`));
  const [set, setSet] = useState(sets[0] ?? "");
  const [code, setCode] = useState("");
  const [label, setLabel] = useState("");
  const [scope, setScope] = useState("");
  const add = () => {
    if (!set.trim() || !code.trim() || !label.trim()) return;
    apply([addLookupOp(w.file, w, { set: set.trim(), code: code.trim(), label: label.trim(), node: scope || undefined, sort: w.lookups.filter((l) => l.set === set.trim()).length + 1 })]);
    setCode(""); setLabel("");
  };
  return (
    <div className="dz-layoutgrid">
      <section className="dz-groups" aria-label={`Workspaces of ${w.name}`}>
        <h3>Workspaces · {w.name}</h3>
        <p className="dz-hint">Levels: {w.levels.join(" › ") || "—"}. Lookups are inherited down this tree; a workspace may add or switch off entries for itself.</p>
        <ul className="dz-tree">
          {tree(w.nodes).map(({ n, depth }) => (
            <li key={n.id} style={{ paddingLeft: depth * 18 }}><b>{n.name || n.id}</b> <em className="dz-kind">{n.level}</em> <code>{n.id}</code></li>
          ))}
          {w.nodes.length === 0 && <li className="dz-hint">No workspaces are declared here (they may come from the database).</li>}
        </ul>
      </section>
      <section className="dz-lpreview" aria-label="Lookups">
        <h3>Lookups</h3>
        {sets.map((s) => (
          <div key={s} className="dz-lookup">
            <h4>{s} <small className="dz-hint">{usedBy(s).length ? `used by ${usedBy(s).join("; ")}` : "not used by any field of this pipeline"}</small></h4>
            <table>
              <thead><tr><th>Code</th><th>Label</th><th>Applies to</th><th /></tr></thead>
              <tbody>
                {w.lookups.filter((l) => l.set === s).map((l, i) => (
                  <tr key={i} className={l.disabled ? "off" : ""}><td><code>{l.code}</code></td><td>{l.label}</td><td>{l.node ? w.nodes.find((n) => n.id === l.node)?.name || l.node : "everyone"}</td><td>{l.disabled ? "switched off" : ""}</td></tr>
                ))}
              </tbody>
            </table>
          </div>
        ))}
        {canEdit && (
          <form className="dz-addlookup" onSubmit={(e) => { e.preventDefault(); add(); }}>
            <h4>Add an entry</h4>
            <input list="dz-lookup-sets" aria-label="Lookup name" placeholder="lookup name" value={set} onChange={(e) => setSet(e.target.value)} />
            <datalist id="dz-lookup-sets">{sets.map((s) => <option key={s} value={s} />)}</datalist>
            <input aria-label="Code" placeholder="code" value={code} onChange={(e) => setCode(e.target.value)} />
            <input aria-label="Label" placeholder="label" value={label} onChange={(e) => setLabel(e.target.value)} />
            <select aria-label="Applies to" value={scope} onChange={(e) => setScope(e.target.value)}>
              <option value="">everyone</option>
              {w.nodes.map((n) => <option key={n.id} value={n.id}>{n.name || n.id}</option>)}
            </select>
            <button type="submit" className="dz-primary" disabled={!set.trim() || !code.trim() || !label.trim()}>Add</button>
          </form>
        )}
      </section>
    </div>
  );
}

function SetRow({ source, set, usedBy }: { source: LookupSourceModel; set: { name: string; type: string; detail: string; raw: string }; usedBy: string }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <tr>
        <td><code>{set.name}</code></td><td>{KIND_LABEL[set.type] ?? set.type}</td><td className="dz-detail" title={set.detail}>{set.detail}</td><td>{usedBy}</td>
        <td><button type="button" className="dz-link" aria-expanded={open} onClick={() => setOpen(!open)}>{open ? "Hide test" : "Test"}</button></td>
      </tr>
      {open && <tr><td colSpan={5}><TestPanel def={defFromRaw(set.raw)} database={source.database} service={source.service} /></td></tr>}
    </>
  );
}

/** Runs a set against the running app and shows its choices, or why it failed. */
function TestPanel({ def, database, service, onOutcome }: { def: SetDef; database?: string; service?: string; onOutcome?(ok: boolean): void }) {
  const token = useStudio((s) => s.token);
  const needs = setDeps(def);
  const [deps, setDeps2] = useState<Record<string, string>>({});
  const [state, setState] = useState<{ busy: boolean; result?: TestResult; error?: string }>({ busy: false });
  const run = async () => {
    setState({ busy: true });
    try { setState({ busy: false, result: await testSet(token, { database, service, set: def, deps }) }); onOutcome?.(true); }
    catch (e) { setState({ busy: false, error: e instanceof Error ? e.message : String(e) }); onOutcome?.(false); }
  };
  return (
    <div className="dz-test">
      {needs.length > 0 && (
        <div className="dz-testdeps">
          <span className="dz-hint">Try it with these field values:</span>
          {needs.map((n) => <label key={n}>{n}<input value={deps[n] ?? ""} onChange={(e) => setDeps2({ ...deps, [n]: e.target.value })} /></label>)}
        </div>
      )}
      <button type="button" disabled={state.busy} onClick={() => void run()}>{state.busy ? "Testing…" : "Test this set"}</button>
      {state.error && <p className="dz-hint err" role="alert">{state.error}</p>}
      {state.result && (
        <div aria-live="polite">
          <p className="dz-hint">{state.result.total} {state.result.total === 1 ? "choice" : "choices"} in {state.result.duration_ms} ms{state.result.total > state.result.options.length ? ` (first ${state.result.options.length} shown)` : ""}.{state.result.total === 0 && needs.length > 0 ? " An empty answer is normal until the field values above are filled in." : ""}</p>
          {state.result.options.length > 0 && (
            <table><thead><tr><th>Value</th><th>Label</th></tr></thead>
              <tbody>{state.result.options.map((o, i) => <tr key={i}><td><code>{o.value}</code></td><td>{o.label}</td></tr>)}</tbody></table>
          )}
        </div>
      )}
    </div>
  );
}
