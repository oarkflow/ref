import "./designer.css";
import { Control } from "./Control";
import { LayoutPanel } from "./LayoutPanel";
import { SlaPanel } from "./SlaPanel";
import { StagePanel } from "./StagePanel";
import { SnippetsPanel } from "./SnippetsPanel";
import { Text } from "./Fields";
import { RuleEditor } from "./RuleEditor";
import { WorkspacePanel } from "./WorkspacePanel";
import { useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useShallow } from "zustand/react/shallow";
import type { Op } from "../api/types";
import { quote } from "../lib/bcl";
import { childPath, findNode } from "../lib/paths";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";
import {
  CHOICE_KINDS, PALETTE, addInputOps, addSharedInputOps, usesOfShared, fieldRefs, contextRefs, detachSharedOps, lookupSets, moveInputOps, parseStages, pipelinesIn, shareInlineOps, setFormPropOp, parseCatalog, parseForms, parseLookupSources, parseWorkspaces, setPropOp, splitList, uniqueName,
  type DesignForm, type DesignInput, type FieldRef, type FormProp, type InputProp, type PaletteItem,
} from "./model";

const WIDTHS = [[0, "Auto"], [3, "Quarter"], [4, "Third"], [6, "Half"], [8, "Two thirds"], [12, "Full"]] as const;

