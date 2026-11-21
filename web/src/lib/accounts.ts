import type { TFunction } from "i18next";
import type { Schemas } from "@/api/client";
import type { StatusKind } from "@/components/Tags";
import { formatDateTime, formatRelative, formatTime } from "./format";

type Account = Schemas["Account"];

/** Display status: "penalized" for a normal account whose weight is reduced. */
export function accountKind(a: Account): StatusKind {
  const s = a.runtime.status;
  if (s === "normal" && a.runtime.penalized) return "penalized";
  return s;
}

/** Assignable = enabled, no error, not stale and not cooling down. */
export function isAssignable(a: Account): boolean {
  return a.runtime.status === "normal";
}

/** Needs attention: stale, cooldown or error. */
export function needsAttention(a: Account): boolean {
  return a.runtime.status === "stale" || a.runtime.status === "cooldown" || a.runtime.status === "error";
}

/** One-line note for the card: reset time, cooldown end, or how stale. */
export function accountNote(a: Account, now: Date, locale: string, t: TFunction): string {
  const rt = a.runtime;
  switch (rt.status) {
    case "cooldown":
      return rt.cooldown_until ? t("accounts.note.cooldown", { time: formatTime(rt.cooldown_until), rel: formatRelative(rt.cooldown_until, now, locale) }) : rt.status_reason;
    case "stale": {
      const at = rt.fetched_at ?? rt.observed_at;
      return at ? t("accounts.note.stale", { rel: formatRelative(at, now, locale) }) : t("accounts.note.staleUnknown");
    }
    case "error":
      return rt.status_reason || t("status.error");
    case "disabled":
      return t("accounts.note.disabled");
  }
  const reset = rt.five_hour?.reset_at;
  if (rt.hard_exhausted) return rt.status_reason;
  if (reset) return t("accounts.note.reset", { time: formatDateTime(reset), rel: formatRelative(reset, now, locale) });
  return rt.status_reason || "";
}
