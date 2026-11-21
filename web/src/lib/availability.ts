import type { Schemas } from "@/api/client";
import { UNLIMITED } from "./format";

export type Horizon = Schemas["AvailabilityHorizon"]["horizon"];
export const HORIZONS: Horizon[] = ["1h", "5h", "24h", "7d"];
/** Column labels: the API's 24h is shown as 1d. */
export const HORIZON_LABEL: Record<Horizon, string> = { "1h": "1h", "5h": "5h", "24h": "1d", "7d": "7d" };

export interface PoolCell {
  horizon: Horizon;
  label: string;
  /** Usable amount now (min of pool and balance), never negative. */
  available: number;
  /** The pool is effectively unlimited (no 5-hour window and a huge week). */
  unlimitedPool: boolean;
  /** The balance, not the pool, is the limit. */
  limitedByBalance: boolean;
}

export function poolCells(rows: Schemas["AvailabilityHorizon"][]): PoolCell[] {
  return HORIZONS.map((h) => {
    const r = rows.find((x) => x.horizon === h);
    return {
      horizon: h,
      label: HORIZON_LABEL[h],
      available: Math.max(0, r?.available_micro ?? 0),
      unlimitedPool: (r?.pool_micro ?? 0) >= UNLIMITED,
      limitedByBalance: r?.limited_by_balance ?? false,
    };
  });
}

export type AccountState = "normal" | "stale" | "cooldown" | "unavailable";

export interface PlanRow {
  id: string;
  name: string;
  pending: boolean;
  accountStatus: AccountState | null;
  five: { available: number; resetAt: string | null; unlimited: boolean } | null;
  period: { available: number; endsAt: string | null } | null;
}

export function planRows(plans: Schemas["PlanAvailability"][]): PlanRow[] {
  return plans.map((p) => ({
    id: p.subscription_id,
    name: p.plan_name,
    pending: p.status === "pending_assignment",
    accountStatus: (p.account_status ?? null) as AccountState | null,
    five: p.five_hour
      ? {
          available: Math.max(0, p.five_hour.available_micro),
          resetAt: p.five_hour.reset_at ?? null,
          // No 5-hour window: the figure is just the plan remaining.
          unlimited: !p.five_hour.reset_at && p.account_status !== "unavailable",
        }
      : null,
    period: p.period ? { available: Math.max(0, p.period.available_micro), endsAt: p.period.ends_at ?? null } : null,
  }));
}

type TokenBucket = Pick<Schemas["MyUsageBucket"], "input_tokens" | "cache_read_tokens" | "cache_write_tokens">;

/** Cache hit rate = cache read ÷ (uncached input + cache read + cache write); null without input. */
export function cacheHitRate(buckets: TokenBucket[]): number | null {
  let read = 0;
  let total = 0;
  for (const b of buckets) {
    read += b.cache_read_tokens;
    total += b.input_tokens + b.cache_read_tokens + b.cache_write_tokens;
  }
  return total > 0 ? read / total : null;
}

/** Spend split: plan tier vs balance tiers (standard + force). */
export function spendSplit(buckets: Pick<Schemas["MyUsageBucket"], "tier" | "billed_micro">[]) {
  let plan = 0;
  let balance = 0;
  for (const b of buckets) {
    if (b.tier === "plan") plan += b.billed_micro;
    else if (b.tier === "standard" || b.tier === "force") balance += b.billed_micro;
  }
  return { plan, balance, total: plan + balance };
}

/** Progress of a usage figure, clamped to [0, ∞) percent. */
export function usedPercent(used: number, total: number): number {
  if (total <= 0) return used > 0 ? 100 : 0;
  return Math.max(0, (used / total) * 100);
}

/** Stacks usage buckets per time bucket and tier for charts. */
export function stackByTier<T extends { bucket: string; tier: string; billed_micro: number }>(items: T[]) {
  const map = new Map<string, { bucket: string; plan: number; standard: number; force: number }>();
  for (const it of items) {
    const row = map.get(it.bucket) ?? { bucket: it.bucket, plan: 0, standard: 0, force: 0 };
    if (it.tier === "plan" || it.tier === "standard" || it.tier === "force") row[it.tier] += it.billed_micro;
    map.set(it.bucket, row);
  }
  return [...map.values()].sort((a, b) => a.bucket.localeCompare(b.bucket));
}

/** Fills missing day buckets between from and to (local days) with zeros. */
export function fillDays<T extends { bucket: string }>(rows: T[], days: Date[], empty: (bucket: string) => T): T[] {
  const byDay = new Map(rows.map((r) => [dayKey(new Date(r.bucket)), r]));
  return days.map((d) => byDay.get(dayKey(d)) ?? empty(d.toISOString()));
}

export function dayKey(d: Date): string {
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
}

/**
 * Re-buckets hourly usage rows into local hours or local days. The API cuts
 * day buckets at UTC midnight, so day views are built from hourly rows.
 */
export function localBuckets<T extends { bucket: string }>(rows: T[], mode: "hour" | "day"): (T & { local: string })[] {
  return rows.map((r) => {
    const d = new Date(r.bucket);
    if (mode === "day") d.setHours(0, 0, 0, 0);
    else d.setMinutes(0, 0, 0);
    return { ...r, local: d.toISOString() };
  });
}

/** Local day starts from `from` (inclusive) to `to` (exclusive). */
export function daysBetween(from: Date, to: Date): Date[] {
  const out: Date[] = [];
  const d = new Date(from);
  d.setHours(0, 0, 0, 0);
  while (d < to) {
    out.push(new Date(d));
    d.setDate(d.getDate() + 1);
  }
  return out;
}
