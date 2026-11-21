import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "@/api/client";
import { formatDateTime, formatUSD, formatWhen } from "@/lib/format";
import { usedPercent } from "@/lib/availability";
import { useNow } from "@/lib/hooks";
import { shortId } from "@/lib/utils";
import { ProgressBar } from "./Progress";

type Sub = Schemas["MySubscription"] | Schemas["Subscription"];

/** Reagent-label card of one subscription. */
export function PlanCard({ sub, actions, extra }: { sub: Sub; actions?: ReactNode; extra?: ReactNode }) {
  const { t, i18n } = useTranslation();
  const now = useNow(60_000);
  const p = sub.current_period ?? null;
  const used = p ? p.used_billed_micro : 0;
  const pct = p ? usedPercent(p.quota_micro - Math.max(0, p.remaining_micro), p.quota_micro) : 0;
  const models = sub.allowed_models.length ? sub.allowed_models.join(", ") : t("plans.allModels");
  const rows: [string, ReactNode][] = [
    [
      t("plans.spec"),
      <>
        <span className="num">{formatUSD(sub.weekly_quota_micro)}</span> / {t("plans.week")} · {t(`duration.${sub.duration_unit}`, { count: sub.duration_value })} · {models}
      </>,
    ],
    [
      t("plans.periodRange"),
      p ? (
        <span className="num">
          {formatDateTime(p.period_start)} → {formatDateTime(p.period_end)}
        </span>
      ) : (
        <span className="text-muted">—</span>
      ),
    ],
    [t("plans.expiresAt"), <span className="num">{sub.expires_at ? formatWhen(sub.expires_at, now, i18n.language) : "—"}</span>],
  ];
  return (
    <article className="flex flex-col border-2 border-ink" aria-label={sub.plan_name}>
      <header className="relative bg-yellow px-4 pt-3 pb-3 text-on-yellow">
        <div className="label-mono">
          PLAN · No. {shortId(sub.id)}
        </div>
        <h3 className="font-display mt-1 pr-24 text-[26px]">{sub.plan_name}</h3>
        {/* Status plate: fixed black with yellow text on the yellow header (both themes). */}
        <span className="label-mono absolute top-3 right-3 bg-[#0A0A0A] px-2 py-1 text-[#F2C200]">{t(`subStatus.${sub.status}`)}</span>
      </header>
      <dl className="flex-1 divide-y divide-rule border-t-2 border-ink">
        {rows.map(([k, v]) => (
          <div key={k} className="grid grid-cols-[96px_1fr] gap-3 px-4 py-2.5 text-[13px]">
            <dt className="label-mono self-center text-muted">{k}</dt>
            <dd className="min-w-0 break-words">{v}</dd>
          </div>
        ))}
        <div className="grid grid-cols-[96px_1fr] gap-3 px-4 py-3 text-[13px]">
          <dt className="label-mono self-center text-muted">{t("plans.periodUsage")}</dt>
          <dd className="min-w-0">
            {sub.status === "pending_assignment" ? (
              <span className="font-semibold">{t("plans.waitingAssignment")}</span>
            ) : p ? (
              <>
                <div className="num mb-1.5 flex justify-between text-[12px]">
                  <span>
                    {formatUSD(used)} / {formatUSD(p.quota_micro)}
                  </span>
                  <span>{t("plans.remaining", { amount: formatUSD(Math.max(0, p.remaining_micro)) })}</span>
                </div>
                <ProgressBar value={pct} label={t("plans.periodUsage")} />
              </>
            ) : (
              <span className="text-muted">—</span>
            )}
          </dd>
        </div>
        {extra}
      </dl>
      {actions ? <footer className="flex flex-wrap gap-2 border-t-2 border-ink p-3 [&>*]:flex-1">{actions}</footer> : null}
    </article>
  );
}
