import { useEffect, useState } from "react";
import type { BlockNode, Op } from "../api/types";
import { Control } from "./Control";
import { RuleEditor } from "./RuleEditor";
import { Text } from "./Fields";
import {
  GROUP_MODES, LAYOUTS, addGroupOp, fieldRefs, splitList, addPageOps, moveFormOps, moveGroupOps, parseStages, setGroupFormsOp, setGroupPropOp, setPageOp, unplacedForms,
  type DesignForm, type DesignGroup, type DesignPage, type FieldRef,
} from "./model";

interface Props { file: string; pipeline: BlockNode; forms: DesignForm[]; canEdit: boolean; apply(ops: Op[]): void }
type Drag = { form: string; group: number } | null; // group -1 = the tray of unplaced forms

/** Edits how a stage's forms are laid out on its page: layout, groups, and which forms sit in which group. */
export function LayoutPanel({ file, pipeline, forms, canEdit, apply }: Props) {
  const stages = parseStages(pipeline);
  const [stageName, setStageName] = useState("");
  const [drag, setDrag] = useState<Drag>(null);
  const [over, setOver] = useState<string>("");
  const stage = stages.find((s) => s.name === stageName) ?? stages[0];
  const page = stage?.page;

  if (!stage) return <p className="empty">This pipeline has no stages.</p>;
  const unplaced = unplacedForms(forms, page);
  const refs = fieldRefs(forms);
  const drop = (toGroup: number, at: number) => {
    if (!page || !drag) return;
    if (drag.group === -1) {
      const g = page.groups[toGroup];
      if (g) { const list = g.forms.filter((f) => f !== drag.form); list.splice(Math.min(at, list.length), 0, drag.form); apply([setGroupFormsOp(file, g, list)]); }
    } else if (toGroup === -1) {
      const g = page.groups[drag.group];
      if (g) apply([setGroupFormsOp(file, g, g.forms.filter((f) => f !== drag.form))]);
    } else apply(moveFormOps(file, page.groups, drag.form, drag.group, toGroup, at));
    setDrag(null); setOver("");
  };

  return (
    <div className="dz-layout">
      <div className="dz-stagebar">
        <label>Stage
          <select value={stage.name} onChange={(e) => setStageName(e.target.value)}>
            {stages.map((s) => <option key={s.name} value={s.name}>{s.title || s.name}{s.page ? "" : " (no page)"}</option>)}
          </select>
        </label>
        <span className="dz-hint">{stage.kind ? `Kind: ${stage.kind}. ` : ""}Choose which forms people see at this stage and how they are arranged.</span>
      </div>

      {!page ? (
        <div className="dz-nopage">
          <p>This stage has no page yet, so it shows its forms as plain defaults.</p>
          {canEdit && <button type="button" className="dz-primary" onClick={() => apply(addPageOps(file, stage, forms.map((f) => f.name), stage.title))}>Create a page with all forms</button>}
        </div>
      ) : (
        <div className="dz-layoutgrid">
          <section className="dz-groups" aria-label="Groups">
            <PageProps key={page.path} file={file} page={page} canEdit={canEdit} apply={apply} />
            {page.groups.map((g, gi) => (
              <GroupCard key={g.name} file={file} group={g} index={gi} count={page.groups.length} page={page} canEdit={canEdit} apply={apply}
                allForms={forms} refs={refs} drag={drag} setDrag={setDrag} over={over} setOver={setOver} drop={drop} />
            ))}
            {canEdit && <button type="button" className="dz-addgroup" onClick={() => apply([addGroupOp(file, page, unplaced.slice(0, 1))])}>+ Add group</button>}
            <div className={`dz-tray${over === "tray" ? " over" : ""}`} onDragOver={(e) => { if (drag) { e.preventDefault(); setOver("tray"); } }} onDrop={() => drop(-1, 0)}>
              <h3>Forms not on this page</h3>
              {unplaced.length === 0 ? <p className="dz-hint">Every form of the pipeline is placed. Drag a form here to remove it from its group.</p> : (
                <div className="dz-chips">
                  {unplaced.map((f) => <span key={f} className="dz-chip" draggable={canEdit} onDragStart={() => setDrag({ form: f, group: -1 })} onDragEnd={() => { setDrag(null); setOver(""); }}>{f}</span>)}
                </div>
              )}
            </div>
          </section>
          <section className="dz-lpreview" aria-label="Page preview">
            <h3>Preview</h3>
            <PagePreview page={page} forms={forms} />
          </section>
        </div>
      )}
    </div>
  );
}

