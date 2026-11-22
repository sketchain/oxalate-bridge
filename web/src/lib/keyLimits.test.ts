import { describe, expect, it } from "vitest";
import { draftFromKey, draftToBody, looksLikeIP, splitList, usedPercent } from "./keyLimits";

describe("key limit drafts", () => {
  it("turns empty fields into unlimited", () => {
    const { body, errors } = draftToBody(draftFromKey(null));
    expect(errors).toEqual({});
    expect(body).toMatchObject({ quota_limit_micro: null, quota_scope: "all", limit_5h_micro: null, model_allowlist: null, ip_allowlist: null, allow_owner_query: false });
  });
  it("parses dollars, lists and the scope", () => {
    const d = { ...draftFromKey(null), quota: "$12.5", scope: "balance" as const, l1d: "3", models: "claude-opus-4-8, claude-sonnet-4.6\nclaude-opus-4-8", ips: "10.0.0.0/8\n127.0.0.1" };
    const { body, errors } = draftToBody(d);
    expect(errors).toEqual({});
    expect(body.quota_limit_micro).toBe(12_500_000);
    expect(body.limit_1d_micro).toBe(3_000_000);
    expect(body.quota_scope).toBe("balance");
    expect(body.model_allowlist).toEqual(["claude-opus-4-8", "claude-sonnet-4.6"]);
    expect(body.ip_allowlist).toEqual(["10.0.0.0/8", "127.0.0.1"]);
  });
  it("reports bad amounts and IPs", () => {
    const { errors } = draftToBody({ ...draftFromKey(null), quota: "-1", l7d: "abc", ips: "300.1.1.1" });
    expect(errors).toEqual({ quota: "validation.nonNegativeAmount", l7d: "validation.nonNegativeAmount", ips: "validation.ip" });
  });
  it("round-trips a key", () => {
    const d = draftFromKey({ quota_limit_micro: 5_000_000, quota_scope: "balance", limit_5h_micro: 1_000_000, model_allowlist: ["a"], ip_allowlist: ["1.2.3.4/32"], allow_owner_query: true } as never);
    expect(d).toMatchObject({ quota: "5", scope: "balance", l5h: "1", models: "a", ips: "1.2.3.4/32", ownerQuery: true });
  });
  it("checks IPs and splits lists", () => {
    expect(looksLikeIP("::1")).toBe(true);
    expect(looksLikeIP("2001:db8::/32")).toBe(true);
    expect(looksLikeIP("10.0.0.0/33")).toBe(false);
    expect(looksLikeIP("example.com")).toBe(false);
    expect(splitList(" a ,b\n\na ")).toEqual(["a", "b"]);
    expect(usedPercent(5, 10)).toBe(50);
    expect(usedPercent(5, null)).toBeNull();
  });
});
