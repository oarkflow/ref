import { useEffect, useMemo, useState } from "react";
import { useShallow } from "zustand/react/shallow";
import type { BlockNode, BlockSchema } from "../api/types";
import { BlockForm } from "../forms/BlockForm";
import { FormProvider, type FormContext } from "../forms/ctx";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";
import { Icon } from "./icons";
import { typeNameOf, visualFor } from "./kinds";
import { edgeLabel, typeLabel } from "./labels";
import { readEdgeBlock, type CanvasGraph, type EdgeTypeInfo } from "./model";
import { EdgeTypePanel } from "./panels/TypePanel";
import { SettingsPanel } from "./registry/SettingsPanel";

/** The schema of a child block ("node" in "intent", "edge" in "process", ...). */
export function childSchema(schemas: Record<string, BlockSchema> | null, rootType: string, childType: string): BlockSchema | undefined {
  return schemas?.[rootType]?.fields.find((f) => f.name === childType)?.block;
}

/** An edge form shows only the fields its type reads (plus the always-relevant ones). */
export function edgeFormSchema(schema: BlockSchema | undefined, info: EdgeTypeInfo | undefined): BlockSchema | undefined {
  if (!schema || !info?.fields?.length) return schema;
  const keep = new Set([...info.fields, "kind", "condition", "when", "description"]);
  return { ...schema, fields: schema.fields.filter((f) => keep.has(f.name)) };
}

interface Props {
  file: string;
  rootType: string;
  node: BlockNode;
  graph: CanvasGraph;
  /** Renames the block and everything that references it; returns an error text or null. */
  onRename(newId: string): string | null;
  onRemove(): void;
  onSetStart(id: string): void;
}

export function NodePanel({ file, rootType, node, graph, onRename, onRemove, onSetStart }: Props) {
  const s = useStudio(
    useShallow((st) => ({ schemas: st.schemas, catalog: st.catalog, diagnostics: st.diagnostics, focusPath: st.focusPath, overlay: st.overlay, meta: st.meta })),
  );
  const store = useStudioStore();
  const readOnly = !hasRole(s.meta, "editor");
  const [renaming, setRenaming] = useState(false);
  const [id, setId] = useState(node.id ?? "");
  const [confirm, setConfirm] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    setId(node.id ?? "");
    setRenaming(false);
    setConfirm(false);
    setError(null);
  }, [node.path, node.id]);

  const type = node.type ?? "";
  const schema = useMemo(() => childSchema(s.schemas, rootType, type), [s.schemas, rootType, type]);
  const kind = readEdgeBlock(node).kind;
  const edgeSchema = useMemo(
    () => (type === "edge" ? edgeFormSchema(schema, s.catalog?.edge_types.find((e) => e.name === kind)) : schema),
    [type, schema, s.catalog, kind],
  );

  const ctx: FormContext = {
    file,
    schemas: s.schemas,
    catalog: s.catalog,
    diagnostics: s.diagnostics,
    focusPath: s.focusPath,
    overlay: s.overlay,
    readOnly,
    edit: (ops) => store.getState().edit(ops),
  };

  const typeName = type === "edge" ? kind : typeNameOf(node, s.catalog);
  const visual = visualFor(typeName, s.catalog);
  const heading = type === "edge" ? `${edgeLabel(kind)}` : typeLabel(typeName);
  const noun = type === "node" || type === "step" ? "Step" : type === "edge" ? "Connection" : type === "stage" ? "Stage" : type;

  let body: React.ReactNode;
  if (graph.kind === "intent" && type === "node") {
    body = <SettingsPanel kind="node" node={node} graph={graph} catalog={s.catalog} schema={schema} />;
  } else if (graph.kind === "process" && type === "step") {
    body = <SettingsPanel kind="step" node={node} graph={graph} catalog={s.catalog} schema={schema} onSetStart={() => onSetStart(node.id ?? "")} />;
  } else if (graph.kind === "process" && type === "edge") {
    body = <EdgeTypePanel node={node} graph={graph} catalog={s.catalog} schema={edgeSchema} />;
  } else {
    body = <BlockForm node={node} schema={edgeSchema} blockType={type} />;
  }

  return (
    <div className="cv-panel" data-testid="node-panel">
      <header className={`cv-panel-head tone-${visual.tone}`}>
        <span className="cv-ico big"><Icon name={type === "edge" ? "branch" : visual.icon} size={18} /></span>
        <div className="cv-panel-title">
          <p className="crumb">{noun} · {heading}</p>
          {renaming ? (
            <form
              className="rename"
              onSubmit={(e) => {
                e.preventDefault();
                const err = onRename(id);
                setError(err);
                if (!err) setRenaming(false);
              }}
            >
              <input aria-label="New name" value={id} onChange={(e) => setId(e.target.value)} autoFocus spellCheck={false} />
              <button type="submit">Rename</button>
              <button type="button" className="link" onClick={() => setRenaming(false)}>Cancel</button>
            </form>
          ) : (
            <h2>{node.id ?? heading}</h2>
          )}
          {error && <p className="problem error" role="alert">{error}</p>}
        </div>
        {!readOnly && (
          <div className="cv-panel-actions">
            {!renaming && <button type="button" onClick={() => setRenaming(true)}>Rename</button>}
            {confirm ? (
              <>
                <button type="button" className="danger" onClick={onRemove}>Confirm remove</button>
                <button type="button" onClick={() => setConfirm(false)}>Cancel</button>
              </>
            ) : (
              <button type="button" className="danger" onClick={() => setConfirm(true)}>Remove</button>
            )}
          </div>
        )}
      </header>
      <FormProvider value={ctx}>{body}</FormProvider>
    </div>
  );
}
