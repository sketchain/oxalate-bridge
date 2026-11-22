import { useTranslation } from "react-i18next";
import type { Schemas } from "@/api/client";
import { formatDateTime, formatUSD } from "@/lib/format";
import { usedPercent } from "@/lib/keyLimits";
import { ProgressBar } from "./Progress";

/** One limit row: label, used / limit, bar and the recovery time of an exhausted window. */
function LimitRow({ label, used, held, limit, recoversAt }: { label: string; used: number; held: number; limit?: number | null; recoversAt?: string | null }) {
  const { t } = useTranslation();
  const pct = usedPercent(used + held, limit);
  return (
    <div className="flex flex-col gap-1.5 py-2.5">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 text-[13px]">
        <span className="font-semibold">{label}</span>
        <span className="num">
          {formatUSD(used)} / {limit == null ? t("keyLimits.unlimited") : formatUSD(limit)}
        </span>
      </div>
      {pct !== null ? <ProgressBar value={pct} label={label} /> : null}
      {held > 0 ? <span className="num text-[11px] text-muted">{t("keyLimits.held", { amount: formatUSD(held) })}</span> : null}
      {recoversAt ? <span className="text-[12px] font-semibold">{t("keyLimits.recoversAt", { time: formatDateTime(recoversAt) })}</span> : null}
    </div>
  );
}

/** Real-time quota and window usage of a key. */
export function KeyLimitsPanel({ limits }: { limits: Schemas["KeyLimitStatus"] }) {
  const { t } = useTranslation();
  const held = limits.quota_held_micro;
  return (
    <div className="flex flex-col">
      <div className="flex flex-wrap items-center gap-2 text-[12px]">
        <span className="label-mono border border-ink px-1.5 py-0.5">{t(`keyLimits.scope.${limits.quota_scope}`)}</span>
        {limits.usage_reset_at ? <span className="text-muted">{t("keyLimits.resetAt", { time: formatDateTime(limits.usage_reset_at) })}</span> : null}
      </div>
      <div className="divide-y divide-rule">
        <LimitRow label={t("keyLimits.quota")} used={limits.quota_used_micro} held={held} limit={limits.quota_limit_micro} />
        {limits.windows.map((w) => (
          <LimitRow key={w.window} label={t(`keyLimits.window.${w.window}`)} used={w.used_micro} held={w.held_micro ?? 0} limit={w.limit_micro} recoversAt={w.recovers_at} />
        ))}
      </div>
      <dl className="num mt-2 grid grid-cols-2 border border-ink text-[12px]">
        <div className="p-2">
          <dt className="label-mono text-muted">{t("keyLimits.usedAll")}</dt>
          <dd className="text-[15px]">{formatUSD(limits.used_all_micro)}</dd>
        </div>
        <div className="border-l border-ink p-2">
          <dt className="label-mono text-muted">{t("keyLimits.usedBalance")}</dt>
          <dd className="text-[15px]">{formatUSD(limits.used_balance_micro)}</dd>
        </div>
      </dl>
    </div>
  );
}
