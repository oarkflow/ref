import { useEffect, useRef, useState } from "react";
import type { FileContent } from "../api/types";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";

/** The raw source of one file: read-only until Edit is pressed. */
export function FileTextView({ file }: { file: string }) {
  const store = useStudioStore();
  const draft = useStudio((s) => s.draft);
  const meta = useStudio((s) => s.meta);
  const focusLine = useStudio((s) => s.focusLine);
  const version = draft?.version;
  const [data, setData] = useState<FileContent | null>(null);
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState("");
  const [saving, setSaving] = useState(false);
  const lineRef = useRef<HTMLLIElement>(null);

  useEffect(() => {
    if (!draft) return;
    let live = true;
    setData(null);
    store.getState().api.getFile(draft.id, file).then((d) => {
      if (live) {
        setData(d);
        setText(d.content);
      }
    }).catch((e) => store.getState().notify("error", String(e.message ?? e)));
    return () => { live = false; };
  }, [draft?.id, file, version, store]);

  useEffect(() => {
    lineRef.current?.scrollIntoView?.({ block: "center" });
  }, [data, focusLine]);

  if (!data) return <p className="empty" aria-busy="true">Loading {file}…</p>;
  const canEdit = hasRole(meta, "editor");
  const lines = data.content.split("\n");

  const save = async () => {
    setSaving(true);
    const ok = await store.getState().setFileText(file, text);
    setSaving(false);
    if (ok) setEditing(false);
  };

  return (
    <div className="source">
      <div className="source-bar">
        <span className="mono">{file}</span>
        {canEdit && !editing && <button type="button" onClick={() => setEditing(true)}>Edit as text</button>}
        {editing && (
          <>
            <button type="button" className="primary" onClick={() => void save()} disabled={saving || text === data.content}>Save</button>
            <button type="button" onClick={() => { setEditing(false); setText(data.content); }}>Cancel</button>
          </>
        )}
      </div>
      {editing ? (
        <textarea className="mono source-edit" aria-label={`Edit ${file}`} value={text} spellCheck={false} onChange={(e) => setText(e.target.value)} />
      ) : (
        <ol className="source-lines mono" aria-label={`${file} source`}>
          {lines.map((l, i) => (
            <li key={i} ref={i + 1 === focusLine ? lineRef : undefined} className={i + 1 === focusLine ? "hot" : ""}>
              <code>{l || " "}</code>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}
