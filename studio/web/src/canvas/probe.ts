// A dev/test-only render counter. Performance regressions on the canvas show up as
// "selecting one step re-rendered every step", which a test can assert without a
// browser. In a production build the probe is a no-op.
const counts: Record<string, number> = {};

export const probe: (kind: string) => void = import.meta.env.PROD
  ? () => {}
  : (kind) => {
      counts[kind] = (counts[kind] ?? 0) + 1;
    };

export const probeCounts = (): Record<string, number> => ({ ...counts });
export const probeReset = (): void => {
  for (const k of Object.keys(counts)) delete counts[k];
};
