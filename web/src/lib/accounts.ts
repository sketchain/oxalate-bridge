import type { TFunction } from "i18next";
import type { Schemas } from "@/api/client";
import type { StatusKind } from "@/components/Tags";
import { formatDateTime, formatRelative, formatTime } from "./format";

type Account = Schemas["Account"];

/**
 * Display status, decided by the backend (`runtime.display_status`) by
 * priority: error > disabled > cooldown > 5h / 7d full or paused > stale >
 * penalized > normal.
 */
export function accountKind(a: Account): StatusKind {
  return a.runtime.display_status;
}

/** Assignable = the backend status is normal (penalized and full accounts still take subscriptions). */
export function isAssignable(a: Account): boolean {
  return a.runtime.status === "normal";
}

/** Needs attention: stale, cooldown or error. */
export function needsAttention(a: Account): boolean {
  const d = a.runtime.display_status;
  return d === "stale" || d === "cooldown" || d === "error";
}

/** Translated reason; unknown codes read "其他原因" and keep the code for the caller to show. */
export function reasonLabel(t: TFunction, code: string): { text: string; unknown: boolean } {
  if (!code) return { text: "", unknown: false };
  const text = t(`reason.${code}`, { defaultValue: "" });
  return text ? { text, unknown: false } : { text: t("reason.other"), unknown: true };
}

/** One-line note for the card: reset time, cooldown end, or how stale. Never shows a raw reason code. */
export function accountNote(a: Account, now: Date, locale: string, t: TFunction): string {
  const rt = a.runtime;
  const reason = reasonLabel(t, rt.status_reason_code);
  const reasonText = reason.unknown ? `${reason.text} (${rt.status_reason_code})` : reason.text;
  switch (rt.display_status) {
    case "cooldown":
      return rt.cooldown_until
        ? `${reasonText} · ${t("accounts.note.cooldown", { time: formatTime(rt.cooldown_until), rel: formatRelative(rt.cooldown_until, now, locale) })}`
        : reasonText;
    case "stale": {
      const at = rt.fetched_at ?? rt.observed_at;
      return at ? t("accounts.note.stale", { rel: formatRelative(at, now, locale) }) : t("accounts.note.staleUnknown");
    }
    case "error":
      return reasonText || t("status.error");
    case "disabled":
      return t("accounts.note.disabled");
    case "paused":
      return t("accounts.note.paused");
    case "five_hour_full":
    case "week_full": {
      const w = rt.display_status === "week_full" ? rt.week : rt.five_hour;
      return w?.reset_at ? t("accounts.note.full", { reason: reasonText, time: formatDateTime(w.reset_at), rel: formatRelative(w.reset_at, now, locale) }) : reasonText;
    }
  }
  const reset = rt.five_hour?.reset_at;
  if (reset) return t("accounts.note.reset", { time: formatDateTime(reset), rel: formatRelative(reset, now, locale) });
  return "";
}
