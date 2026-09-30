// The settings form for one step, drawn from its descriptor. One renderer, many
// descriptors: sections and fields come from the registry; the parts every step
// shares (its kind, needs and produces, what happens when it fails, anything the
// descriptor does not know) are added here.
import { useMemo, type ReactNode } from "react";
import type { BlockNode, BlockSchema, Catalog } from "../../api/types";
import { BlockForm } from "../../forms/BlockForm";
import { ScalarField } from "../../forms/ScalarField";
import { useForm } from "../../forms/ctx";
import { quote, unquote } from "../../lib/bcl";
import { blocksOf, fieldOf } from "../../lib/paths";
import { Icon } from "../icons";
import { FAMILIES, typeLabel } from "../labels";
import { typeNameOf, visualFor } from "../kinds";
import type { CanvasGraph, IntentGraph, ProcessGraph } from "../model";
import { FactsSection, ReliabilitySection } from "../panels/sections";
import { Choice, Row, Section, Setting, Toggle, useBlockIO, useIntentIds } from "../panels/kit";
import { ConditionBuilder } from "./ConditionBuilder";
import { SettingsProvider, factsFor, useSettings } from "./context";
import { FieldView, valuesOf } from "./fields";
import { actionFields, coveredKeys, isGeneric, resolveNodeType } from "./resolve";
import type { FieldDef, NodeTypeDef, Problem, SectionDef } from "./types";
import "./builtins";

/** A schema without the named fields (what the descriptor and the common sections already show). */
function without(schema: BlockSchema | undefined, names: string[]): BlockSchema | undefined {
  return schema && { ...schema, fields: schema.fields.filter((f) => !names.includes(f.name)) };
}

interface Props {
  node: BlockNode;
  graph: CanvasGraph;
  catalog?: Catalog | null;
  schema?: BlockSchema;
  kind: "node" | "step";
  onSetStart?(): void;
}

function Intro({ def, typeName, generic, uses, catalog }: { def: NodeTypeDef; typeName: string; generic: boolean; uses?: string; catalog?: Catalog | null }) {
  const info = catalog?.node_types.find((t) => t.name === typeName);
  const v = visualFor(typeName, catalog);
  const action = uses ? catalog?.actions.find((a) => a.name === uses) : undefined;
  return (
    <div className={`cv-intro tone-${def.category === "process" ? "process" : v.tone}`}>
      <span className="cv-ico big"><Icon name={def.icon} size={18} /></span>
      <div>
        <strong>{def.label}</strong>
        <p>{def.blurb ?? info?.summary ?? FAMILIES[def.category]?.blurb ?? ""}</p>
        {action && <p className="muted">Uses <code>{action.name}</code>: {action.summary}</p>}
        {generic && <p className="muted">Its settings come from what the platform says this step takes.</p>}
      </div>
    </div>
  );
}

/** Everything wrong or missing in the fields that are showing, in plain words. */
export function problemsFor(def: NodeTypeDef, sections: readonly SectionDef[], node: BlockNode): Problem[] {
  const all = sections.flatMap((s) => s.fields);
  const values = valuesOf(node, all);
  const out: Problem[] = [];
  for (const s of sections) {
    for (const f of s.fields) {
      if (f.visibleWhen && !f.visibleWhen(values)) continue;
      const raw = values.raw(f.id);
      if (f.required && (raw === undefined || raw.trim() === "" || raw.trim() === "[]")) out.push({ field: f.id, message: `“${f.label}” is needed.` });
      const bad = f.validate?.(raw, values);
      if (bad) out.push({ field: f.id, message: bad });
    }
  }
  out.push(...(def.validate?.(values) ?? []));
  return out;
}

