import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath, URL } from "node:url";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";

const backend = process.env.BRIDGE_DEV_BACKEND ?? "http://127.0.0.1:8080";

/** App shell files precached next to the hashed build output (assets/). */
const SHELL = ["/", "/offline.html", "/manifest.webmanifest", "/favicon.svg", "/icons/icon-192.png", "/icons/badge-96.png"];

/**
 * Emits sw.js from sw/sw.js with the precache list (every hashed file under
 * assets/ plus SHELL) and a version derived from the build output, so any
 * change to the build gives the service worker new bytes and the browser
 * installs it as an update.
 */
function serviceWorker(): Plugin {
  const template = readFileSync(fileURLToPath(new URL("./sw/sw.js", import.meta.url)), "utf8");
  return {
    name: "ob-service-worker",
    apply: "build",
    enforce: "post",
    generateBundle(_options, bundle) {
      const hashed = Object.keys(bundle).filter((f) => f.startsWith("assets/") && !f.endsWith(".map"));
      const precache = [...SHELL, ...hashed.map((f) => `/${f}`).sort()];
      const hash = createHash("sha256").update(template).update(JSON.stringify(precache));
      const html = bundle["index.html"];
      if (html && html.type === "asset") hash.update(typeof html.source === "string" ? html.source : Buffer.from(html.source));
      const version = hash.digest("hex").slice(0, 12);
      const source = template.replace("self.__OB_PRECACHE__", JSON.stringify(precache)).replace("__OB_VERSION__", version);
      this.emitFile({ type: "asset", fileName: "sw.js", source });
    },
  };
}

export default defineConfig({
  plugins: [
    tanstackRouter({ target: "react", autoCodeSplitting: true, quoteStyle: "double" }),
    react(),
    tailwindcss(),
    serviceWorker(),
    {
      // emptyOutDir removes the committed dist/.gitkeep; put it back.
      name: "keep-dist-gitkeep",
      apply: "build",
      closeBundle() {
        writeFileSync(fileURLToPath(new URL("../backend/internal/webui/dist/.gitkeep", import.meta.url)), "");
      },
    },
  ],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": { target: backend, changeOrigin: false },
      "/v1": { target: backend, changeOrigin: false },
    },
  },
  build: {
    outDir: "../backend/internal/webui/dist",
    emptyOutDir: true,
    assetsDir: "assets",
    chunkSizeWarningLimit: 900,
  },
  test: {
    environment: "node",
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
