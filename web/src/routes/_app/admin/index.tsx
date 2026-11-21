import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ArrowRight, Diamond, Radio } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { keys, overviewQuery } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { StatStrip } from "@/components/StatStrip";
import { OversoldLine, StatusTag, StatusCode, TierTag, type StatusKind } from "@/components/Tags";
import { EmptyState, ErrorBox } from "@/components/Feedback";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminEvent } from "@/lib/events";
import { useApiMutation } from "@/lib/mutations";
import { formatDateTime, formatInt, formatRelative, formatTime, formatUSD } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { cn, shortId } from "@/lib/utils";

export const Route = createFileRoute("/_app/admin/")({
  loader: ({ context: { queryClient } }) => queryClient.prefetchQuery(overviewQuery),
  component: AdminOverview,
});

const SEGMENTS: { key: keyof Schemas["OverviewAccounts"]; kind: StatusKind; cls: string }[] = [
  { key: "normal", kind: "normal", cls: "bg-ink" },
  { key: "penalized", kind: "penalized", cls: "bg-yellow" },
  { key: "full", kind: "full", cls: "hazard-sm" },
  { key: "stale", kind: "stale", cls: "bg-bg" },
  { key: "cooldown", kind: "cooldown", cls: "hazard" },
  { key: "error", kind: "error", cls: "bg-red" },
  { key: "disabled", kind: "disabled", cls: "bg-rule" },
];