function SectionView({ section, values }: { section: SectionDef; values: ReturnType<typeof valuesOf> }) {
  const shown = section.fields.filter((f) => !f.advanced && (!f.visibleWhen || f.visibleWhen(values)));
  if (shown.length === 0) return null;
  return (
    <Section title={section.title} hint={section.hint} id={section.id} open={section.open ?? true}>
      {shown.map((f) => <FieldView key={f.id} field={f} />)}
    </Section>
  );
}

/** Settings the step has that nothing above knows about: shown as they are, never dropped. */
function OtherSettings({ node, covered }: { node: BlockNode; covered: ReadonlySet<string> }) {
  const { io, readOnly } = useSettings();
  const { file } = useForm();
  const config = blocksOf(node, "config")[0];
  const loose = (config?.children ?? []).filter((c) => c.kind === "field" && !covered.has(`config.${c.name}`));
  const blocks = (config?.children ?? []).filter((c) => c.kind === "block" && !covered.has(`config.${c.type}`));
  if (loose.length === 0 && blocks.length === 0) return null;
  const configOnly = config && { ...config, children: blocks };
  return (
    <div className="cv-other">
      <p className="hint">These settings are not part of the standard form, so they are shown as they are written.</p>
      {loose.map((c) => (
        <ScalarField
          key={c.path} label={c.name ?? ""} kind="any" raw={c.raw} path={c.path} readOnly={readOnly}
          onCommit={(raw) => io.edit([raw === undefined ? { op: "removeField", file, path: c.path } : { op: "setField", file, path: c.path, value: raw }])}
        />
      ))}
      {configOnly && blocks.length > 0 && <BlockForm node={configOnly} schema={{ fields: [] } as unknown as BlockSchema} blockType="config" />}
    </div>
  );
}

