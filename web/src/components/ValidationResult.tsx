import { useTranslation } from "react-i18next";
import type { Schemas } from "@/api/client";
import { cn } from "@/lib/utils";

/** Result of an account connection check (import dialog, test button). */
export function ValidationResult({ r }: { r: Schemas["AccountTestResult"] }) {
  const { t } = useTranslation();
  const rows: [string, string | undefined][] = [
    ["instance", r.instance],
    ["subscription", r.subscription],
    ["tier", r.tier],
    ["auth_index", r.auth_index],
    ["provider", r.provider],
  ];
  return (
    <div className={cn("border-2", r.ok ? "border-ink" : "border-red")} role="status">
      <div className={cn("label-mono flex items-center justify-between px-3 py-1.5", r.ok ? "bg-yellow text-on-yellow" : "bg-red text-on-red")}>
        <span>{r.ok ? t("accounts.validOk") : t("accounts.validBad")}</span>
        <span>
          /quota {r.quota_ok ? "OK" : "FAIL"} · /v1/models {r.models_ok ? "OK" : "FAIL"}
        </span>
      </div>
      <dl className="grid grid-cols-[120px_1fr] gap-x-3 gap-y-1 px-3 py-2 text-[13px]">
        {rows.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="label-mono text-muted">{k}</dt>
            <dd className="num">{v || "—"}</dd>
          </div>
        ))}
        <dt className="label-mono text-muted">models</dt>
        <dd className="num break-words">{r.models?.join(", ") || "—"}</dd>
      </dl>
      {!r.ok ? (
        <p className="border-t border-red px-3 py-2 text-[12px]">
          {r.quota_ok ? t("accounts.modelsFailed") : r.subscription && r.subscription !== "claude" ? t("errors.unsupported_subscription") : t("accounts.quotaFailed")}
        </p>
      ) : null}
    </div>
  );
}
