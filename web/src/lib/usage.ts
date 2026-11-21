import { useMemo } from "react";
import type { Schemas } from "@/api/client";
import { formatDate } from "./format";
import { daysBetween, localBuckets } from "./availability";

/** Local midnight of the day containing d (plus `days`). */
export function startOfDay(d: Date, days = 0): Date {
  const out = new Date(d);
  out.setHours(0, 0, 0, 0);
  out.setDate(out.getDate() + days);
  return out;
}

/**
 * A stable [from, to) range of whole local days: `days` days ending with
 * today. The ISO strings only change when the day changes, so query keys
 * stay stable across renders.
 */
export function useDayRange(days: number) {
  const today = formatDate(new Date());
  return useMemo(() => {
    const to = startOfDay(new Date(), 1);
    const from = startOfDay(new Date(), 1 - days);
    return { from: from.toISOString(), to: to.toISOString(), fromDate: from, toDate: to };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [days, today]);
}

type Bucket = Pick<Schemas["MyUsageBucket"], "bucket" | "tier" | "billed_micro" | "model" | "requests">;

export interface TierRow {
  key: string;
  label: string;
  plan: number;
  standard: number;
  force: number;
}

/** Billed amounts per local day (or hour) and tier, zero-filled for days. */
export function tierSeries(items: Bucket[], mode: "hour" | "day", from: Date, to: Date): TierRow[] {
  const map = new Map<string, TierRow>();
  const label = (d: Date) =>
    mode === "day" ? `${d.getMonth() + 1}/${d.getDate()}` : `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, "0")}h`;
  if (mode === "day") {
    for (const d of daysBetween(from, to)) map.set(d.toISOString(), { key: d.toISOString(), label: label(d), plan: 0, standard: 0, force: 0 });
  }
  for (const it of localBuckets(items, mode)) {
    const d = new Date(it.local);
    const row = map.get(it.local) ?? { key: it.local, label: label(d), plan: 0, standard: 0, force: 0 };
    if (it.tier === "plan" || it.tier === "standard" || it.tier === "force") row[it.tier] += it.billed_micro;
    map.set(it.local, row);
  }
  return [...map.values()].sort((a, b) => a.key.localeCompare(b.key));
}

/** Billed amount and requests per model, largest first. */
export function modelSeries(items: Bucket[]) {
  const map = new Map<string, { name: string; value: number; requests: number }>();
  for (const it of items) {
    const row = map.get(it.model) ?? { name: it.model || "—", value: 0, requests: 0 };
    row.value += it.billed_micro;
    row.requests += it.requests;
    map.set(it.model, row);
  }
  return [...map.values()].sort((a, b) => b.value - a.value);
}

/** Figures for the screen-reader description of a tier series. */
export function seriesFacts(rows: TierRow[], fmt: (v: number) => string) {
  const totals = rows.map((r) => ({ label: r.label, total: r.plan + r.standard + r.force }));
  const sum = totals.reduce((a, b) => a + b.total, 0);
  const peak = totals.reduce((a, b) => (b.total > a.total ? b : a), { label: "—", total: 0 });
  const tiers = rows.reduce((a, r) => ({ plan: a.plan + r.plan, standard: a.standard + r.standard, force: a.force + r.force }), { plan: 0, standard: 0, force: 0 });
  return {
    n: rows.length,
    total: fmt(sum),
    peakLabel: peak.label,
    peak: fmt(peak.total),
    plan: fmt(tiers.plan),
    standard: fmt(tiers.standard),
    force: fmt(tiers.force),
  };
}
