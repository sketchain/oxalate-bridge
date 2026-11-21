import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { availabilityQuery } from "@/api/queries";
import { planRows, poolCells, type PlanRow, type PoolCell } from "@/lib/availability";
import { formatTime, formatUSD, formatUSDOrUnlimited, formatWhen } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { Section } from "./Section";
import { EstimatedTag, StatusTag } from "./Tags";
import { EmptyState, ErrorBox } from "./Feedback";
import { Skeleton } from "./ui/skeleton";

function Cell({ cell, balance }: { cell: PoolCell; balance: number }) {
  const { t } = useTranslation();
  return (
    <td className={cn("px-3 py-2.5 align-top", cell.limitedByBalance && "border-l-[3px] border-l-ink")}>
      <div className="num text-[16px] font-medium">{formatUSD(cell.available)}</div>
      {cell.limitedByBalance ? (
        <div className="mt-0.5 text-[11px] leading-tight">
          <span className="font-semibold">{t("availability.limitedByBalance")}</span>
          <span className="num block text-muted">{t("availability.balance", { amount: formatUSD(balance) })}</span>
        </div>
      ) : cell.unlimitedPool ? (
        <div className="mt-0.5 text-[11px] text-muted">{t("availability.unlimitedPool")}</div>
      ) : null}
    </td>
  );
}

function PlanLine({ row, now }: { row: PlanRow; now: Date }) {
  const { t, i18n } = useTranslation();
  return (
    <tr className="border-b border-rule">
      <th scope="row" className="px-3 py-2.5 text-left align-top font-semibold">
        {row.name}
      </th>
      {row.pending ? (
        <td colSpan={3} className="px-3 py-2.5 text-muted">
          {t("plans.waitingAssignment")}
        </td>
      ) : (
        <>
          <td className="px-3 py-2.5 align-top">
            <div className="num text-[16px] font-medium">{row.five ? formatUSD(row.five.available) : "—"}</div>
            <div className="num text-[11px] text-muted">
              {row.five?.resetAt ? t("availability.resetsAt", { when: formatWhen(row.five.resetAt, now, i18n.language) }) : row.five?.unlimited ? t("availability.noFiveHour") : ""}
            </div>
          </td>
          <td className="px-3 py-2.5 align-top">
            <div className="num text-[16px] font-medium">{row.period ? formatUSD(row.period.available) : "—"}</div>
            <div className="num text-[11px] text-muted">{row.period?.endsAt ? t("availability.endsAt", { when: formatWhen(row.period.endsAt, now, i18n.language) }) : ""}</div>
          </td>
          <td className="px-3 py-2.5 align-top">{row.accountStatus ? <StatusTag kind={row.accountStatus} /> : null}</td>
        </>
      )}
    </tr>
  );
}

/** "可用额度": per-plan figures and the normal / forced pools (estimates). */
export function AvailabilityBlock({ n }: { n: number }) {
  const { t } = useTranslation();
  const now = useNow(30_000);
  const q = useQuery(availabilityQuery);
  const a = q.data;
  return (
    <Section
      n={n}
      title={t("availability.title")}
      aside={
        <>
          <EstimatedTag />
          {a ? <span className="num text-[11px] text-muted">{t("availability.computedAt", { time: formatTime(a.computed_at) })}</span> : null}
        </>
      }
      bodyClassName="p-0"
    >
      {q.error ? (
        <ErrorBox className="m-4" error={q.error} onRetry={() => q.refetch()} />
      ) : !a ? (
        <div className="flex flex-col gap-2 p-4">
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
        </div>
      ) : (
        <div className="flex flex-col">
          <div className="scroll-x">
            <table className="w-full min-w-[560px] border-collapse text-[13px]" aria-label={t("availability.plansTable")}>
              <thead>
                <tr className="border-b border-ink">
                  <th scope="col" className="label-mono px-3 py-2 text-left">
                    {t("availability.plan")}
                  </th>
                  <th scope="col" className="label-mono px-3 py-2 text-left">
                    {t("availability.fiveHour")}
                  </th>
                  <th scope="col" className="label-mono px-3 py-2 text-left">
                    {t("availability.period")}
                  </th>
                  <th scope="col" className="label-mono px-3 py-2 text-left">
                    {t("availability.accountStatus")}
                  </th>
                </tr>
              </thead>
              <tbody>
                {a.plans.length === 0 ? (
                  <tr className="border-b border-rule">
                    <td colSpan={4} className="px-3 py-3 text-muted">
                      {t("availability.noPlans")}
                    </td>
                  </tr>
                ) : (
                  planRows(a.plans).map((row) => <PlanLine key={row.id} row={row} now={now} />)
                )}
              </tbody>
            </table>
          </div>
          <div className="scroll-x border-t-2 border-ink">
            <table className="w-full min-w-[560px] border-collapse text-[13px]" aria-label={t("availability.poolsTable")}>
              <thead>
                <tr className="border-b border-ink">
                  <th scope="col" className="label-mono px-3 py-2 text-left">
                    {t("availability.callType")}
                  </th>
                  {poolCells(a.normal).map((c) => (
                    <th key={c.horizon} scope="col" className="label-mono px-3 py-2 text-left">
                      {c.label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {(
                  [
                    ["normal", a.normal],
                    ["force", a.force],
                  ] as const
                ).map(([kind, rows]) => (
                  <tr key={kind} className="border-b border-rule last:border-b-0">
                    <th scope="row" className="px-3 py-2.5 text-left align-top font-semibold">
                      {t(`availability.${kind}`)}
                    </th>
                    {poolCells(rows).map((c) => (
                      <Cell key={c.horizon} cell={c} balance={a.balance_available_micro} />
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {a.normal.every((x) => x.pool_micro === 0) && a.force.every((x) => x.pool_micro === 0) ? (
            <EmptyState className="m-4" title={t("availability.noPool")} description={t("availability.noPoolHint")} />
          ) : null}
          <p className="border-t border-ink px-3 py-2 text-[11px] text-muted">{t("availability.note", { unlimited: formatUSDOrUnlimited(2 ** 63) })}</p>
        </div>
      )}
    </Section>
  );
}
