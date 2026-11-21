import { readFileSync } from "node:fs";
import { join } from "node:path";
import i18next from "i18next";
import { describe, expect, it } from "vitest";
import zh from "@/i18n/locales/zh-CN.json";
import en from "@/i18n/locales/en.json";
import { reasonLabel } from "./accounts";

const spec = readFileSync(join(process.cwd(), "..", "api", "openapi.yaml"), "utf8");

/** The codes listed under "Known codes:" of status_reason_code in openapi.yaml. */
function documentedReasonCodes(): string[] {
  const m = spec.match(/status_reason_code:[\s\S]*?Known codes:([\s\S]*?)Clients must cope/);
  if (!m) throw new Error("status_reason_code codes not found in openapi.yaml");
  return [...m[1]!.matchAll(/`([a-z0-9_]+)`/g)].map((x) => x[1]!);
}

describe("account reason codes", () => {
  it("documents a code list", () => {
    expect(documentedReasonCodes()).toContain("5h_exhausted");
  });
  for (const [lng, res] of [
    ["zh-CN", zh],
    ["en", en],
  ] as const) {
    it(`every documented code is translated (${lng})`, async () => {
      const t = await i18next.createInstance().init({ lng, resources: { [lng]: { translation: res } } });
      for (const code of documentedReasonCodes()) {
        const r = reasonLabel(t, code);
        expect(r.unknown, code).toBe(false);
        expect(r.text).not.toBe(code);
      }
    });
  }
  it("unknown codes read 其他原因 and are flagged", async () => {
    const t = await i18next.createInstance().init({ lng: "zh-CN", resources: { "zh-CN": { translation: zh } } });
    expect(reasonLabel(t, "brand_new_reason")).toEqual({ text: "其他原因", unknown: true });
    expect(reasonLabel(t, "")).toEqual({ text: "", unknown: false });
  });
});
