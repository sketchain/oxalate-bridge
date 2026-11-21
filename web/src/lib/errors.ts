import type { TFunction } from "i18next";
import { ApiError, ERROR_CODES } from "@/api/client";

/** i18n key of an error code. */
export function errorKey(code: string): string {
  return (ERROR_CODES as readonly string[]).includes(code) || code === "network" ? `errors.${code}` : "errors.unknown";
}

/** User-facing text for any thrown value; never the raw backend message. */
export function errorText(t: TFunction, err: unknown): string {
  if (err instanceof ApiError) return t(errorKey(err.code));
  return t("errors.unknown");
}
