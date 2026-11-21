// Checks the locale files: zh-CN and en must have exactly the same keys, and
// every key the source uses (t("…"), label: "…" in the nav, template keys
// like t(`tier.${x}`) by prefix) must exist. Exits 1 on any problem.
// `node scripts/check-i18n.mjs --list` prints the used keys instead.
import { readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const load = (lng) => JSON.parse(readFileSync(join(root, "src/i18n/locales", `${lng}.json`), "utf8"));

function flatten(obj, prefix = "", out = new Map()) {
  for (const [k, v] of Object.entries(obj)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (v && typeof v === "object") flatten(v, key, out);
    else out.set(key, v);
  }
  return out;
}

function walk(dir, files = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, files);
    else if (/\.(tsx?|mjs)$/.test(name) && !name.endsWith(".gen.ts") && !name.endsWith(".d.ts") && !name.includes(".test.")) files.push(p);
  }
  return files;
}

const used = new Set();
const prefixes = new Set();
for (const file of walk(join(root, "src"))) {
  const src = readFileSync(file, "utf8");
  for (const m of src.matchAll(/\bt\(\s*"([a-zA-Z0-9_.]+)"/g)) used.add(m[1]);
  for (const m of src.matchAll(/\blabel:\s*"((?:nav)\.[a-zA-Z0-9_]+)"/g)) used.add(m[1]);
  for (const m of src.matchAll(/\bt\(\s*`([a-zA-Z0-9_.]+)\.\$\{/g)) prefixes.add(m[1]);
}

if (process.argv.includes("--list")) {
  console.log([...used].sort().join("\n"));
  console.log("-- prefixes --\n" + [...prefixes].sort().join("\n"));
  process.exit(0);
}

const zh = flatten(load("zh-CN"));
const en = flatten(load("en"));
const problems = [];
for (const k of zh.keys()) if (!en.has(k)) problems.push(`en is missing ${k}`);
for (const k of en.keys()) if (!zh.has(k)) problems.push(`zh-CN is missing ${k}`);
for (const [lng, map] of [["zh-CN", zh], ["en", en]]) {
  for (const [k, v] of map) if (typeof v !== "string" || v.trim() === "") problems.push(`${lng}: ${k} is empty`);
  for (const k of used) if (!map.has(k)) problems.push(`${lng}: used key ${k} is not defined`);
  for (const p of prefixes) if (![...map.keys()].some((k) => k.startsWith(`${p}.`))) problems.push(`${lng}: no keys under ${p}.*`);
}
if (problems.length) {
  console.error(problems.join("\n"));
  console.error(`\n${problems.length} i18n problem(s)`);
  process.exit(1);
}
console.log(`i18n ok: ${zh.size} keys in zh-CN and en, ${used.size} used directly, ${prefixes.size} dynamic prefixes`);
