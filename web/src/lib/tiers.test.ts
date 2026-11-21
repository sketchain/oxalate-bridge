import { describe, expect, it } from "vitest";
import { moveTier, reorder, rowsFromTiers, tiersFromRows, toggleTier, validateTiers } from "./tiers";

describe("tier chain editing", () => {
  it("puts enabled tiers first in their order, then the rest", () => {
    const rows = rowsFromTiers(["force", "standard"]);
    expect(rows).toEqual([
      { tier: "force", enabled: true },
      { tier: "standard", enabled: true },
      { tier: "plan", enabled: false },
    ]);
    expect(tiersFromRows(rows)).toEqual(["force", "standard"]);
  });
  it("toggles a tier in place", () => {
    const rows = toggleTier(rowsFromTiers(["standard"]), "plan");
    expect(tiersFromRows(rows)).toEqual(["standard", "plan"]);
  });
  it("moves up and down, staying put at the ends", () => {
    let rows = rowsFromTiers(["plan", "standard", "force"]);
    rows = moveTier(rows, "force", -1);
    expect(tiersFromRows(rows)).toEqual(["plan", "force", "standard"]);
    expect(tiersFromRows(moveTier(rows, "plan", -1))).toEqual(["plan", "force", "standard"]);
    expect(tiersFromRows(moveTier(rows, "standard", 1))).toEqual(["plan", "force", "standard"]);
  });
  it("reorders by drag indexes and ignores bad ones", () => {
    const rows = rowsFromTiers(["plan", "standard", "force"]);
    expect(tiersFromRows(reorder(rows, 0, 2))).toEqual(["standard", "force", "plan"]);
    expect(reorder(rows, 0, 9)).toEqual(rows);
  });
  it("validates the chain", () => {
    expect(validateTiers(rowsFromTiers([]), null)).toBe("none_enabled");
    expect(validateTiers(rowsFromTiers(["plan", "standard"]), "")).toBe("plan_needs_subscription");
    expect(validateTiers(rowsFromTiers(["plan"]), "sub-1")).toBeNull();
    expect(validateTiers(rowsFromTiers(["standard"]), null)).toBeNull();
  });
});
