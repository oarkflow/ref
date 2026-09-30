import type { ReactElement } from "react";
import { Icon as CanvasIcon } from "../canvas/icons";

// Glyphs the canvas set does not have, in the same style (16px grid, 1.4 stroke).
const EXTRA: Record<string, ReactElement> = {
  link: <path d="M6.8 9.2a2.6 2.6 0 0 0 3.7 0l2.2-2.2a2.6 2.6 0 0 0-3.7-3.7l-.8.8M9.2 6.8a2.6 2.6 0 0 0-3.7 0L3.3 9a2.6 2.6 0 0 0 3.7 3.7l.8-.8" />,
  cursor: <path d="M4 2.5 12.5 7l-3.8 1.2L7.4 12z" />,
  form: (<><rect x="2.5" y="2.5" width="11" height="11" rx="1.5" /><path d="M5 6h6M5 8.5h6M5 11h3" /></>),
  page: (<><rect x="2" y="2.5" width="12" height="11" rx="1.5" /><path d="M2 5.5h12M4.5 4h.01M6.5 4h.01" /></>),
  globe: (<><circle cx="8" cy="8" r="5.5" /><path d="M2.5 8h11M8 2.5c2 2 2 9 0 11M8 2.5c-2 2-2 9 0 11" /></>),
  external: <path d="M9 3h4v4M13 3 7.5 8.5M11.5 9.5V12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V5.5a1 1 0 0 1 1-1h2.5" />,
  server: (<><rect x="2.5" y="3" width="11" height="4" rx="1.2" /><rect x="2.5" y="9" width="11" height="4" rx="1.2" /><path d="M5 5h.01M5 11h.01" /></>),
  layers: <path d="m8 2.5 6 3-6 3-6-3zM2 8.5l6 3 6-3M2 11.3l6 3 6-3" />,
  search: (<><circle cx="7" cy="7" r="4.5" /><path d="m10.5 10.5 3 3" /></>),
  filter: <path d="M2.5 3.5h11L9.5 8.5v4l-3 1v-5z" />,
  arrow: <path d="M3 8h10M9.5 4.5 13 8l-3.5 3.5" />,
  route: (<><circle cx="4" cy="4" r="1.6" /><circle cx="12" cy="12" r="1.6" /><path d="M5.6 4H9a2.5 2.5 0 0 1 0 5H7a2.5 2.5 0 0 0 0 5" /></>),
  footsteps: <path d="M5 3.5c1.2 0 2 1 2 2.6S6 9 5 9 3 7.700 3 6.100 3.800 3.500 5 3.500zM4 11h2.200M11 7c1.200 0 2 1 2 2.600S12 12.500 11 12.500 9 11.200 9 9.600 9.800 7 11 7zM10 14.500h2.200" />,
  gear: <CanvasIcon name="gear" />,
};

export function JIcon({ name, size = 14 }: { name: string; size?: number }) {
  const body = EXTRA[name];
  if (!body || name === "gear") return <CanvasIcon name={name} size={size} />;
  return (
    <svg className="cv-icon" width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      {body}
    </svg>
  );
}

import type { ElementKind } from "./text";
import type { JKind } from "./model";

export const ELEMENT_ICON: Record<ElementKind, string> = { link: "link", form: "form", button: "cursor", fetch: "code" };

export const KIND_ICON: Record<JKind, string> = {
  page: "page", shared: "layers", route: "server", intent: "branch", resource: "database", external: "external", unresolved: "alert",
};

const RESOURCE_ICON: Record<string, string> = {
  database: "database", queue: "list", cache: "bolt", http: "plug", email: "mail", smtp: "mail", session: "user",
  rules: "shield", circuit_breaker: "gauge", files: "doc", storage: "doc", auth: "lock", authz: "lock",
};
export const resourceIcon = (category: string) => RESOURCE_ICON[category] ?? "plug";
