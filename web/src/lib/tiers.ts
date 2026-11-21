import type { Tier } from "@/api/client";

export const TIERS: Tier[] = ["plan", "standard", "force"];

/** One row of the tier chain editor. */
export interface TierRow {
  tier: Tier;
  enabled: boolean;
}

/** Editor rows from a key's ordered tiers: enabled ones first, in order. */
export function rowsFromTiers(tiers: readonly Tier[]): TierRow[] {
  const rows: TierRow[] = tiers.map((tier) => ({ tier, enabled: true }));
  for (const tier of TIERS) if (!tiers.includes(tier)) rows.push({ tier, enabled: false });
  return rows;
}

/** The ordered tiers a set of rows stands for. */
export function tiersFromRows(rows: readonly TierRow[]): Tier[] {
  return rows.filter((r) => r.enabled).map((r) => r.tier);
}

export function toggleTier(rows: readonly TierRow[], tier: Tier): TierRow[] {
  return rows.map((r) => (r.tier === tier ? { ...r, enabled: !r.enabled } : r));
}

/** Moves a row from one index to another (drag and drop). */
export function reorder(rows: readonly TierRow[], from: number, to: number): TierRow[] {
  if (from === to || from < 0 || to < 0 || from >= rows.length || to >= rows.length) return [...rows];
  const out = [...rows];
  const [moved] = out.splice(from, 1);
  out.splice(to, 0, moved!);
  return out;
}

/** Moves a row one place up (-1) or down (+1); no-op at the ends. */
export function moveTier(rows: readonly TierRow[], tier: Tier, delta: -1 | 1): TierRow[] {
  const i = rows.findIndex((r) => r.tier === tier);
  return reorder(rows, i, i + delta);
}

export type TierProblem = "none_enabled" | "plan_needs_subscription";

/** Validates rows plus the bound subscription. */
export function validateTiers(rows: readonly TierRow[], subscriptionId: string | null | undefined): TierProblem | null {
  const tiers = tiersFromRows(rows);
  if (tiers.length === 0) return "none_enabled";
  if (tiers.includes("plan") && !subscriptionId) return "plan_needs_subscription";
  return null;
}
