export type DiffLine = { t: " " | "+" | "-" | "@"; text: string };

/** Line diff by longest common subsequence; fine for configuration files. */
export function diffLines(a: string, b: string): DiffLine[] {
  const al = a === "" ? [] : a.split("\n");
  const bl = b === "" ? [] : b.split("\n");
  // Trim the common prefix/suffix so the O(n*m) table only covers the changed middle.
  let lo = 0;
  while (lo < al.length && lo < bl.length && al[lo] === bl[lo]) lo++;
  let ha = al.length;
  let hb = bl.length;
  while (ha > lo && hb > lo && al[ha - 1] === bl[hb - 1]) {
    ha--;
    hb--;
  }
  const A = al.slice(lo, ha);
  const B = bl.slice(lo, hb);
  const n = A.length;
  const m = B.length;
  const dp: Uint32Array[] = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i]![j] = A[i] === B[j] ? dp[i + 1]![j + 1]! + 1 : Math.max(dp[i + 1]![j]!, dp[i]![j + 1]!);
    }
  }
  const out: DiffLine[] = al.slice(0, lo).map((text) => ({ t: " ", text }));
  let i = 0;
  let j = 0;
  while (i < n || j < m) {
    if (i < n && j < m && A[i] === B[j]) {
      out.push({ t: " ", text: A[i]! });
      i++;
      j++;
    } else if (j < m && (i === n || dp[i]![j + 1]! >= dp[i + 1]![j]!)) {
      out.push({ t: "+", text: B[j]! });
      j++;
    } else {
      out.push({ t: "-", text: A[i]! });
      i++;
    }
  }
  for (const text of al.slice(ha)) out.push({ t: " ", text });
  return out;
}

/** Keeps `context` unchanged lines around each change; folds the rest into "@" markers. */
export function withContext(lines: DiffLine[], context = 3): DiffLine[] {
  const keep = new Array<boolean>(lines.length).fill(false);
  lines.forEach((l, i) => {
    if (l.t === " ") return;
    for (let k = Math.max(0, i - context); k <= Math.min(lines.length - 1, i + context); k++) keep[k] = true;
  });
  const out: DiffLine[] = [];
  let skipped = 0;
  lines.forEach((l, i) => {
    if (keep[i]) {
      if (skipped) out.push({ t: "@", text: `${skipped} unchanged line${skipped === 1 ? "" : "s"}` });
      skipped = 0;
      out.push(l);
    } else skipped++;
  });
  if (skipped && out.length) out.push({ t: "@", text: `${skipped} unchanged line${skipped === 1 ? "" : "s"}` });
  return out;
}

/** Parses the body of a unified diff (as sent by GET /drafts/{id}/diff). */
export function parseUnified(text: string): DiffLine[] {
  const out: DiffLine[] = [];
  for (const line of text.split("\n")) {
    if (line.startsWith("--- ") || line.startsWith("+++ ")) continue;
    if (line === "") continue;
    if (line.startsWith("@@")) out.push({ t: "@", text: line });
    else if (line[0] === "+" || line[0] === "-" || line[0] === " ") out.push({ t: line[0] as DiffLine["t"], text: line.slice(1) });
    else out.push({ t: " ", text: line });
  }
  return out;
}

export function stats(lines: DiffLine[]): { added: number; removed: number } {
  let added = 0;
  let removed = 0;
  for (const l of lines) {
    if (l.t === "+") added++;
    else if (l.t === "-") removed++;
  }
  return { added, removed };
}