function PageProps({ file, page, canEdit, apply }: { file: string; page: DesignPage; canEdit: boolean; apply(o: Op[]): void }) {
  const [title, setTitle] = useState(page.title);
  const [label, setLabel] = useState(page.submitLabel);
  useEffect(() => { setTitle(page.title); setLabel(page.submitLabel); }, [page.title, page.submitLabel]);
  const layout = LAYOUTS.find((l) => l.value === page.layout);
  return (
    <div className="dz-pageprops">
      <label>Page title<input disabled={!canEdit} value={title} onChange={(e) => setTitle(e.target.value)} onBlur={() => title !== page.title && apply([setPageOp(file, page, "title", title)])} /></label>
      <label>Layout
        <select disabled={!canEdit} value={page.layout} onChange={(e) => apply([setPageOp(file, page, "layout", e.target.value)])}>
          {LAYOUTS.map((l) => <option key={l.value} value={l.value}>{l.label}</option>)}
        </select>
        <small>{layout?.hint}</small>
      </label>
      <label>Submit button label<input disabled={!canEdit} value={label} placeholder="Submit" onChange={(e) => setLabel(e.target.value)} onBlur={() => label !== page.submitLabel && apply([setPageOp(file, page, "submit_label", label)])} /></label>
    </div>
  );
}

function GroupCard({ file, group: g, index, count, page, canEdit, apply, allForms, refs, drag, setDrag, over, setOver, drop }: {
  refs: FieldRef[]; file: string; group: DesignGroup; index: number; count: number; page: DesignPage; canEdit: boolean; apply(o: Op[]): void; allForms: DesignForm[];
  drag: Drag; setDrag(d: Drag): void; over: string; setOver(s: string): void; drop(group: number, at: number): void;
}) {
  const [title, setTitle] = useState(g.title);
  const [cols, setCols] = useState(String(g.columns || ""));
  useEffect(() => { setTitle(g.title); setCols(String(g.columns || "")); }, [g.title, g.columns]);
  const addable = allForms.map((f) => f.name).filter((n) => !g.forms.includes(n));
  return (
    <div className={`dz-group${over === g.name ? " over" : ""}`} onDragOver={(e) => { if (drag) { e.preventDefault(); setOver(g.name); } }} onDrop={() => drop(index, g.forms.length)}>
      <div className="dz-grouphead">
        <input aria-label="Group title" disabled={!canEdit} value={title} placeholder={g.name} onChange={(e) => setTitle(e.target.value)} onBlur={() => title !== g.title && apply([setGroupPropOp(file, g, "title", title)])} />
        <select aria-label="Group mode" disabled={!canEdit} value={g.mode} onChange={(e) => apply([setGroupPropOp(file, g, "mode", e.target.value)])}>
          {GROUP_MODES.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}
        </select>
        <input aria-label="Columns" className="dz-cols" disabled={!canEdit} inputMode="numeric" placeholder="cols" value={cols} onChange={(e) => setCols(e.target.value)}
          onBlur={() => Number(cols || 0) !== g.columns && apply([setGroupPropOp(file, g, "columns", Number(cols) || 0)])} />
        <label className="dz-inline"><input type="checkbox" disabled={!canEdit} checked={g.collapsed} onChange={(e) => apply([setGroupPropOp(file, g, "collapsed", e.target.checked)])} /> Collapsed</label>
        {canEdit && (
          <span className="dz-gactions">
            <button type="button" aria-label="Move group up" disabled={index === 0} onClick={() => apply(moveGroupOps(file, page, index, index - 1))}>↑</button>
            <button type="button" aria-label="Move group down" disabled={index === count - 1} onClick={() => apply(moveGroupOps(file, page, index, index + 1))}>↓</button>
            <button type="button" className="danger" aria-label="Delete group" onClick={() => apply([{ op: "removeBlock", file, path: g.path }])}>Delete</button>
          </span>
        )}
      </div>
      <div className="dz-chips">
        {g.forms.map((f, fi) => (
          <span key={f} className="dz-chip" draggable={canEdit}
            onDragStart={() => setDrag({ form: f, group: index })} onDragEnd={() => { setDrag(null); setOver(""); }}
            onDragOver={(e) => { if (drag) { e.preventDefault(); e.stopPropagation(); setOver(g.name); } }}
            onDrop={(e) => { e.stopPropagation(); drop(index, fi); }}>
            {allForms.find((x) => x.name === f)?.title || f}
            {canEdit && <button type="button" aria-label={`Remove ${f} from group`} onClick={() => apply([setGroupFormsOp(file, g, g.forms.filter((x) => x !== f))])}>×</button>}
          </span>
        ))}
        {g.forms.length === 0 && <span className="dz-hint">Empty. Drag a form here.</span>}
        {canEdit && addable.length > 0 && (
          <select aria-label="Add a form to this group" className="dz-addform" value="" onChange={(e) => e.target.value && apply([setGroupFormsOp(file, g, [...g.forms, e.target.value])])}>
            <option value="">+ add form…</option>
            {addable.map((n) => <option key={n} value={n}>{n}</option>)}
          </select>
        )}
      </div>
      <details className="dz-adv" open={!!(g.visibleIf || g.disabledIf || g.roles.length || g.editRoles.length)}>
        <summary>Conditions &amp; access</summary>
        <Text label="Description" value={g.description} disabled={!canEdit} onCommit={(v) => apply([setGroupPropOp(file, g, "description", v)])} />
        <RuleEditor label="Show this group only when" hint="Always shown." value={g.visibleIf} fields={refs} disabled={!canEdit} onCommit={(v) => apply([setGroupPropOp(file, g, "visible_if", v)])} />
        <RuleEditor label="Make this group read-only when" hint="Never read-only." value={g.disabledIf} fields={refs} disabled={!canEdit} onCommit={(v) => apply([setGroupPropOp(file, g, "disabled_if", v)])} />
        <Text label="Only these roles see it" value={g.roles.join(", ")} disabled={!canEdit} onCommit={(v) => apply([setGroupPropOp(file, g, "roles", splitList(v))])} />
        <Text label="Only these roles may edit it" value={g.editRoles.join(", ")} disabled={!canEdit} onCommit={(v) => apply([setGroupPropOp(file, g, "edit_roles", splitList(v))])} />
      </details>
    </div>
  );
}