/** A visual designer for the forms of one pipeline: palette, canvas, properties and live preview. */
export function DesignerPage() {
  const [params] = useSearchParams();
  const file = params.get("file") ?? "";
  const path = params.get("path") ?? "";
  const store = useStudioStore();
  const s = useStudio(useShallow((st) => ({ draft: st.draft, trees: st.trees, meta: st.meta })));
  const canEdit = hasRole(s.meta, "editor");
  const draftId = s.draft?.id;
  const [formName, setFormName] = useState<string>("");
  const [selected, setSelected] = useState<string>("");
  const [shareNew, setShareNew] = useState(false);
  const [mode, setMode] = useState<"design" | "layout" | "flow" | "sla" | "workspaces" | "snippets" | "preview">("design");
  const [drag, setDrag] = useState<{ kind: "palette"; item: PaletteItem } | { kind: "input"; index: number } | null>(null);
  const [over, setOver] = useState<number | null>(null);

  useEffect(() => {
    if (draftId && file && path) void store.getState().select(file, path);
  }, [store, draftId, file, path]);
  // Workspaces and lookups live in other files than the pipeline.
  useEffect(() => {
    if (draftId) void store.getState().ensureTrees();
  }, [store, draftId]);

  const pipeline = useMemo(() => (file ? findNode(s.trees[file], path) : undefined), [s.trees, file, path]);
  const forms = useMemo(() => parseForms(pipeline), [pipeline]);
  const catalog = useMemo(() => parseCatalog(pipeline), [pipeline]);
  const workspaces = useMemo(() => parseWorkspaces(s.trees), [s.trees]);
  const stages = useMemo(() => parseStages(pipeline), [pipeline]);
  const pipelines = useMemo(() => pipelinesIn(s.trees), [s.trees]);
  const refs = useMemo(() => [...fieldRefs(forms), ...contextRefs(workspaces, stages)], [forms, workspaces, stages]);
  const sources = useMemo(() => parseLookupSources(s.trees), [s.trees]);
  const lookups = useMemo(() => [...new Set([...sources.flatMap((x) => x.sets.map((y) => y.name)), ...lookupSets(workspaces)])], [sources, workspaces]);
  const form: DesignForm | undefined = forms.find((f) => f.name === formName) ?? forms[0];
  const input: DesignInput | undefined = selected.startsWith("shared:")
    ? form?.shared.find((i) => i.name === selected.slice(7))
    : form?.inputs.find((i) => i.name === selected);

  if (!s.draft) return <main className="page"><p className="empty">Open a working copy first. <a href="#/overview">Back</a></p></main>;
  if (!file || !path) return <main className="page"><p className="empty">Open a pipeline to design its forms. <a href="#/overview">Back</a></p></main>;
  if (!pipeline) return <main className="page"><p className="empty" aria-busy="true">Loading {path}…</p></main>;

  const apply = (ops: Op[]) => { if (ops.length && canEdit) store.getState().edit(ops); };
  const addInput = (item: PaletteItem, at?: number) => {
    if (!form) return;
    const ops = shareNew ? addSharedInputOps(file, pipeline.path, form, catalog, item) : addInputOps(file, form, item, at);
    apply(ops);
    setSelected((shareNew ? "shared:" : "") + (ops[0] as { id: string }).id);
  };
  const onDropAt = (index: number) => {
    if (!form || !drag) return;
    if (drag.kind === "palette") addInput(drag.item, index);
    else apply(moveInputOps(file, form, drag.index, Math.min(index > drag.index ? index - 1 : index, form.inputs.length - 1)));
    setDrag(null); setOver(null);
  };
  const addForm = () => {
    const name = uniqueName("form", forms.map((f) => f.name));
    apply([{ op: "addBlock", file, parent: pipeline.path, type: "form", id: name, body: `title ${quote("New form")}` }]);
    setFormName(name); setSelected("");
  };
  const removeInput = (i: DesignInput) => { apply([{ op: "removeBlock", file, path: i.path }]); setSelected(""); };
  const rename = (i: DesignInput, next: string) => {
    const id = uniqueName(next, form?.inputs.filter((x) => x !== i).map((x) => x.name) ?? []);
    if (id === i.name) return;
    apply([{ op: "renameBlock", file, path: i.path, newId: id }]);
    setSelected(id);
  };
  const setProp = (i: DesignInput, prop: InputProp, value: string | boolean | number | string[]) => apply([setPropOp(file, i, prop, value)]);

  return (
    <div className="dz" data-mode={mode}>
      <header className="dz-head">
        <div>
          <a className="dz-back" href="#/edit">← Pipeline settings</a>
          <h1>{pipeline.id} <small>form designer</small></h1>
        </div>
        <div className="dz-modes" role="tablist" aria-label="Designer mode">
          {(["design", "layout", "flow", "sla", "workspaces", "snippets", "preview"] as const).map((m) => (
            <button key={m} type="button" role="tab" aria-selected={mode === m} className={mode === m ? "on" : ""} onClick={() => setMode(m)}>{({ design: "Design", layout: "Page layout", flow: "Stages & flow", sla: "SLA", workspaces: "Workspaces & lookups", snippets: "Snippets", preview: "Preview" } as const)[m]}</button>
          ))}
        </div>
      </header>
      {!canEdit && <p className="dz-note">You can look but not change anything (viewer role).</p>}

      {mode === "layout" && <LayoutPanel file={file} pipeline={pipeline} forms={forms} canEdit={canEdit} apply={apply} />}
      {mode === "flow" && <StagePanel file={file} pipeline={pipeline} refs={refs} canEdit={canEdit} apply={apply} />}
      {mode === "snippets" && <SnippetsPanel file={file} pipelinePath={pipeline.path} pipelineName={pipeline.id ?? ""} forms={forms} catalog={catalog} stages={stages} pipelines={pipelines} canEdit={canEdit} apply={apply} onOpenForm={(n) => { setFormName(n); setSelected(""); setMode("design"); }} />}
      {mode === "sla" && <SlaPanel file={file} pipeline={pipeline} canEdit={canEdit} apply={apply} />}
      {mode === "workspaces" && <WorkspacePanel workspaces={workspaces} sources={sources} trees={s.trees} pipelineName={pipeline.id ?? ""} pipelineFile={file} forms={forms} canEdit={canEdit} apply={apply} />}

      {(mode === "design" || mode === "preview") && <div className="dz-tabs" role="tablist" aria-label="Forms">
        {forms.map((f) => (
          <button key={f.name} type="button" role="tab" aria-selected={f === form} className={f === form ? "on" : ""} onClick={() => { setFormName(f.name); setSelected(""); }}>
            {f.title || f.name} <span className="dz-count">{f.inputs.length}</span>
          </button>
        ))}
        {canEdit && <button type="button" className="dz-add" onClick={addForm}>+ Form</button>}
      </div>}

      {mode === "layout" || mode === "flow" || mode === "snippets" || mode === "sla" || mode === "workspaces" ? null : !form ? <p className="empty">This pipeline has no forms yet. Add one to start.</p> : (
        <div className="dz-body">
          {mode === "design" && (
            <aside className="dz-palette" aria-label="Field types">
              <h2>Fields</h2>
              <p className="dz-hint">Click to add, or drag onto the form.</p>
              <label className="dz-inline dz-sharetoggle" title="A shared field is defined once in the pipeline's catalog and can be used by several forms">
                <input type="checkbox" checked={shareNew} disabled={!canEdit} onChange={(e) => setShareNew(e.target.checked)} /> Add as shared field
              </label>
              {PALETTE.map((p) => (
                <button key={p.kind} type="button" className="dz-pal" disabled={!canEdit} draggable={canEdit}
                  onDragStart={() => setDrag({ kind: "palette", item: p })} onDragEnd={() => { setDrag(null); setOver(null); }}
                  onClick={() => addInput(p)}>
                  <b>{p.label}</b><span>{p.hint}</span>
                </button>
              ))}
            </aside>
          )}

          <section className="dz-canvas" aria-label={`Form ${form.name}`}>
            <FormHeader key={form.path} form={form} file={file} canEdit={canEdit && mode === "design"} apply={apply} onDeleted={() => setFormName("")} />
            {form.repeatable && <p className="dz-repnote">Repeatable form: people add {form.minItems || 1}{form.maxItems ? `–${form.maxItems}` : " or more"} entries{mode === "preview" ? ". Entry 1:" : ". Use Preview to see several."}</p>}
            <div className="dz-grid" onDragOver={(e) => { if (drag) e.preventDefault(); }} onDrop={() => onDropAt(form.inputs.length)}>
              {form.inputs.map((i, idx) => (
                <div key={i.name} className={`dz-cell${i.name === selected && mode === "design" ? " sel" : ""}${over === idx ? " over" : ""}`}
                  style={{ gridColumn: `span ${mode === "design" ? Math.max(i.span || 6, 4) : i.span || 6}` }}
                  draggable={canEdit && mode === "design"} tabIndex={mode === "design" ? 0 : -1}
                  onDragStart={() => setDrag({ kind: "input", index: idx })} onDragEnd={() => { setDrag(null); setOver(null); }}
                  onDragOver={(e) => { if (drag) { e.preventDefault(); e.stopPropagation(); setOver(idx); } }}
                  onDrop={(e) => { e.stopPropagation(); onDropAt(idx); }}
                  onClick={() => mode === "design" && setSelected(i.name)}
                  onKeyDown={(e) => { if (mode === "design" && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); setSelected(i.name); } }}>
                  <Control input={i} design={mode === "design"} />
                </div>
              ))}
              {form.shared.map((i) => (
                <div key={"shared:" + i.name} className={`dz-cell shared${selected === "shared:" + i.name && mode === "design" ? " sel" : ""}`} style={{ gridColumn: `span ${mode === "design" ? Math.max(i.span || 6, 4) : i.span || 6}` }}
                  tabIndex={mode === "design" ? 0 : -1} onClick={() => mode === "design" && setSelected("shared:" + i.name)}
                  onKeyDown={(e) => { if (mode === "design" && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); setSelected("shared:" + i.name); } }}>
                  <Control input={i} design={mode === "design"} />
                  {mode === "design" && canEdit && <button type="button" className="dz-link danger" onClick={() => apply([{ op: "setField", file, path: childPath(form.path, "fields"), value: "[" + form.fields.filter((x) => x !== i.name).map(quote).join(", ") + "]" }])}>Remove shared field</button>}
                </div>
              ))}
              {form.inputs.length === 0 && form.shared.length === 0 && <p className="dz-empty">{mode === "design" ? "Empty form. Add a field from the left." : "Nothing to preview."}</p>}
            </div>
            {mode === "preview" && form.repeatable && <RepeatPreview key={form.path} form={form} />}
            {mode === "design" && canEdit && catalog.filter((c) => !form.fields.includes(c.name)).length > 0 && (
              <label className="dz-sharedadd">Add a shared field
                <select value="" onChange={(e) => e.target.value && apply([{ op: "setField", file, path: childPath(form.path, "fields"), value: "[" + [...form.fields, e.target.value].map(quote).join(", ") + "]" }])}>
                  <option value="">choose from the catalog…</option>
                  {catalog.filter((c) => !form.fields.includes(c.name)).map((c) => <option key={c.name} value={c.name}>{c.label || c.name}</option>)}
                </select>
              </label>
            )}
          </section>

          {mode === "design" && (
            <aside className="dz-props" aria-label="Field properties">
              {input ? (
                <Props key={input.path} onDetach={() => { apply(detachSharedOps(file, form, input)); setSelected(input.name); }} onShare={() => { apply(shareInlineOps(file, pipeline.path, form, catalog, input)); setSelected("shared:" + input.name); }} refs={refs} sharedIn={input.shared ? usesOfShared(forms, input.name) : null} lookups={lookups} siblings={form.inputs.map((x) => x.name)} input={input} index={form.inputs.indexOf(input)} count={form.inputs.length} canEdit={canEdit}
                  setProp={(p, v) => setProp(input, p, v)} onRename={(n) => rename(input, n)} onDelete={() => removeInput(input)}
                  onMove={(d) => apply(moveInputOps(file, form, form.inputs.indexOf(input), form.inputs.indexOf(input) + d))} />
              ) : (
                <>
                  <FormProps key={form.path} form={form} refs={refs} canEdit={canEdit} onProp={(p, v) => apply([setFormPropOp(file, form, p, v)])} />
                  <p className="dz-hint">Select a field to change its label, type, width and rules.</p>
                </>
              )}
            </aside>
          )}
        </div>
      )}
    </div>
  );
}

