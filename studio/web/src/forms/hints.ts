// Presentation hints for identifier-valued fields whose allowed values are
// documented in the spec comments but not in the generated schema.
export const IDENT_OPTIONS: Record<string, string[]> = {
  method: ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"],
  mode: ["sync", "async", "stream", "download"],
};
