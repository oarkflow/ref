import { useState } from "react";
import type { BlockNode, Op } from "../api/types";
import { Text } from "./Fields";
import { RuleEditor } from "./RuleEditor";
import {
  COMPLETE_RULES, OUTCOMES, addActionOp, parseStages, setActionOp, setStageOp, splitList, stageTargets,
  type DesignAction, type DesignStage, type FieldRef,
} from "./model";

interface Props { file: string; pipeline: BlockNode; refs: FieldRef[]; canEdit: boolean; apply(ops: Op[]): void }

const COMPLETE_LABEL: Record<string, string> = { "": "All work items (default)", all: "All work items", any: "Any one work item", quorum: "A quorum of work items" };

/** When each stage is skipped or may open, where it leads, when it is done, and what its actions offer. */
export function StagePanel({ file, pipeline, refs, canEdit, apply }: Props) {
  const stages = parseStages(pipeline);
  const [name, setName] = useState("");
  const idx = Math.max(stages.findIndex((s) => s.name === name), 0);
  const stage = stages[idx];
  if (!stage) return <p className="empty">This pipeline has no stages.</p>;
  const dis = !canEdit;
  const others = stages.filter((s) => s.name !== stage.name);

  return (
    <div className="dz-layout">
      <ol className="dz-flow" aria-label="Stage order">
        {stages.map((s, i) => (
          <li key={s.name} className={s.name === stage.name ? "on" : ""}>
            <button type="button" onClick={() => setName(s.name)}>
              <b>{i + 1}. {s.title || s.name}</b>
              <span>{[s.skipIf && "can be skipped", s.entryConditions.length && `${s.entryConditions.length} entry rule${s.entryConditions.length > 1 ? "s" : ""}`, s.requires.length && `needs ${s.requires.join(", ")}`].filter(Boolean).join(" · ") || "no conditions"}</span>
              <em>→ {stageTargets(stages, i).join(", ") || "end"}</em>
            </button>
          </li>
        ))}
      </ol>

      <div className="dz-layoutgrid">
        <section className="dz-groups" aria-label={`Rules for ${stage.name}`}>
          <h3>{stage.title || stage.name}</h3>
          <div className="dz-propform">
            <RuleEditor label="Skip this stage when" hint="Never skipped." value={stage.skipIf} fields={refs} disabled={dis} onCommit={(v) => apply([setStageOp(file, stage, "skip_if", v)])} />
            <EntryConditions stage={stage} refs={refs} disabled={dis} onChange={(list) => apply([setStageOp(file, stage, "entry_conditions", list)])} />
            <fieldset className="dz-requires">
              <legend>Needs these stages completed first</legend>
              {others.length === 0 && <small className="dz-hint">There are no other stages.</small>}
              {others.map((o) => (
                <label key={o.name} className="dz-inline"><input type="checkbox" disabled={dis} checked={stage.requires.includes(o.name)}
                  onChange={(e) => apply([setStageOp(file, stage, "requires", e.target.checked ? [...stage.requires, o.name] : stage.requires.filter((x) => x !== o.name))])} /> {o.title || o.name}</label>
              ))}
            </fieldset>
            <label>After this stage, go to
              <select disabled={dis} value={stage.next} onChange={(e) => apply([setStageOp(file, stage, "next", e.target.value)])}>
                <option value="">the next stage in order</option>
                {others.map((o) => <option key={o.name} value={o.name}>{o.title || o.name}</option>)}
              </select>
            </label>
            <div className="dz-two">
              <label>The stage is done when
                <select disabled={dis} value={stage.complete} onChange={(e) => apply([setStageOp(file, stage, "complete", e.target.value)])}>
                  {COMPLETE_RULES.map((c) => <option key={c} value={c}>{COMPLETE_LABEL[c]}</option>)}
                </select>
              </label>
              <label className="dz-inline dz-alignend"><input type="checkbox" disabled={dis} checked={stage.autoAdvance} onChange={(e) => apply([setStageOp(file, stage, "auto_advance", e.target.checked)])} /> Move on automatically</label>
            </div>
          </div>
        </section>

        <section className="dz-lpreview" aria-label="Actions">
          <h3>Actions</h3>
          <p className="dz-hint">The buttons people see here. An action can be offered only when a condition holds, and can send the case to a particular stage.</p>
          {stage.actions.length === 0 && <p className="dz-hint">No actions: the stage shows a default Submit.</p>}
          {stage.actions.map((a) => (
            <ActionCard key={a.path} file={file} action={a} refs={refs} stages={others} disabled={dis} apply={apply} />
          ))}
          {canEdit && <button type="button" className="dz-addgroup" onClick={() => apply([addActionOp(file, stage)])}>+ Add an action</button>}
        </section>
      </div>
    </div>
  );
}

