/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const api = process.env.STUDIO_API ?? "http://127.0.0.1:8787";
// When the API is the real starter (Studio mounted under /studio), set STUDIO_PREFIX=/studio:
// /api and /preview are forwarded under that prefix, and /studio/... (the preview's own links) as is.
const prefix = process.env.STUDIO_PREFIX ?? "";
const forward = (target: string) => (prefix ? { target, rewrite: (p: string) => prefix + p } : target);

// base "./" keeps every asset URL relative, so the build works when a host
// mounts it under any prefix (the starter mounts it at /studio/).
export default defineConfig({
  base: "./",
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      output: {
        // Vendor code changes far less often than the app, so it lives in long-lived chunks. The canvas
        // libraries are left to the bundler: they are only reachable from the lazy canvas route.
        manualChunks(id: string) {
          if (!id.includes("node_modules")) return undefined;
          if (/[\\/]node_modules[\\/](react|react-dom|react-router|react-router-dom|scheduler|@remix-run|zustand)[\\/]/.test(id)) return "react";
          if (/[\\/]node_modules[\\/](motion|framer-motion|motion-dom|motion-utils)[\\/]/.test(id)) return "motion";
          if (/[\\/]node_modules[\\/]lucide-react[\\/]/.test(id)) return "icons";
          return undefined;
        },
      },
    },
  },
  server: { proxy: { "/api": forward(api), "/preview": forward(api), ...(prefix ? { [prefix]: api } : {}) } },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: false,
  },
});
