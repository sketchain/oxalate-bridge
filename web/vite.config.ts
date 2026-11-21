import { writeFileSync } from "node:fs";
import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";

const backend = process.env.BRIDGE_DEV_BACKEND ?? "http://127.0.0.1:8080";

export default defineConfig({
  plugins: [
    tanstackRouter({ target: "react", autoCodeSplitting: true, quoteStyle: "double" }),
    react(),
    tailwindcss(),
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