function EntryConditions({ stage, refs, disabled, onChange }: { stage: DesignStage; refs: FieldRef[]; disabled: boolean; onChange(list: string[]): void }) {
  const list = stage.entryConditions;
  return (
    <div className="dz-entryconds">
      <b>The stage opens only when all of these hold</b>
      {list.length === 0 && <small className="dz-hint">No entry rules.</small>}
      {list.map((c, i) => (
        <div key={i} className="dz-entryrow">
          <RuleEditor label={`Rule ${i + 1}`} value={c} fields={refs} disabled={disabled} onCommit={(v) => onChange(v ? list.map((x, k) => (k === i ? v : x)) : list.filter((_, k) => k !== i))} />
          {!disabled && <button type="button" className="dz-link danger" onClick={() => onChange(list.filter((_, k) => k !== i))}>Remove rule {i + 1}</button>}
        </div>
      ))}
      {!disabled && <button type="button" className="dz-link" onClick={() => onChange([...list, refs[0] ? `${refs[0].ref} not empty` : ""].filter(Boolean))}>+ Add an entry rule</button>}
    </div>
  );
}

function ActionCard({ file, action: a, refs, stages, disabled, apply }: { file: string; action: DesignAction; refs: FieldRef[]; stages: DesignStage[]; disabled: boolean; apply(o: Op[]): void }) {
  const set = (prop: Parameters<typeof setActionOp>[2], v: string | boolean | string[]) => apply([setActionOp(file, a, prop, v)]);
  return (
    <div className="dz-esc">
      <b>{a.label || a.name}</b>
      <Text label="Button label" value={a.label} disabled={disabled} onCommit={(v) => set("label", v)} />
      <label>What it does
        <select disabled={disabled} value={a.outcome} onChange={(e) => set("outcome", e.target.value)}>
          {OUTCOMES.map((o) => <option key={o} value={o}>{({ advance: "Move to the next stage", return: "Send back for correction", reject: "Reject the case", approve: "Approve", withdraw: "Withdraw", hold: "Put on hold" } as Record<string, string>)[o]}</option>)}
        </select>
      </label>
      <RuleEditor label="Offer this action only when" hint="Always offered." value={a.condition} fields={refs} disabled={disabled} onCommit={(v) => set("condition", v)} />
      {a.outcome === "return" && (
        <label>Send back to
          <select disabled={disabled} value={a.returnTo} onChange={(e) => set("return_to", e.target.value)}>
            <option value="">the previous stage</option>
            {stages.map((s) => <option key={s.name} value={s.name}>{s.title || s.name}</option>)}
          </select>
        </label>
      )}
      {a.outcome === "advance" && (
        <label>Go to
          <select disabled={disabled} value={a.next} onChange={(e) => set("next", e.target.value)}>
            <option value="">the stage's usual next stage</option>
            {stages.map((s) => <option key={s.name} value={s.name}>{s.title || s.name}</option>)}
          </select>
        </label>
      )}
      <Text label="Only these roles may use it" value={a.roles.join(", ")} disabled={disabled} onCommit={(v) => set("roles", splitList(v))} />
      <Text label="Ask for confirmation" value={a.confirm} disabled={disabled} hint="Leave blank for none" onCommit={(v) => set("confirm", v)} />
      <label className="dz-inline"><input type="checkbox" disabled={disabled} checked={a.commentRequired} onChange={(e) => set("comment_required", e.target.checked)} /> A comment is required</label>
      {!disabled && <button type="button" className="dz-link danger" onClick={() => apply([{ op: "removeBlock", file, path: a.path }])}>Delete action</button>}
    </div>
  );
}
