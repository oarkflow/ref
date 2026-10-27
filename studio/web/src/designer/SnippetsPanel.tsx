import { useState } from "react";
import type { Op } from "../api/types";
import {
  PALETTE, addCatalogInputOp, copyFormOps, deleteFormOps, deleteSharedOps, formUsage, renameSharedOps, usesOfShared,
  type DesignForm, type DesignInput, type DesignStage, type PipelineRef,
} from "./model";

interface Props {
  file: string; pipelinePath: string; pipelineName: string;
  forms: DesignForm[]; catalog: DesignInput[]; stages: DesignStage[]; pipelines: PipelineRef[];
  canEdit: boolean; apply(ops: Op[]): void; onOpenForm(name: string): void;
}

/** Reusable pieces: shared fields (defined once, used by many forms) and the library of forms. */
export function SnippetsPanel(p: Props) {
  return (
    <div className="dz-layout">
      <div className="dz-layoutgrid">
        <SharedFields {...p} />
        <FormLibrary {...p} />
      </div>
    </div>
  );
}

/** A destructive button that asks once, in place. */
function Confirm({ label, ask, disabled, onYes }: { label: string; ask: string; disabled?: boolean; onYes(): void }) {
  const [open, setOpen] = useState(false);
  if (!open) return <button type="button" className="dz-link danger" disabled={disabled} onClick={() => setOpen(true)}>{label}</button>;
  return (
    <span className="dz-confirm" role="group" aria-label={ask}>
      <small>{ask}</small>
      <button type="button" className="dz-link danger" onClick={() => { setOpen(false); onYes(); }}>Yes, {label.toLowerCase()}</button>
      <button type="button" className="dz-link" onClick={() => setOpen(false)}>Cancel</button>
    </span>
  );
}

function SharedFields({ file, pipelinePath, forms, catalog, canEdit, apply }: Props) {
  const [kind, setKind] = useState("text");
  const [label, setLabel] = useState("");
  const item = { ...(PALETTE.find((x) => x.kind === kind) ?? PALETTE[0]!), label: label.trim() || (PALETTE.find((x) => x.kind === kind)?.label ?? "Field") };
  return (
    <section className="dz-groups" aria-label="Shared fields">
      <h3>Shared fields</h3>
      <p className="dz-hint">A shared field is defined once and listed by any number of forms. Change it here and every form that uses it changes.</p>
      {catalog.length === 0 && <p className="dz-hint">No shared fields yet. Add one below, or tick “Add as shared field” in the Design palette.</p>}
      {catalog.map((c) => {
        const users = usesOfShared(forms, c.name);
        return <SharedRow key={c.path} field={c} users={users} canEdit={canEdit} taken={catalog.map((x) => x.name)}
          onRename={(n) => apply(renameSharedOps(file, pipelinePath, forms, c, n))} onDelete={() => apply(deleteSharedOps(file, forms, c))} />;
      })}
      {canEdit && (
        <div className="dz-addlookup">
          <h4>New shared field</h4>
          <input aria-label="Label of the new shared field" placeholder="label, e.g. Contact email" value={label} onChange={(e) => setLabel(e.target.value)} />
          <select aria-label="Type of the new shared field" value={kind} onChange={(e) => setKind(e.target.value)}>{PALETTE.map((x) => <option key={x.kind} value={x.kind}>{x.label}</option>)}</select>
          <button type="button" className="dz-primary" disabled={!label.trim()} onClick={() => { apply([addCatalogInputOp(file, pipelinePath, catalog, item)]); setLabel(""); }}>Add</button>
        </div>
      )}
    </section>
  );
}

