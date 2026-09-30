// The wire types of GET /drafts/{id}/flows (docs/studio-api.md, "Journeys").

export type FlowNodeKind = "page" | "element" | "route" | "intent" | "resource" | "external" | "unresolved";
export type FlowEdgeKind = "contains" | "navigates" | "calls" | "runs" | "renders" | "redirects" | "uses";

export interface FlowNode {
  id: string;
  kind: FlowNodeKind;
  subkind?: string;
  label: string;
  group?: string;
  shared?: boolean;
  file?: string;
  line?: number;
  path?: string;
  data?: Record<string, unknown>;
}

export interface FlowEdge {
  id: string;
  kind: FlowEdgeKind;
  from: string;
  to: string;
  label?: string;
  shared?: boolean;
  data?: Record<string, unknown>;
}

export interface FlowWarning {
  code: string;
  severity: "warning" | "info";
  message: string;
  node?: string;
  file?: string;
  line?: number;
}

export interface FlowGraph {
  version: number;
  focus?: string;
  depth?: number;
  nodes: FlowNode[];
  edges: FlowEdge[];
  stats: {
    nodes: Record<string, number>;
    edges: Record<string, number>;
    pages: number;
    routes: number;
    elements: number;
    sharedElements: number;
    unresolved: number;
    unusedPages: number;
  };
  warnings: FlowWarning[];
}
