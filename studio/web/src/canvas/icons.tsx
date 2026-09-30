import type { ReactElement } from "react";

// Small stroke icons (16px grid). Inline so the canvas has no asset pipeline.
const P: Record<string, ReactElement> = {
  play: <path d="M5 3.5v9l7-4.5z" />,
  stop: <rect x="4" y="4" width="8" height="8" rx="1.5" />,
  clock: (<><circle cx="8" cy="8" r="5.5" /><path d="M8 5v3.2l2 1.3" /></>),
  user: (<><circle cx="8" cy="5.5" r="2.5" /><path d="M3 13c.5-2.6 2.6-4 5-4s4.5 1.4 5 4" /></>),
  code: <path d="M5.5 5 2.5 8l3 3M10.5 5l3 3-3 3M9 3.5l-2 9" />,
  plug: (<><path d="M6 2.5v3M10 2.5v3M4.5 5.5h7v2.2a3.5 3.5 0 0 1-7 0z" /><path d="M8 11.2V14" /></>),
  loop: (<><path d="M3 8a4 4 0 0 1 7-2.6L12 7" /><path d="M12 3.5V7H8.5" /><path d="M13 8a4 4 0 0 1-7 2.6L4 9" /><path d="M4 12.5V9h3.5" /></>),
  branch: (<><circle cx="4" cy="3.5" r="1.5" /><circle cx="4" cy="12.5" r="1.5" /><circle cx="12" cy="6" r="1.5" /><path d="M4 5v6M4 9c0-2 2-3 6.5-3" /></>),
  diamond: <path d="M8 2 14 8l-6 6L2 8z" />,
  database: (<><ellipse cx="8" cy="4" rx="4.5" ry="1.8" /><path d="M3.5 4v8c0 1 2 1.8 4.5 1.8s4.5-.8 4.5-1.8V4M3.5 8c0 1 2 1.8 4.5 1.8S12.5 9 12.5 8" /></>),
  mail: (<><rect x="2" y="3.5" width="12" height="9" rx="1.5" /><path d="m2.5 4.5 5.5 4.2 5.5-4.2" /></>),
  spark: <path d="M8 2v3.5M8 10.5V14M2 8h3.5M10.5 8H14M4 4l2.2 2.2M9.8 9.8 12 12M12 4 9.8 6.2M6.2 9.8 4 12" />,
  shield: <path d="M8 2 13 4v4c0 3-2.2 5-5 6-2.8-1-5-3-5-6V4z" />,
  gear: (<><circle cx="8" cy="8" r="2" /><path d="M8 2v1.6M8 12.4V14M2 8h1.6M12.4 8H14M3.8 3.8l1.1 1.1M11.1 11.1l1.1 1.1M12.2 3.8l-1.1 1.1M4.9 11.1l-1.1 1.1" /></>),
  flag: <path d="M4 14V2.5M4 3h7.5l-1.5 2.7 1.5 2.7H4" />,
  doc: (<><path d="M4 2h5l3 3v9H4z" /><path d="M9 2v3h3M6 8.5h4M6 11h4" /></>),
  bell: <path d="M4 11V7.5a4 4 0 0 1 8 0V11l1 1.5H3zM6.8 14a1.4 1.4 0 0 0 2.4 0" />,
  eye: (<><path d="M1.5 8S4 3.5 8 3.5 14.5 8 14.5 8 12 12.5 8 12.5 1.5 8 1.5 8z" /><circle cx="8" cy="8" r="2" /></>),
  lock: (<><rect x="3.5" y="7" width="9" height="6.5" rx="1.5" /><path d="M5.5 7V5a2.5 2.5 0 0 1 5 0v2" /></>),
  gauge: (<><path d="M2.5 11a5.5 5.5 0 1 1 11 0" /><path d="m8 11 2.5-3.5" /></>),
  send: <path d="M14 2 2 7l4.5 2L8 14l2-4.5z M6.5 9 14 2" />,
  wrench: <path d="M10.5 2.5a3 3 0 0 0-2.8 4L2.5 11.7 4.3 13.5 9.5 8.3a3 3 0 0 0 4-2.8L11.5 7.5 9.5 6.5l1-2z" />,
  list: <path d="M5.5 4h8M5.5 8h8M5.5 12h8M2.5 4h.01M2.5 8h.01M2.5 12h.01" />,
  bolt: <path d="M9 2 3.5 9H8l-1 5 5.5-7H8z" />,
  check: <path d="m3 8.5 3.2 3.2L13 4.8" />,
  alert: (<><path d="M8 2.5 14 13H2z" /><path d="M8 6.5v3M8 11.3h.01" /></>),
  plus: <path d="M8 3v10M3 8h10" />,
  x: <path d="M4 4l8 8M12 4l-8 8" />,
  chevron: <path d="m4.5 6 3.5 3.5L11.5 6" />,
  fit: <path d="M2.5 6V3.5H5M11 3.5h2.5V6M13.5 10v2.5H11M5 12.5H2.5V10" />,
  layout: (<><rect x="2" y="2.5" width="4" height="3.5" rx="1" /><rect x="10" y="2.5" width="4" height="3.5" rx="1" /><rect x="6" y="10" width="4" height="3.5" rx="1" /><path d="M4 6v1.5h8V6M8 7.5V10" /></>),
  undo: <path d="M6 4.5 3 7.5l3 3M3.5 7.5H10a3.5 3.5 0 0 1 0 7H8" />,
  redo: <path d="m10 4.5 3 3-3 3M12.5 7.5H6a3.5 3.5 0 0 0 0 7h2" />,
  map: <path d="M2 4l4-1.5 4 1.5 4-1.5v9.5l-4 1.5-4-1.5-4 1.5zM6 2.5v9.5M10 4v9.5" />,
  zoomin: (<><circle cx="7" cy="7" r="4.5" /><path d="m10.5 10.5 3 3M7 5v4M5 7h4" /></>),
  zoomout: (<><circle cx="7" cy="7" r="4.5" /><path d="m10.5 10.5 3 3M5 7h4" /></>),
  trace: <path d="M2.5 8h3l2-4 2 8 2-4h2" />,
  panel: (<><rect x="2" y="3" width="12" height="10" rx="1.5" /><path d="M10 3v10" /></>),
  palette: (<><rect x="2" y="3" width="12" height="10" rx="1.5" /><path d="M6 3v10" /></>),
  trash: <path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5" />,
  ban: (<><circle cx="8" cy="8" r="5.5" /><path d="m4.2 4.2 7.6 7.6" /></>),
  skip: <path d="M3 4.5 8 8l-5 3.5zM9 4.5 14 8l-5 3.5z" />,
  info: (<><circle cx="8" cy="8" r="5.5" /><path d="M8 7.5v3.2M8 5.2h.01" /></>),
  person: (<><circle cx="8" cy="5.5" r="2.5" /><path d="M3 13c.5-2.6 2.6-4 5-4s4.5 1.4 5 4" /></>),
};

export type IconName = keyof typeof P;

export function Icon({ name, size = 14, title }: { name: string; size?: number; title?: string }) {
  const body = P[name] ?? P.gear!;
  return (
    <svg
      className="cv-icon"
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
    >
      {body}
    </svg>
  );
}