/** The page as the user will see it, in the chosen layout. */
function PagePreview({ page, forms }: { page: DesignPage; forms: DesignForm[] }) {
  const groups = page.groups.filter((g) => g.mode !== "hidden");
  const [step, setStep] = useState(0);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  useEffect(() => { setStep(0); }, [page.layout, groups.length]);
  const body = (g: DesignGroup) => (
    <div className="dz-pvgroup" style={g.columns ? { gridTemplateColumns: `repeat(${g.columns}, minmax(0, 1fr))` } : undefined}>
      {g.forms.flatMap((fn) => forms.find((f) => f.name === fn)?.inputs.map((i) => <div key={fn + i.name} className="dz-pvfield"><Control input={i} design={g.mode !== "editable"} /></div>) ?? [])}
      {g.forms.length === 0 && <p className="dz-hint">No forms in this group.</p>}
    </div>
  );
  const head = (g: DesignGroup) => <>{g.title || g.name}{g.mode !== "editable" && <em className="dz-kind">{g.mode}</em>}</>;
  if (groups.length === 0) return <p className="dz-hint">Nothing to show yet.</p>;
  const idx = Math.min(step, groups.length - 1);
  return (
    <div className="dz-pv">
      {page.title && <h4>{page.title}</h4>}
      {(page.layout === "tabbed") && (
        <>
          <div className="dz-pvtabs" role="tablist">{groups.map((g, i) => <button key={g.name} type="button" role="tab" aria-selected={i === idx} className={i === idx ? "on" : ""} onClick={() => setStep(i)}>{head(g)}</button>)}</div>
          {body(groups[idx]!)}
        </>
      )}
      {page.layout === "wizard" && (
        <>
          <ol className="dz-pvsteps">{groups.map((g, i) => <li key={g.name} className={i === idx ? "on" : i < idx ? "done" : ""}>{g.title || g.name}</li>)}</ol>
          {body(groups[idx]!)}
          <div className="dz-pvnav"><button type="button" disabled={idx === 0} onClick={() => setStep(idx - 1)}>Back</button>
            {idx < groups.length - 1 ? <button type="button" className="dz-primary" onClick={() => setStep(idx + 1)}>Next</button> : <button type="button" className="dz-primary" disabled>{page.submitLabel || "Submit"}</button>}</div>
        </>
      )}
      {page.layout === "accordion" && groups.map((g) => {
        const isOpen = open[g.name] ?? !g.collapsed;
        return <div key={g.name} className="dz-pvacc"><button type="button" aria-expanded={isOpen} onClick={() => setOpen({ ...open, [g.name]: !isOpen })}>{isOpen ? "▾" : "▸"} {head(g)}</button>{isOpen && body(g)}</div>;
      })}
      {page.layout === "grid" && <div className="dz-pvgrid">{groups.map((g) => <section key={g.name}><h5>{head(g)}</h5>{body(g)}</section>)}</div>}
      {(page.layout === "stacked" || !LAYOUTS.some((l) => l.value === page.layout)) && groups.map((g) => <section key={g.name} className="dz-pvstack"><h5>{head(g)}</h5>{body(g)}</section>)}
      {page.layout !== "wizard" && <div className="dz-pvnav"><button type="button" className="dz-primary" disabled>{page.submitLabel || "Submit"}</button></div>}
    </div>
  );
}
