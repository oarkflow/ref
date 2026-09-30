import { ArrowLeft, ArrowRight } from "lucide-react";
import { useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { CATEGORIES, BLOCKS, blockInfo, lc, type CategoryId } from "../labels";
import { collectItems } from "../nav/model";
import { suggestFile } from "../nav/model";
import { QUICK, buildBody, defaultAnswers, type QuickField } from "../nav/quickstart";
import { useStudio, useStudioStore } from "../state/context";
import { uiStore, useUi } from "../state/ui";
import { Icon } from "../ui/Icon";
import { motionOff } from "../ui/motion";
import { Presence, motion } from "../ui/motion-components";
import { Dialog } from "./Dialog";

export function AddWizardHost() {
  const arg = useUi((s) => s.addOpen);
  return <Presence mode="sync">{arg && <AddWizard key="wizard" initialCategory={arg.category as CategoryId | undefined} initialType={arg.type} />}</Presence>;
}

function AddWizard({ initialCategory, initialType }: { initialCategory?: CategoryId; initialType?: string }) {
  const store = useStudioStore();
  const navigate = useNavigate();
  const nav = useStudio((s) => s.nav);
  const trees = useStudio((s) => s.trees);
  const draft = useStudio((s) => s.draft);
  const catalog = useStudio((s) => s.catalog);
  const schemas = useStudio((s) => s.schemas);
  const dev = useUi((s) => s.devView);
  const [type, setType] = useState<string | null>(initialType ?? null);
  const [dir, setDir] = useState(1);
  const [showAll, setShowAll] = useState(!initialCategory);
  const [name, setName] = useState("");
  const [answers, setAnswers] = useState<Record<string, string>>(() => defaultAnswers(initialType ?? ""));
  const [file, setFile] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const close = () => uiStore.getState().closeAdd();

  const items = useMemo(() => collectItems(nav, trees), [nav, trees]);
  const files = draft?.files ?? [];
  const info = type ? blockInfo(type) : null;
  const needsId = type ? (schemas?.[type]?.has_id ?? true) : true;
  const target = file ?? (type ? suggestFile(type, nav, files) : null);
  const taken = !!type && items.some((i) => i.type === type && i.id === name.trim());
  const missing = (QUICK[type ?? ""] ?? []).filter((f) => f.required && !(answers[f.name] ?? "").trim());
  const valid = !!type && !!target && (!needsId || (!!name.trim() && !taken)) && missing.length === 0;

  const choose = (t: string) => {
    setDir(1);
    setType(t);
    setName("");
    setAnswers(defaultAnswers(t));
    setFile(null);
  };
  const back = () => { setDir(-1); setType(null); };

  const submit = async () => {
    if (!valid || !type || !target) return;
    setBusy(true);
    const id = name.trim();
    store.getState().edit([{ op: "addBlock", file: target, type, ...(needsId ? { id } : {}), body: buildBody(type, answers) }]);
    await store.getState().flush();
    await store.getState().ensureTrees();
    close();
    await store.getState().select(target, needsId ? `${type}/${id}` : type);
    navigate("/edit");
  };

  const cats = (showAll ? CATEGORIES : CATEGORIES.filter((c) => c.id === initialCategory)).filter((c) => Object.values(BLOCKS).some((b) => b.category === c.id));
  const off = motionOff();
  const Step = off ? "div" : motion.div;
  const stepAnim = off ? {} : { initial: { opacity: 0, x: dir * 28 }, animate: { opacity: 1, x: 0 }, exit: { opacity: 0, x: dir * -28 }, transition: { duration: 0.2, ease: [0.16, 1, 0.3, 1] as const } };

  return (
    <Dialog title={type ? `Add ${lc(info!.label)}` : "What would you like to add?"} subtitle={type ? info!.description : "Pick one. You can fill in the details next."} onClose={close} wide>
      <div className="wizard-steps" aria-hidden="true"><span className={!type ? "on" : "done"} /><span className={type ? "on" : ""} /></div>
      <Presence mode="wait" initial={false}>
        {!type ? (
          <Step key="pick" className="wizard-step" {...stepAnim}>
            {cats.map((c) => (
              <section key={c.id} className="type-section">
                <h3 className="type-section-title"><Icon name={c.icon} size={16} /> {c.label}</h3>
                <div className="type-grid">
                  {Object.values(BLOCKS).filter((b) => b.category === c.id).map((b) => (
                    <button key={b.raw} type="button" className="type-card" onClick={() => choose(b.raw)}>
                      <span className="type-icon"><Icon name={b.icon} size={22} /></span>
                      <span className="type-name">{b.label}</span>
                      <span className="type-desc">{b.description}</span>
                    </button>
                  ))}
                </div>
              </section>
            ))}
            {!showAll && <button type="button" className="link-btn" onClick={() => setShowAll(true)}>Show everything I can add</button>}
          </Step>
        ) : (
          <Step key="details" className="wizard-step" {...stepAnim}>
            <form className="form-stack" onSubmit={(e) => { e.preventDefault(); void submit(); }}>
              {needsId && (
                <label className="fld">
                  <span className="fld-label">{info!.nameLabel ?? "Name"} <span className="req">*</span></span>
                  <input autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder={info!.namePlaceholder} spellCheck={false} aria-invalid={taken || undefined} />
                  {taken ? <span className="fld-error" role="alert">You already have one called “{name.trim()}”. Pick a different name.</span> : <span className="fld-help">This is how you’ll find it later. Letters, numbers, dots and dashes work best.</span>}
                </label>
              )}
              <div className="quick-grid">
                {(QUICK[type] ?? []).map((f) => (
                  <QuickInput key={f.name} f={f} value={answers[f.name] ?? ""} onChange={(v) => setAnswers((a) => ({ ...a, [f.name]: v }))} suggestions={suggestionsFor(f, items, catalog)} />
                ))}
              </div>
              {dev && (
                <label className="fld">
                  <span className="fld-label">Save in file</span>
                  <select value={target ?? ""} onChange={(e) => setFile(e.target.value)}>{files.map((f) => <option key={f} value={f}>{f}</option>)}</select>
                </label>
              )}
              <div className="dialog-actions between">
                <button type="button" className="btn" onClick={back}><ArrowLeft size={16} /> Back</button>
                <button type="submit" className="btn primary" disabled={!valid || busy}>Add and keep editing <ArrowRight size={16} /></button>
              </div>
            </form>
          </Step>
        )}
      </Presence>
    </Dialog>
  );
}

function suggestionsFor(f: QuickField, items: ReturnType<typeof collectItems>, catalog: ReturnType<typeof useStudio<import("../api/types").Catalog | null>>): string[] {
  if (f.name === "kind" && catalog) return catalog.resource_kinds.map((k) => k.name);
  if (f.from === "intent") return items.filter((i) => i.type === "intent").map((i) => i.id);
  if (f.from === "database") return items.filter((i) => i.type === "resource").map((i) => i.id);
  return [];
}

function QuickInput({ f, value, onChange, suggestions }: { f: QuickField; value: string; onChange(v: string): void; suggestions: string[] }) {
  const id = `q-${f.name}`;
  return (
    <label className="fld" htmlFor={id}>
      <span className="fld-label">{f.label}{f.required && <span className="req"> *</span>}</span>
      {f.options ? (
        <select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
          {f.options.map((o) => <option key={o} value={o}>{o === "true" ? "On" : o === "false" ? "Off" : o}</option>)}
        </select>
      ) : (
        <>
          <input id={id} value={value} onChange={(e) => onChange(e.target.value)} placeholder={f.placeholder} spellCheck={false} list={suggestions.length ? `${id}-list` : undefined} />
          {suggestions.length > 0 && <datalist id={`${id}-list`}>{suggestions.map((s) => <option key={s} value={s} />)}</datalist>}
        </>
      )}
      {f.help && <span className="fld-help">{f.help}</span>}
    </label>
  );
}