function AdminOverview() {
  const { t, i18n } = useTranslation();
  const now = useNow(30_000);
  const qc = useQueryClient();
  const q = useQuery(overviewQuery);
  const o = q.data;
  const [live, setLive] = useState<Schemas["RequestSettledEvent"][]>([]);
  useAdminEvent((ev) => {
    if (ev.type === "request_settled") setLive((l) => [ev.data, ...l].slice(0, 50));
  });
  const reconcile = useApiMutation({
    mutationFn: () => unwrap(api.POST("/admin/reconcile")),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: keys.overview });
      return r;
    },
    success: t("adminOverview.reconcileDone"),
  });

  if (q.error) return <ErrorBox error={q.error} onRetry={() => q.refetch()} />;
  const acc = o?.accounts;
  const attention = acc ? acc.stale + acc.cooldown + acc.error : 0;

  return (
    <>
      <PageHeader code="OB-07" title={t("adminOverview.title")} />
      <div className="flex flex-col gap-6">
        <StatStrip
          loading={!o}
          stats={[
            {
              label: t("adminOverview.todayRequests"),
              value: formatInt(o?.today.requests ?? 0),
              note: o ? t("adminOverview.todayRange", { day: o.today.day, tz: o.timezone }) : "",
            },
            {
              label: t("adminOverview.todayBilled"),
              value: formatUSD(o?.today.billed_micro ?? 0),
              note: t("adminOverview.upstream", { amount: formatUSD(o?.today.upstream_cost_micro ?? 0) }),
            },
            {
              label: t("adminOverview.pending"),
              value: formatInt(o?.pending_assignment ?? 0),
              note: t("adminOverview.pendingNote"),
              alert: (o?.pending_assignment ?? 0) > 0,
            },
            {
              label: t("adminOverview.headroom"),
              value: formatUSD(acc?.headroom_micro ?? 0),
              note: (
                <>
                  {t("adminOverview.reserved", { amount: formatUSD(acc?.reserved_micro ?? 0) })}
                  {acc ? <OversoldLine count={acc.oversold_count} amount={formatUSD(acc.oversold_micro)} /> : null}
                </>
              ),
            },
            {
              label: t("adminOverview.attention"),
              value: formatInt(attention),
              note: t("adminOverview.attentionNote"),
              alert: attention > 0,
              icon: attention > 0 ? <Diamond className="size-3.5 fill-current" aria-hidden /> : undefined,
            },
          ]}
        />

        {o && o.pending_assignment > 0 ? (
          <div role="status" className="flex flex-wrap items-center gap-3 border-2 border-ink bg-yellow px-4 py-3 text-on-yellow">
            <Diamond className="size-4 fill-current" aria-hidden />
            <span className="font-semibold">{t("adminOverview.pendingBanner", { count: o.pending_assignment })}</span>
            <Button asChild variant="ink" size="sm" className="ml-auto">
              <Link to="/admin/subscriptions" search={{ tab: "pending" }}>
                {t("adminOverview.goAssign")}
                <ArrowRight aria-hidden />
              </Link>
            </Button>
          </div>
        ) : null}

        <div className="grid gap-6 xl:grid-cols-2">
          <Section n={2} title={t("adminOverview.accountStatus")} aside={<Link to="/admin/accounts" className="text-[13px] font-semibold underline underline-offset-4">{t("adminOverview.allAccounts")}</Link>}>
            {!acc ? (
              <Skeleton className="h-24" />
            ) : acc.total === 0 ? (
              <EmptyState
                title={t("accounts.emptyTitle")}
                description={t("accounts.emptyHint")}
                action={
                  <Button asChild variant="primary" size="sm">
                    <Link to="/admin/accounts">{t("accounts.import")}</Link>
                  </Button>
                }
              />
            ) : (
              <>
                <div
                  className="flex h-6 border-2 border-ink"
                  role="img"
                  aria-label={t("adminOverview.distributionLabel", {
                    list: SEGMENTS.map((s) => `${t(`status.${s.kind}`)} ${acc[s.key]}`).join("，"),
                    total: acc.total,
                  })}
                >
                  {SEGMENTS.filter((s) => (acc[s.key] as number) > 0).map((s) => (
                    <div key={s.key} className={cn("h-full border-r border-ink last:border-r-0", s.cls)} style={{ width: `${((acc[s.key] as number) / acc.total) * 100}%` }} />
                  ))}
                </div>
                <ul className="mt-4 grid grid-cols-2 gap-2 sm:grid-cols-3">
                  {SEGMENTS.map((s) => (
                    <li key={s.key} className="flex items-center justify-between gap-2 border border-ink px-2 py-1.5">
                      <StatusTag kind={s.kind} />
                      <span className="num text-[18px]">{acc[s.key] as number}</span>
                    </li>
                  ))}
                </ul>
              </>
            )}
          </Section>

          <Section n={3} title={t("adminOverview.system")}>
            {!o ? (
              <Skeleton className="h-24" />
            ) : (
              <dl className="divide-y divide-rule border-y-2 border-ink text-[13px]">
                <div className="flex items-center justify-between gap-3 py-2.5">
                  <dt className="font-semibold">{t("adminOverview.backlog")}</dt>
                  <dd className={cn("num text-[18px]", o.stream_backlog > 1000 && "bg-yellow px-1 text-on-yellow")}>{formatInt(o.stream_backlog)}</dd>
                </div>
                <div className="flex flex-wrap items-center justify-between gap-3 py-2.5">
                  <dt className="font-semibold">{t("adminOverview.reconcile")}</dt>
                  <dd className="flex flex-wrap items-center gap-2">
                    {o.last_reconcile ? (
                      <>
                        <span className={cn("label-mono px-1.5 py-0.5", o.last_reconcile.ok ? "bg-ink text-bg" : "bg-red text-on-red")}>
                          {o.last_reconcile.ok ? t("adminOverview.reconcileOk") : t("adminOverview.reconcileBad")}
                        </span>
                        <span className="num text-muted">
                          {formatDateTime(o.last_reconcile.finished_at)} · {formatRelative(o.last_reconcile.finished_at, now, i18n.language)}
                        </span>
                        {!o.last_reconcile.ok ? (
                          <span className="num">
                            {t("adminOverview.mismatch", { n: o.last_reconcile.balance_mismatches, diff: formatUSD(o.last_reconcile.charge_diff_micro) })}
                          </span>
                        ) : null}
                      </>
                    ) : (
                      <span className="text-muted">{t("adminOverview.neverReconciled")}</span>
                    )}
                    <Button size="sm" onClick={() => reconcile.mutate()} disabled={reconcile.isPending}>
                      {t("adminOverview.reconcileNow")}
                    </Button>
                  </dd>
                </div>
              </dl>
            )}
            {o && o.today.by_tier.length ? (
              <table className="mt-4 w-full text-[13px]" aria-label={t("adminOverview.byTier")}>
                <thead>
                  <tr className="border-t-2 border-b border-ink">
                    {[t("usage.tier"), t("usage.requests"), t("usage.billed"), t("adminOverview.upstreamCol")].map((h) => (
                      <th key={h} scope="col" className="label-mono px-2 py-2 text-left">
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {o.today.by_tier.map((r) => (
                    <tr key={r.tier} className="border-b border-rule">
                      <td className="px-2 py-2">
                        <TierTag tier={r.tier} />
                      </td>
                      <td className="num px-2 py-2">{formatInt(r.requests)}</td>
                      <td className="num px-2 py-2">{formatUSD(r.billed_micro)}</td>
                      <td className="num px-2 py-2">{formatUSD(r.upstream_cost_micro)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : null}
          </Section>
        </div>

        <Section
          n={4}
          title={t("adminOverview.live")}
          aside={
            <span className="flex items-center gap-1.5 text-[12px] text-muted">
              <Radio className="size-3.5" aria-hidden />
              {t("adminOverview.liveNote")}
            </span>
          }
          bodyClassName="p-0"
        >
          {live.length === 0 ? (
            <EmptyState className="m-4" title={t("adminOverview.liveEmpty")} description={t("adminOverview.liveEmptyHint")} />
          ) : (
            <div className="scroll-x" aria-live="polite">
              <table className="w-full min-w-[720px] text-[13px]" aria-label={t("adminOverview.live")}>
                <thead>
                  <tr className="border-b border-ink">
                    {[t("usage.time"), t("adminOverview.user"), t("usage.model"), t("usage.tier"), t("usage.status"), t("usage.billed"), t("adminOverview.upstreamCol"), t("usage.latency")].map((h) => (
                      <th key={h} scope="col" className="label-mono px-3 py-2 text-left">
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {live.map((r) => (
                    <tr key={r.request_id} className="border-b border-rule">
                      <td className="num px-3 py-2">{formatTime(r.created_at, true)}</td>
                      <td className="px-3 py-2">
                        <Link to="/admin/users/$userId" params={{ userId: r.user_id }} className="num underline underline-offset-4">
                          {shortId(r.user_id)}
                        </Link>
                      </td>
                      <td className="px-3 py-2 font-semibold">{r.model}</td>
                      <td className="px-3 py-2">
                        <TierTag tier={r.tier} />
                      </td>
                      <td className="px-3 py-2">
                        <StatusCode code={r.status_code} />
                      </td>
                      <td className="num px-3 py-2">{formatUSD(r.billed_micro)}</td>
                      <td className="num px-3 py-2">{formatUSD(r.upstream_cost_micro)}</td>
                      <td className="num px-3 py-2">{(r.latency_ms / 1000).toFixed(1)}s</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Section>
      </div>
    </>
  );
}
