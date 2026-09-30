// URL handling for the preview's address bar. The preview app is served under
// a prefix ("/studio/preview/<draft>/"); the bar only ever shows and accepts
// paths of the previewed app, and never lets the frame leave that prefix.

export interface Confined {
  /** Absolute URL to load in the frame (origin + prefix + app path). */
  href: string;
  /** The app-relative path, always starting with "/", including query and hash. */
  appPath: string;
}

function normalizePrefix(prefix: string): string {
  return prefix.endsWith("/") ? prefix : prefix + "/";
}

/**
 * Resolves what the user typed against the preview prefix. `prefixUrl` is the
 * preview root (an absolute URL, or a path resolved against `origin`).
 * Returns null when the input would leave the prefix: another origin, a
 * non-http scheme, or a ".." that climbs out.
 *
 *   "/todos"                       -> <prefix>todos
 *   "todos?x=1#top"                -> <prefix>todos?x=1#top
 *   "<origin><prefix>todos"        -> as is
 *   "<prefix>todos" (a full path)  -> as is (not doubled)
 *   "https://evil.example/x"       -> null
 *   "/../../api/v1/meta"           -> null
 */
export function confinePreviewUrl(input: string, prefixUrl: string, origin = safeOrigin()): Confined | null {
  const text = input.trim();
  let root: URL;
  try {
    root = new URL(prefixUrl, origin || "http://localhost");
  } catch {
    return null;
  }
  const prefix = normalizePrefix(root.pathname);
  if (root.origin === "null") return null;

  let target: URL;
  try {
    if (/^[a-z][a-z0-9+.-]*:/i.test(text)) {
      // Absolute: only http(s) on the same origin.
      target = new URL(text);
      if ((target.protocol !== "http:" && target.protocol !== "https:") || target.origin !== root.origin) return null;
    } else if (text === "" || text === "/") {
      target = new URL(prefix, root.origin);
    } else if (text.startsWith("//")) {
      return null; // protocol-relative: could name any host
    } else if (text.startsWith("#") || text.startsWith("?")) {
      target = new URL(text, new URL(prefix, root.origin));
    } else if (text.startsWith("/")) {
      // A path. Already inside the prefix? Otherwise it is an app path.
      const inside = text === prefix.slice(0, -1) || text.startsWith(prefix);
      target = new URL(inside ? text : prefix + text.slice(1), root.origin);
    } else {
      target = new URL(text, new URL(prefix, root.origin));
    }
  } catch {
    return null;
  }

  // ".." resolution happened in URL(); check the result stayed inside.
  const path = target.pathname;
  if (path !== prefix.slice(0, -1) && !path.startsWith(prefix)) return null;
  // Encoded traversal ("%2e%2e") is decoded by servers, not by URL(): refuse it.
  if (/%2e|%2f|%5c/i.test(path.slice(prefix.length))) return null;

  const full = path === prefix.slice(0, -1) ? prefix : path;
  const rest = full.slice(prefix.length);
  return { href: root.origin + full + target.search + target.hash, appPath: "/" + rest + target.search + target.hash };
}

/** The app-relative path of a frame location, or null when it is outside the prefix. */
export function appPathOf(location: string, prefixUrl: string, origin = safeOrigin()): string | null {
  const c = confinePreviewUrl(location, prefixUrl, origin);
  return c ? c.appPath : null;
}

function safeOrigin(): string {
  try {
    return window.location.origin;
  } catch {
    return "http://localhost";
  }
}
