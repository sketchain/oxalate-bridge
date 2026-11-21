import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { ApiError, ERROR_CODES } from "@/api/client";
import { errorKey, errorText } from "./errors";
import zh from "@/i18n/locales/zh-CN.json";
import en from "@/i18n/locales/en.json";

const spec = readFileSync(join(process.cwd(), "..", "api", "openapi.yaml"), "utf8");

/** The codes listed under "Fixed codes:" in openapi.yaml. */
function documentedCodes(): string[] {
  const m = spec.match(/Fixed codes:([\s\S]*?)\n\s*message:/);
  if (!m) throw new Error("Fixed codes list not found in openapi.yaml");
  return m[1]!
    .split(/[\s,.]+/)
    .map((s) => s.trim())
    .filter((s) => /^[a-z_]+$/.test(s));
}

describe("error code mapping", () => {
  it("knows every code openapi.yaml documents", () => {
    expect([...ERROR_CODES].sort()).toEqual(documentedCodes().sort());
  });
  it("has zh-CN and en text for every code plus network and unknown", () => {
    for (const code of [...ERROR_CODES, "network", "unknown"]) {
      expect((zh.errors as Record<string, string>)[code], `zh ${code}`).toBeTruthy();
      expect((en.errors as Record<string, string>)[code], `en ${code}`).toBeTruthy();
    }
  });
  it("maps unknown codes to the generic text and never shows the raw message", () => {
    expect(errorKey("insufficient_balance")).toBe("errors.insufficient_balance");
    expect(errorKey("something_new")).toBe("errors.unknown");
    const t = ((k: string) => `T(${k})`) as never;
    expect(errorText(t, new ApiError(402, "insufficient_balance", "余额不足 raw"))).toBe("T(errors.insufficient_balance)");
    expect(errorText(t, new ApiError(500, "weird", "stack trace"))).toBe("T(errors.unknown)");
    expect(errorText(t, new Error("boom"))).toBe("T(errors.unknown)");
  });
});