function FormHeader({ form, file, canEdit, apply, onDeleted }: { form: DesignForm; file: string; canEdit: boolean; apply(o: Op[]): void; onDeleted(): void }) {
  const [title, setTitle] = useState(form.title);
  return (
    <div className="dz-formhead">
      {canEdit ? (
        <input aria-label="Form title" value={title} placeholder={form.name} onChange={(e) => setTitle(e.target.value)}
          onBlur={() => { if (title !== form.title) apply([title ? { op: "setField", file, path: childPath(form.path, "title"), value: quote(title) } : { op: "removeField", file, path: childPath(form.path, "title") }]); }} />
      ) : <h2>{form.title || form.name}</h2>}
      {canEdit && <button type="button" className="dz-link danger" onClick={() => { apply([{ op: "removeBlock", file, path: form.path }]); onDeleted(); }}>Delete form</button>}
    </div>
  );
}

function Props({ input: i, index, count, canEdit, setProp, onRename, onDelete, onMove, lookups, siblings, sharedIn, refs, onDetach, onShare }: {
  onDetach(): void; onShare(): void; refs: FieldRef[]; sharedIn: string[] | null; lookups: string[]; siblings: string[]; input: DesignInput; index: number; count: number; canEdit: boolean;
  setProp(p: InputProp, v: string | boolean | number | string[]): void; onRename(n: string): void; onDelete(): void; onMove(d: number): void;
}) {
  const [label, setLabel] = useState(i.label);
  const [name, setName] = useState(i.name);
  const [help, setHelp] = useState(i.help);
  const [ph, setPh] = useState(i.placeholder);
  const [opts, setOpts] = useState(i.options.join("\n"));
  const [pattern, setPattern] = useState(i.pattern);
  const [minL, setMinL] = useState(i.minLength);
  const [maxL, setMaxL] = useState(i.maxLength);
  const [accept, setAccept] = useState(i.accept.join(", "));
  const commitText = (prop: InputProp, now: string, was: string) => { if (now !== was) setProp(prop, now); };
  const choice = CHOICE_KINDS.includes(i.kind);
  const dis = !canEdit;
  return (
    <div className="dz-propform">
      <h2>{i.label || i.name}</h2>
      {sharedIn && <p className="dz-sharednote">Shared field. Changes here apply to every form that uses it: {sharedIn.join(", ") || "none yet"}.</p>}
      <label>Label<input disabled={dis} value={label} onChange={(e) => setLabel(e.target.value)} onBlur={() => commitText("label", label, i.label)} /></label>
      <label>Field name<input disabled={dis || !!sharedIn} value={name} onChange={(e) => setName(e.target.value)} onBlur={() => onRename(name)} /><small>Used in data, rules and exports.</small></label>
      <label>Type
        <select disabled={dis} value={i.kind} onChange={(e) => setProp("kind", e.target.value)}>
          {PALETTE.map((p) => <option key={p.kind} value={p.kind}>{p.label}</option>)}
        </select>
      </label>
      <label className="dz-inline"><input type="checkbox" disabled={dis} checked={i.required} onChange={(e) => setProp("required", e.target.checked)} /> Required</label>
      <label>Width
        <select disabled={dis} value={i.span} onChange={(e) => setProp("span", Number(e.target.value))}>
          {WIDTHS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
        </select>
      </label>
      <label>Help text<input disabled={dis} value={help} onChange={(e) => setHelp(e.target.value)} onBlur={() => commitText("help", help, i.help)} /></label>
      {!choice && i.kind !== "boolean" && i.kind !== "file" && <label>Placeholder<input disabled={dis} value={ph} onChange={(e) => setPh(e.target.value)} onBlur={() => commitText("placeholder", ph, i.placeholder)} /></label>}
      {choice && (
        <label>Options (one per line)
          <textarea rows={5} disabled={dis} value={opts} onChange={(e) => setOpts(e.target.value)}
            onBlur={() => { const list = opts.split("\n").map((x) => x.trim()).filter(Boolean); if (list.join("\n") !== i.options.join("\n")) setProp("options", list); }} />
        </label>
      )}
      {(i.kind === "text" || i.kind === "textarea") && (
        <>
          <label>Pattern (regular expression)<input disabled={dis} value={pattern} onChange={(e) => setPattern(e.target.value)} onBlur={() => commitText("pattern", pattern, i.pattern)} /></label>
          <div className="dz-two">
            <label>Min length<input disabled={dis} inputMode="numeric" value={minL} onChange={(e) => setMinL(e.target.value)} onBlur={() => commitText("min_length", minL.trim() === "0" ? "" : minL.trim(), i.minLength)} /></label>
            <label>Max length<input disabled={dis} inputMode="numeric" value={maxL} onChange={(e) => setMaxL(e.target.value)} onBlur={() => commitText("max_length", maxL.trim() === "0" ? "" : maxL.trim(), i.maxLength)} /></label>
          </div>
        </>
      )}
      {i.kind === "file" && (
        <label>Accepted types (comma separated)<input disabled={dis} value={accept} placeholder="image/*, application/pdf" onChange={(e) => setAccept(e.target.value)}
          onBlur={() => { const list = accept.split(",").map((x) => x.trim()).filter(Boolean); if (list.join() !== i.accept.join()) setProp("accept", list); }} /></label>
      )}
      <details className="dz-adv" open={!!(i.lookup || i.dependsOn.length || i.visibleIf || i.requiredIf || i.disabledIf || i.viewRoles.length || i.editRoles.length)}>
        <summary>Data &amp; rules</summary>
        {(choice || i.lookup) && (
          <label>Choices from a workspace lookup
            <select disabled={dis} value={i.lookup} onChange={(e) => setProp("lookup", e.target.value)}>
              <option value="">none (use the options above)</option>
              {[...new Set([...lookups, ...(i.lookup ? [i.lookup] : [])])].map((l) => <option key={l} value={l}>{l}</option>)}
            </select>
          </label>
        )}
        <Text label="Depends on (fields, comma separated)" value={i.dependsOn.join(", ")} disabled={dis} hint={`Fields of this form: ${siblings.filter((n) => n !== i.name).join(", ") || "none"}`} onCommit={(v) => setProp("depends_on", splitList(v))} />
        <RuleEditor label="Show this field only when" hint="Always shown." value={i.visibleIf} fields={refs} disabled={dis} onCommit={(v) => setProp("visible_if", v)} />
        <RuleEditor label="Make it required when" hint="Required only if it is marked required above." value={i.requiredIf} fields={refs} disabled={dis} onCommit={(v) => setProp("required_if", v)} />
        <RuleEditor label="Make it read-only when" hint="Never read-only." value={i.disabledIf} fields={refs} disabled={dis} onCommit={(v) => setProp("disabled_if", v)} />
        <Text label="Only these roles may see it" value={i.viewRoles.join(", ")} disabled={dis} onCommit={(v) => setProp("view_roles", splitList(v))} />
        <Text label="Only these roles may edit it" value={i.editRoles.join(", ")} disabled={dis} onCommit={(v) => setProp("edit_roles", splitList(v))} />
        <Text label="Default value" value={i.def} disabled={dis} onCommit={(v) => setProp("default", v)} />
        {(i.kind === "number" || i.kind === "integer" || i.kind === "date" || i.kind === "datetime") && (
          <div className="dz-two">
            <Text label="Minimum" value={i.min} disabled={dis} onCommit={(v) => setProp("min", v)} />
            <Text label="Maximum" value={i.max} disabled={dis} onCommit={(v) => setProp("max", v)} />
          </div>
        )}
        <Text label="Computed from (expression)" value={i.compute} disabled={dis} onCommit={(v) => setProp("compute", v)} />
        <div className="dz-checks">
          <label className="dz-inline"><input type="checkbox" disabled={dis} checked={i.pii} onChange={(e) => setProp("pii", e.target.checked)} /> Personal data (PII)</label>
          <label className="dz-inline"><input type="checkbox" disabled={dis} checked={i.sensitive} onChange={(e) => setProp("sensitive", e.target.checked)} /> Sensitive</label>
          <label className="dz-inline"><input type="checkbox" disabled={dis} checked={i.sealed} onChange={(e) => setProp("sealed", e.target.checked)} /> Sealed until opened</label>
        </div>
      </details>
      {canEdit && sharedIn && <div className="dz-actions"><button type="button" onClick={onDetach} title="Make this form's own copy; the shared field and other forms stay as they are">Detach into this form</button></div>}
      {canEdit && !sharedIn && (
        <div className="dz-actions">
          <button type="button" onClick={onShare} title="Define it once in the catalog so other forms can use it too">Make shared</button>
          <button type="button" disabled={index === 0} onClick={() => onMove(-1)}>Move up</button>
          <button type="button" disabled={index === count - 1} onClick={() => onMove(1)}>Move down</button>
          <button type="button" className="danger" onClick={onDelete}>Delete field</button>
        </div>
      )}
    </div>
  );
}


/** Settings of the form itself: repeatable entries, columns, when it is shown or read-only, and who may see or edit it. */
function FormProps({ form, refs, canEdit, onProp }: { form: DesignForm; refs: FieldRef[]; canEdit: boolean; onProp(p: FormProp, v: string | number | boolean | string[]): void }) {
  const dis = !canEdit;
  return (
    <div className="dz-propform">
      <h2>Form settings</h2>
      <Text label="Description" value={form.description} disabled={dis} onCommit={(v) => onProp("description", v)} />
      <label className="dz-inline"><input type="checkbox" disabled={dis} checked={form.repeatable} onChange={(e) => onProp("repeatable", e.target.checked)} /> Repeatable: the person adds several entries</label>
      {form.repeatable && (
        <div className="dz-two">
          <Text label="At least" value={form.minItems ? String(form.minItems) : ""} disabled={dis} onCommit={(v) => onProp("min_items", Number(v) || 0)} />
          <Text label="At most" value={form.maxItems ? String(form.maxItems) : ""} disabled={dis} onCommit={(v) => onProp("max_items", Number(v) || 0)} />
        </div>
      )}
      <Text label="Columns" value={form.columns ? String(form.columns) : ""} disabled={dis} hint="Fields per row (default 2)" onCommit={(v) => onProp("columns", Number(v) || 0)} />
      <RuleEditor label="Show this form only when" hint="Always shown." value={form.visibleIf} fields={refs} disabled={dis} onCommit={(v) => onProp("visible_if", v)} />
      <RuleEditor label="Make this form read-only when" hint="Never read-only." value={form.disabledIf} fields={refs} disabled={dis} onCommit={(v) => onProp("disabled_if", v)} />
      <Text label="Only these roles may see it" value={form.viewRoles.join(", ")} disabled={dis} onCommit={(v) => onProp("view_roles", splitList(v))} />
      <Text label="Only these roles may edit it" value={form.editRoles.join(", ")} disabled={dis} onCommit={(v) => onProp("edit_roles", splitList(v))} />
    </div>
  );
}

/** More entries of a repeatable form, within its limits, as the person filling it in would add them. */
function RepeatPreview({ form }: { form: DesignForm }) {
  const [count, setCount] = useState(Math.max(form.minItems, 1));
  const all = [...form.inputs, ...form.shared];
  return (
    <div className="dz-repeat">
      {Array.from({ length: count - 1 }, (_, k) => (
        <div key={k} className="dz-entry">
          <p className="dz-repnote">Entry {k + 2}:{count > Math.max(form.minItems, 1) && k === count - 2 && <button type="button" className="dz-link danger" onClick={() => setCount(count - 1)}>Remove</button>}</p>
          <div className="dz-grid">{all.map((i) => <div key={i.name} className="dz-cell" style={{ gridColumn: `span ${i.span || 6}` }}><Control input={i} design={false} /></div>)}</div>
        </div>
      ))}
      <button type="button" className="dz-addgroup" disabled={form.maxItems > 0 && count >= form.maxItems} onClick={() => setCount(count + 1)}>+ Add another entry{form.maxItems > 0 ? ` (${count} of ${form.maxItems})` : ""}</button>
    </div>
  );
}