function SharedRow({ field: c, users, canEdit, taken, onRename, onDelete }: { field: DesignInput; users: string[]; canEdit: boolean; taken: string[]; onRename(n: string): void; onDelete(): void }) {
  const [name, setName] = useState(c.name);
  const clash = name !== c.name && taken.includes(name);
  return (
    <div className="dz-lookup">
      <h4>{c.label || c.name} <small className="dz-hint">{c.kind}{c.lookup ? ` · lookup ${c.lookup}` : ""}</small></h4>
      <p className="dz-hint">{users.length ? `Used by ${users.join(", ")}` : "Not used by any form yet."}</p>
      {canEdit && (
        <div className="dz-rowact">
          <label className="dz-inline">Name <input aria-label={`Name of ${c.name}`} value={name} onChange={(e) => setName(e.target.value.replace(/[^A-Za-z0-9_]/g, "_"))} /></label>
          <button type="button" className="dz-link" disabled={!name || name === c.name || clash} onClick={() => onRename(name)}>Rename{users.length ? ` (updates ${users.length} form${users.length > 1 ? "s" : ""})` : ""}</button>
          {clash && <small className="dz-hint err">Another shared field has that name.</small>}
          <Confirm label="Delete" ask={users.length ? `Delete it and remove it from ${users.length} form${users.length > 1 ? "s" : ""}?` : "Delete this shared field?"} onYes={onDelete} />
        </div>
      )}
    </div>
  );
}

function FormLibrary({ file, pipelinePath, pipelineName, forms, catalog, stages, pipelines, canEdit, apply, onOpenForm }: Props) {
  const others = pipelines.filter((x) => x.name !== pipelineName && x.forms.length > 0);
  const [from, setFrom] = useState("");
  const [which, setWhich] = useState("");
  const source = others.find((x) => x.name === from) ?? others[0];
  const picked = source?.forms.find((f) => f.name === which) ?? source?.forms[0];
  return (
    <section className="dz-lpreview" aria-label="Form library">
      <h3>Forms</h3>
      <p className="dz-hint">Each form is a reusable block: stages show it by listing it in a page group, so one form can appear on several stages.</p>
      <table>
        <thead><tr><th>Form</th><th>Fields</th><th>Shown on</th><th /></tr></thead>
        <tbody>
          {forms.map((f) => {
            const where = formUsage(stages, f.name);
            return (
              <tr key={f.path}>
                <td><b>{f.title || f.name}</b>{f.repeatable && <em className="dz-kind">repeatable</em>}<br /><code>{f.name}</code></td>
                <td>{f.inputs.length + f.shared.length}{f.shared.length ? ` (${f.shared.length} shared)` : ""}</td>
                <td>{where.length ? where.join("; ") : <span className="dz-hint">not shown anywhere</span>}</td>
                <td className="dz-actcell">
                  <button type="button" className="dz-link" onClick={() => onOpenForm(f.name)}>Open</button>
                  {canEdit && <button type="button" className="dz-link" onClick={() => apply(copyFormOps(file, pipelinePath, forms, f, catalog, " (copy)", true))}>Duplicate</button>}
                  {canEdit && <Confirm label="Delete" ask={where.length ? `Delete it and remove it from ${where.length} page group${where.length > 1 ? "s" : ""}?` : "Delete this form?"} onYes={() => apply(deleteFormOps(file, stages, f))} />}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {canEdit && (
        <div className="dz-addlookup">
          <h4>Copy a form from another pipeline</h4>
          {others.length === 0 ? <p className="dz-hint">No other pipeline here has forms to copy.</p> : (
            <>
              <select aria-label="Pipeline to copy from" value={source?.name} onChange={(e) => { setFrom(e.target.value); setWhich(""); }}>{others.map((x) => <option key={x.name} value={x.name}>{x.name}</option>)}</select>
              <select aria-label="Form to copy" value={picked?.name} onChange={(e) => setWhich(e.target.value)}>{source?.forms.map((f) => <option key={f.name} value={f.name}>{f.title || f.name}</option>)}</select>
              <button type="button" className="dz-primary" disabled={!picked} onClick={() => picked && source && apply(copyFormOps(file, pipelinePath, forms, picked, source.catalog, ""))}>Copy here</button>
              <p className="dz-hint">The copy is independent: its shared fields become ordinary fields of the new form. Add it to a page group on the Page layout tab.</p>
            </>
          )}
        </div>
      )}
    </section>
  );
}

