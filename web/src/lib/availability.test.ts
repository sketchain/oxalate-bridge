import { describe, expect, it } from "vitest";
import type { Schemas } from "@/api/client";
import { cacheHitRate, planRows, poolCells, spendSplit } from "./availability";

const h = (horizon: Schemas["AvailabilityHorizon"]["horizon"], pool: number, available: number, limited: boolean): Schemas["AvailabilityHorizon"] => ({
  horizon,
  pool_micro: pool,
  available_micro: available,
  limited_by_balance: limited,
});

describe("poolCells", () => {
  it("orders 1h/5h/1d/7d and labels 24h as 1d", () => {
    const cells = poolCells([h("7d", 9, 9, false), h("1h", 1, 1, false), h("24h", 5, 3, true), h("5h", 2, 2, false)]);
    expect(cells.map((c) => c.label)).toEqual(["1h", "5h", "1d", "7d"]);
    expect(cells[2]).toMatchObject({ available: 3, limitedByBalance: true });
  });
  it("never shows negative amounts and spots unlimited pools", () => {
    const cells = poolCells([h("1h", 2 ** 63, -5, true)]);
    expect(cells[0]).toMatchObject({ available: 0, unlimitedPool: true });
    // Missing horizons render as 0.
    expect(cells[3]).toMatchObject({ available: 0, limitedByBalance: false });
  });
});

describe("planRows", () => {
  it("maps pending, normal and unavailable plans", () => {
    const rows = planRows([
      { subscription_id: "a", plan_name: "P", status: "pending_assignment", account_status: null },
      {
        subscription_id: "b",
        plan_name: "Q",
        status: "active",
        account_status: "normal",
        five_hour: { available_micro: 5, reset_at: "2026-11-21T05:00:00Z", ends_at: null },
        period: { available_micro: 9, reset_at: null, ends_at: "2026-11-25T00:00:00Z" },
      },
      {
        subscription_id: "c",
        plan_name: "R",
        status: "active",
        account_status: "unavailable",
        five_hour: { available_micro: 0, reset_at: null, ends_at: null },
        period: { available_micro: 0, reset_at: null, ends_at: "2026-11-25T00:00:00Z" },
      },
    ]);
    expect(rows[0]).toMatchObject({ pending: true, accountStatus: null, five: null });
    expect(rows[1]).toMatchObject({ pending: false, accountStatus: "normal", five: { available: 5, unlimited: false }, period: { available: 9 } });
    expect(rows[2]).toMatchObject({ accountStatus: "unavailable", five: { available: 0, unlimited: false } });
  });
  it("treats a plan without a 5h reset as having no 5h limit", () => {
    const [row] = planRows([
      { subscription_id: "d", plan_name: "S", status: "active", account_status: "stale", five_hour: { available_micro: 7, reset_at: null, ends_at: null }, period: null },
    ]);
    expect(row!.five).toMatchObject({ unlimited: true, available: 7 });
  });
});

describe("today figures", () => {
  it("computes the cache hit rate over all buckets", () => {
    expect(cacheHitRate([])).toBeNull();
    expect(cacheHitRate([{ input_tokens: 100, cache_read_tokens: 300, cache_write_tokens: 100 }])).toBeCloseTo(0.6);
    expect(cacheHitRate([{ input_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 }])).toBeNull();
  });
  it("splits spend into plan and balance tiers", () => {
    expect(
      spendSplit([
        { tier: "plan", billed_micro: 10 },
        { tier: "standard", billed_micro: 3 },
        { tier: "force", billed_micro: 2 },
        { tier: "", billed_micro: 100 },
      ]),
    ).toEqual({ plan: 10, balance: 5, total: 15 });
  });
});
