import { useEffect, useState } from "react";

/** A text property that is written when the field loses focus. */
export function Text({ label, value, disabled, hint, onCommit }: { label: string; value: string; disabled: boolean; hint?: string; onCommit(v: string): void }) {
  const [v, setV] = useState(value);
  useEffect(() => setV(value), [value]);
  return (
    <label>{label}
      <input disabled={disabled} value={v} onChange={(e) => setV(e.target.value)} onBlur={() => v !== value && onCommit(v)} />
      {hint && <small>{hint}</small>}
    </label>
  );
}
