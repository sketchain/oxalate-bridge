/** Formatting helpers. Amounts are micro-USD integers. */

export const MICRO = 1_000_000;
/** int64 capacities at or above this are effectively unlimited. */
export const UNLIMITED = 2 ** 62;

/**
 * USD with two decimals at or above $0.01 (in absolute value), four below.
 * Grouping uses commas; negative amounts get a leading minus.
 */
export function formatUSD(micro: number): string {
  const sign = micro < 0 ? "-" : "";
  const abs = Math.abs(micro);
  const usd = abs / MICRO;
  const digits = abs >= 10_000 || abs === 0 ? 2 : 4;
  return `${sign}$${usd.toLocaleString("en-US", { minimumFractionDigits: digits, maximumFractionDigits: digits })}`;
}

/** USD that may be unlimited. */
export function formatUSDOrUnlimited(micro: number): string {
  return micro >= UNLIMITED ? "∞" : formatUSD(micro);
}

/** Token counts as plain, k or M. */
export function formatTokens(n: number): string {
  const abs = Math.abs(n);
  if (abs < 1000) return String(n);
  if (abs < 1_000_000) return `${trim((n / 1000).toFixed(abs < 10_000 ? 2 : 1))}k`;
  return `${trim((n / 1_000_000).toFixed(abs < 10_000_000 ? 2 : 1))}M`;
}

function trim(s: string): string {
  return s.includes(".") ? s.replace(/\.?0+$/, "") : s;
}

export function formatPercent(v: number, digits = 1): string {
  return `${trim(v.toFixed(digits))}%`;
}

export function formatInt(n: number): string {
  return n.toLocaleString("en-US");
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

/** Local date and time, YYYY-MM-DD HH:mm. */
export function formatDateTime(iso: string | Date | null | undefined): string {
  if (!iso) return "—";
  const d = typeof iso === "string" ? new Date(iso) : iso;
  return `${formatDate(d)} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Local date, YYYY-MM-DD. */
export function formatDate(iso: string | Date | null | undefined): string {
  if (!iso) return "—";
  const d = typeof iso === "string" ? new Date(iso) : iso;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** Local time, HH:mm (or HH:mm:ss). */
export function formatTime(iso: string | Date, seconds = false): string {
  const d = typeof iso === "string" ? new Date(iso) : iso;
  return `${pad(d.getHours())}:${pad(d.getMinutes())}${seconds ? `:${pad(d.getSeconds())}` : ""}`;
}

const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["day", 86_400_000],
  ["hour", 3_600_000],
  ["minute", 60_000],
];

/** Relative description such as "3 天后" / "in 3 days" / "5 分钟前". */
export function formatRelative(iso: string | Date, now: Date, locale: string): string {
  const d = typeof iso === "string" ? new Date(iso) : iso;
  const diff = d.getTime() - now.getTime();
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  for (const [unit, ms] of UNITS) {
    if (Math.abs(diff) >= ms) return rtf.format(Math.round(diff / ms), unit);
  }
  return rtf.format(Math.round(diff / 1000), "second");
}

/** Local date-time plus a relative hint for times in the future. */
export function formatWhen(iso: string | null | undefined, now: Date, locale: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  const abs = formatDateTime(d);
  return d.getTime() > now.getTime() ? `${abs}（${formatRelative(d, now, locale)}）` : abs;
}

const WEEKDAYS = ["SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"];

/** Page code line, e.g. "OB-01 · 2026-11-21 SAT". */
export function pageCode(code: string, now: Date): string {
  return `${code} · ${formatDate(now)} ${WEEKDAYS[now.getDay()]}`;
}

/** Splits seconds into the largest whole unit for inputs (s, min, h, d). */
export type DurationUnit = "s" | "min" | "h" | "d";
export const DURATION_FACTORS: Record<DurationUnit, number> = { s: 1, min: 60, h: 3600, d: 86400 };

export function toDuration(seconds: number): { value: number; unit: DurationUnit } {
  for (const unit of ["d", "h", "min"] as const) {
    const f = DURATION_FACTORS[unit];
    if (seconds >= f && seconds % f === 0) return { value: seconds / f, unit };
  }
  return { value: seconds, unit: "s" };
}

export function fromDuration(value: number, unit: DurationUnit): number {
  return Math.round(value * DURATION_FACTORS[unit]);
}

/** Human readable duration, e.g. "1h 30min" / "2d". */
export function formatDuration(seconds: number): string {
  if (seconds <= 0) return "0s";
  const parts: string[] = [];
  let rest = Math.round(seconds);
  for (const unit of ["d", "h", "min", "s"] as const) {
    const f = DURATION_FACTORS[unit];
    const n = Math.floor(rest / f);
    if (n > 0) {
      parts.push(`${n}${unit}`);
      rest -= n * f;
    }
    if (parts.length === 2) break;
  }
  return parts.join(" ");
}

/** Parses a USD amount typed by a person into micro-USD (NaN when invalid). */
export function parseUSD(input: string): number {
  const s = input.trim().replace(/^\$/, "").replace(/,/g, "");
  if (!/^-?\d+(\.\d+)?$/.test(s)) return NaN;
  return Math.round(Number(s) * MICRO);
}

/** micro-USD as a plain decimal string for inputs. */
export function microToInput(micro: number): string {
  return trim((micro / MICRO).toFixed(6));
}
