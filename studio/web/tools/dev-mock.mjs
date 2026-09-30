// Starts the Go mock studio server (real studio/model + platform + deploy,
// in-memory drafts) and the Vite dev server that proxies /api to it.
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const web = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repo = resolve(web, "../..");
const addr = process.env.MOCK_ADDR ?? "127.0.0.1:8787";

const mock = spawn("go", ["run", "./studio/web/tools/mock", "-addr", addr], { cwd: repo, stdio: "inherit" });
const vite = spawn("npx", ["vite", ...process.argv.slice(2)], {
  cwd: web,
  stdio: "inherit",
  env: { ...process.env, STUDIO_API: `http://${addr}` },
});

const stop = () => { mock.kill(); vite.kill(); };
process.on("SIGINT", stop);
process.on("SIGTERM", stop);
mock.on("exit", (c) => { if (c) { console.error(`mock exited with ${c}`); vite.kill(); } });
vite.on("exit", () => { mock.kill(); process.exit(0); });
console.log(`\nSign in with a token: editor-token-000001 | reviewer-token-0001 | admin-token-0000001 | viewer-token-00000001\n`);
