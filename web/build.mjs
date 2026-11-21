// Placeholder build: copies index.html into the directory the Go binary
// embeds (backend/internal/webui/dist). The real frontend build replaces this.
import { mkdirSync, copyFileSync, rmSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const out = join(here, "..", "backend", "internal", "webui", "dist");
rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });
copyFileSync(join(here, "index.html"), join(out, "index.html"));
console.log("web build written to", out);
