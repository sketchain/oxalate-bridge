import type { Schemas } from "@/api/client";
import { microToInput, parseUSD } from "./format";

type Key = Schemas["ApiKey"];
export type QuotaScope = Schemas["QuotaScope"];

/** Editable form of a key's limits (strings as typed). */
export interface LimitDraft {
  quota: string;
  scope: QuotaScope;
  l5h: string;
  l1d: string;
  l7d: string;
  /** datetime-local value ("" = never). */
  expires: string;
  /** One model per line (or comma separated). */
  models: string;
  /** One IP / CIDR per line (or comma separated). */
  ips: string;
  ownerQuery: boolean;
}

export type LimitErrors = Partial<Record<keyof LimitDraft, string>>;

const money = (v?: number | null) => (v == null ? "" : microToInput(v));

function toLocalInput(iso?: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function draftFromKey(k?: Key | null): LimitDraft {
  return {
    quota: money(k?.quota_limit_micro),
    scope: k?.quota_scope ?? "all",
    l5h: money(k?.limit_5h_micro),
    l1d: money(k?.limit_1d_micro),
    l7d: money(k?.limit_7d_micro),
    expires: toLocalInput(k?.expires_at),
    models: (k?.model_allowlist ?? []).join("\n"),
    ips: (k?.ip_allowlist ?? []).join("\n"),
    ownerQuery: k?.allow_owner_query ?? false,
  };
}

/** Splits a list typed one per line or comma separated. */
export function splitList(s: string): string[] {
  return [...new Set(s.split(/[\n,，\s]+/).map((x) => x.trim()).filter(Boolean))];
}

const IPV4 = /^(25[0-5]|2[0-4]\d|1?\d?\d)(\.(25[0-5]|2[0-4]\d|1?\d?\d)){3}$/;

/** Light client-side check of an IP or CIDR (the server validates strictly). */
export function looksLikeIP(v: string): boolean {
  const [addr = "", bits, ...rest] = v.split("/");
  if (rest.length) return false;
  const v4 = IPV4.test(addr);
  const v6 = !v4 && addr.includes(":") && /^[0-9a-fA-F:.]+$/.test(addr);
  if (!v4 && !v6) return false;
  if (bits === undefined) return true;
  if (!/^\d+$/.test(bits)) return false;
  return Number(bits) <= (v4 ? 32 : 128);
}

type LimitBody = Pick<
  Schemas["ApiKeyUpdate"],
  "quota_limit_micro" | "quota_scope" | "limit_5h_micro" | "limit_1d_micro" | "limit_7d_micro" | "expires_at" | "model_allowlist" | "ip_allowlist" | "allow_owner_query"
>;

/**
 * Validates a draft and turns it into the request fields. Error values are
 * i18n keys. Empty fields mean "unlimited" (null).
 */
export function draftToBody(d: LimitDraft): { body: LimitBody; errors: LimitErrors } {
  const errors: LimitErrors = {};
  const amount = (field: "quota" | "l5h" | "l1d" | "l7d"): number | null => {
    const v = d[field].trim();
    if (v === "") return null;
    const m = parseUSD(v);
    if (!Number.isFinite(m) || m < 0) {
      errors[field] = "validation.nonNegativeAmount";
      return null;
    }
    return m;
  };
  let expires: string | null = null;
  if (d.expires.trim()) {
    const t = new Date(d.expires);
    if (Number.isNaN(t.getTime())) errors.expires = "validation.date";
    else expires = t.toISOString();
  }
  const models = splitList(d.models);
  const ips = splitList(d.ips);
  if (models.length > 100) errors.models = "validation.tooMany";
  if (ips.length > 100) errors.ips = "validation.tooMany";
  else if (ips.some((ip) => !looksLikeIP(ip))) errors.ips = "validation.ip";
  const body: LimitBody = {
    quota_limit_micro: amount("quota"),
    quota_scope: d.scope,
    limit_5h_micro: amount("l5h"),
    limit_1d_micro: amount("l1d"),
    limit_7d_micro: amount("l7d"),
    expires_at: expires,
    model_allowlist: models.length ? models : null,
    ip_allowlist: ips.length ? ips : null,
    allow_owner_query: d.ownerQuery,
  };
  return { body, errors };
}

/** Used fraction (0–100+) of a limit, or null without a limit. */
export function usedPercent(used: number, limit?: number | null): number | null {
  if (limit == null) return null;
  if (limit <= 0) return 100;
  return (used / limit) * 100;
}
