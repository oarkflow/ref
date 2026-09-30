// The configuration panel for a connection between two steps. (Steps are drawn
// by registry/SettingsPanel from the node-type registry.)
import type { BlockNode, BlockSchema, Catalog } from "../../api/types";
import { BlockForm } from "../../forms/BlockForm";
import { blocksOf } from "../../lib/paths";
import { familyLabel, edgeLabel } from "../labels";
import { Icon } from "../icons";
import { edgeShape, readEdgeBlock, type EdgeBlockInfo, type ProcessGraph } from "../model";
import { AddName, Choice, ExprBox, NameChips, Row, Section, useBlockIO, useStringList, type BlockIO } from "./kit";

/** A schema with only (or without) the named fields. */
export function pick(schema: BlockSchema | undefined, names: string[]): BlockSchema | undefined {
  return schema && { ...schema, fields: schema.fields.filter((f) => names.includes(f.name)) };
}
export function omit(schema: BlockSchema | undefined, names: string[]): BlockSchema | undefined {
  return schema && { ...schema, fields: schema.fields.filter((f) => !names.includes(f.name)) };
}

// ---------------------------------------------------------------------------
// connections

const PLAIN = new Set(["simple", "branch"]);

export function EdgeTypePanel({ node, graph, catalog, schema }: { node: BlockNode; graph: ProcessGraph; catalog?: Catalog | null; schema?: BlockSchema }) {
  const io = useBlockIO(node);
  const info: EdgeBlockInfo = readEdgeBlock(node);
  const et = catalog?.edge_types.find((e) => e.name === info.kind);
  const shape = edgeShape(et);
  const fields = et?.fields ?? [];
  const steps = graph.steps.map((s) => s.id);
  const sources = useStringList(io.field("sources"));
  const targets = useStringList(io.field("targets"));
  const stepOpts = steps.map((s) => ({ value: s, label: s }));
  const shown = fields.filter((f) => !["from", "to", "sources", "targets", "condition", "data"].includes(f));
  const handled = ["kind", "from", "to", "sources", "targets", "condition", "when", "description"];

  // grouped kinds for the picker
  const opts = (catalog?.edge_types ?? []).map((e) => ({ value: e.name, label: `${edgeLabel(e.name)}${e.parks ? " (waits)" : ""}`, hint: e.summary }));

  return (
    <div className="cv-typepanel" data-type={`edge-${info.kind}`}>
      <div className={`cv-intro tone-flow`}>
        <span className="cv-ico big"><Icon name="branch" size={18} /></span>
        <div>
          <strong>{edgeLabel(info.kind)}</strong>
          <p>{et?.summary ?? "How the run moves from one step to the next."}</p>
          {et?.parks && <p className="muted">The run waits here until it is time to continue.</p>}
          {et?.error_path && <p className="muted">Only followed when the previous step fails.</p>}
        </div>
      </div>
      <Section title="How steps connect" id="connect">
        <Choice io={io.field("kind")} label="Kind of connection" options={opts} readOnly={io.readOnly} allowNone={false} />
        {shape === "single" && (
          <Row>
            <Choice io={io.field("from")} label="From" bare={false} options={stepOpts} readOnly={io.readOnly} />
            <Choice io={io.field("to")} label="To" bare={false} options={stepOpts} readOnly={io.readOnly} />
          </Row>
        )}
        {shape === "forward" && (
          <>
            <Choice io={io.field("from")} label="From" bare={false} options={stepOpts} readOnly={io.readOnly} />
            <StepList label="Then all of" list={targets} steps={steps} readOnly={io.readOnly} />
          </>
        )}
        {shape === "backward" && (
          <>
            <StepList label="Once all of" list={sources} steps={steps} readOnly={io.readOnly} />
            <Choice io={io.field("to")} label="Continue to" bare={false} options={stepOpts} readOnly={io.readOnly} />
          </>
        )}
        {shape === "none" && <p className="hint">This kind of connection picks its targets at run time. Its settings are below.</p>}
        {fields.includes("condition") && (
          <ExprBox io={io.field("condition")} label={PLAIN.has(info.kind) && info.kind === "branch" ? "Only when" : "Only when (optional)"} placeholder="e.g. result.action == 'approve'" rows={2} readOnly={io.readOnly} doc="The connection is followed only when this is true." />
        )}
      </Section>
      {shown.length > 0 && (
        <Section title={`About “${edgeLabel(info.kind)}”`} id="kind-settings">
          <BlockForm node={node} schema={pick(schema, shown)} blockType="edge" />
        </Section>
      )}
      <Section title="More settings" id="advanced" open={false}>
        <BlockForm node={node} schema={omit(schema, [...handled, ...shown])} blockType="edge" />
      </Section>
    </div>
  );
}

function StepList({ label, list, steps, readOnly }: { label: string; list: ReturnType<typeof useStringList>; steps: string[]; readOnly: boolean }) {
  if (!list.editable) return <p className="hint">This list is written as an expression; edit it under More settings.</p>;
  return (
    <div className="cv-field">
      <label>{label}</label>
      <NameChips names={list.names} readOnly={readOnly} empty="No steps yet" onRemove={(n) => list.write(list.raws.filter((_, i) => list.names[i] !== n))} />
      <AddName label={`Add a step to “${label}”`} placeholder="+ Add a step…" readOnly={readOnly} options={steps.filter((s) => !list.names.includes(s))} onAdd={(n) => list.write([...list.raws, `"${n.replace(/"/g, '\\"')}"`])} />
    </div>
  );
}

export { blocksOf, familyLabel };
export type { BlockIO };