export function SettingsPanel({ node, graph, catalog, schema, kind, onSetStart }: Props) {
  const io = useBlockIO(node);
  const intentIds = useIntentIds();
  const scope = kind === "node" ? "flow" : "process";
  const typeName = kind === "node" ? typeNameOf(node, catalog) : (unquote(fieldOf(node, "family")?.raw ?? "") ?? fieldOf(node, "family")?.raw?.trim() ?? "action");
  const uses = kind === "node" ? (unquote(fieldOf(node, "uses")?.raw ?? "") ?? undefined) : undefined;
  const { def, generic } = useMemo(() => resolveNodeType(typeName, uses, catalog), [typeName, uses, catalog]);

  // The descriptor's sections for this scope, plus a section generated from the action's own config for anything it left out.
  const sections = useMemo<SectionDef[]>(() => {
    const own = def.sections.filter((s) => !s.scope || s.scope === scope);
    const extra = def.actionConfig === false || scope !== "flow" ? [] : actionFields(uses, catalog, coveredKeys(def));
    return extra.length ? [...own, { id: "action-settings", title: own.length ? "More of what it takes" : "Settings", fields: extra }] : own;
  }, [def, scope, uses, catalog]);

  const facts = factsFor(graph, node.id ?? "");
  const allFields = sections.flatMap((s) => s.fields);
  const values = valuesOf(node, allFields);
  const problems = problemsFor(def, sections, node);
  const advanced = allFields.filter((f) => f.advanced && (!f.visibleWhen || f.visibleWhen(values)));
  const covered = new Set<string>([...coveredKeys(def), ...allFields.map((f) => f.at.join("."))]);
  const handledTop = new Set<string>(["family", "uses", "description", "requires", "provides", "timeout", "retry", "bulkhead", "on_error", "config", "intent", "process", "terminal", "skip_when", "compensate", "circuit_breaker", "task", "resource"]);
  for (const f of allFields) handledTop.add(f.at[0]);
  // What the form above does not show, as the step with only those settings left: a BlockForm lists whatever
  // it is given that its schema lacks, so the handled ones must not be in it.
  const rest: BlockNode = { ...node, children: (node.children ?? []).filter((c) => !handledTop.has((c.kind === "field" ? c.name : c.type) ?? "")) };
  const kinds = (catalog?.node_types ?? []).filter((t) => kind === "step" || !t.durable);
  const actions = (catalog?.actions ?? []).map((a) => a.name);
  const isStart = kind === "step" && (graph as ProcessGraph).start === node.id;
  const intents = graph.kind === "intent" ? (graph as IntentGraph) : undefined;

  // A step that names no `family` still has a kind (derived from its action): show it, and only write when it is changed.
  const familyField = io.field("family");
  const familyIo = familyField.raw === undefined ? { ...familyField, raw: typeName } : familyField;

  const ctx = { node, io, catalog, graph, readOnly: io.readOnly, needs: facts.needs, produces: facts.produces, available: facts.available };

  return (
    <SettingsProvider value={ctx}>
      <div className="cv-typepanel" data-type={typeName} data-descriptor={def.id}>
        <Intro def={def} typeName={typeName} generic={generic || isGeneric(def)} uses={uses} catalog={catalog} />
        {problems.length > 0 && (
          <div className="cv-todo" role="status" aria-label="Still to do">
            <strong>To finish this step</strong>
            <ul>{problems.map((p, i) => <li key={i}>{p.message}</li>)}</ul>
          </div>
        )}

        <Section title={kind === "node" ? "What it does" : "What happens here"} id="what">
          <Choice
            io={familyIo} label="Kind of step" readOnly={io.readOnly}
            options={kinds.map((t) => ({ value: t.name, label: typeLabel(t.name) + (kind === "step" && t.durable ? " (waits)" : ""), hint: t.summary }))}
          />
          {kind === "node" ? (
            <Setting io={io.field("uses")} label="Action" suggestions={actions} doc="The behaviour this step runs." />
          ) : (
            <>
              <Choice io={io.field("intent")} label="Run this flow" bare={false} readOnly={io.readOnly} options={intentIds.map((i) => ({ value: i, label: i }))} doc="The work this step does when it is reached." />
              <Setting io={io.field("process")} label="…or start this process" placeholder="another process" />
            </>
          )}
          <Setting io={io.field("description")} label="Note" placeholder="what this step is for" />
          {kind === "step" && (
            <div className="cv-toggles">
              <Toggle io={{ path: "", raw: isStart ? "true" : undefined, set: (v) => v && onSetStart?.() }} label="This is the first step" readOnly={io.readOnly || isStart} />
              <Toggle io={io.field("terminal")} label="The run finishes after this step" readOnly={io.readOnly} doc="Ends the run successfully, even if lines lead on from here." />
            </div>
          )}
        </Section>

        {sections.map((s) => <SectionView key={s.id} section={s} values={values} />)}

        {intents && <FactsSection io={io} graph={intents} selfId={node.id ?? ""} />}
        {kind === "step" && (
          <Section title="Skip it when" id="skip" open={false}>
            <SkipWhen />
          </Section>
        )}
        <ReliabilitySection io={io} kind={kind} />

        <Section title="More settings" id="advanced" open={false}>
          {advanced.length > 0 && <div className="cv-group">{advanced.map((f) => <FieldView key={f.id} field={f} />)}</div>}
          <OtherSettings node={node} covered={covered} />
          <BlockForm node={rest} schema={without(schema, [...handledTop])} blockType={kind === "node" ? "node" : "step"} />
        </Section>
      </div>
    </SettingsProvider>
  );
}

/** The "skip this step when" condition of a process step. */
function SkipWhen(): ReactNode {
  const { io, readOnly } = useSettings();
  const f = io.field("skip_when");
  const cur = f.raw === undefined ? undefined : (unquote(f.raw) ?? f.raw.trim());
  return (
    <ConditionBuilder
      label="Skip this step when" value={cur} path={f.path} readOnly={readOnly} lead="Skip it when"
      onChange={(v) => f.set(v === undefined ? undefined : quote(v))}
    />
  );
}

export { Row };
export type { FieldDef };
