import { createFileRoute, Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ArrowRight, Plus } from "lucide-react";
import { api, unwrap } from "@/api/client";
import { balanceQuery, keySecretQuery, keysQuery, subscriptionsQuery, usageSummaryQuery } from "@/api/queries";
import { useQueryClient } from "@tanstack/react-query";
import { PageHeader } from "@/components/PageHeader";
import { StatStrip } from "@/components/StatStrip";
import { Section } from "@/components/Section";
import { AvailabilityBlock } from "@/components/AvailabilityBlock";
import { TierStackedBars } from "@/components/Charts";
import { PlanCard } from "@/components/PlanCard";
import { TierChain, TierTag } from "@/components/Tags";
import { CopyButton, EmptyState, ErrorBox } from "@/components/Feedback";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { cacheHitRate, spendSplit } from "@/lib/availability";
import { formatDateTime, formatPercent, formatTokens, formatUSD } from "@/lib/format";
import { seriesFacts, tierSeries, useDayRange } from "@/lib/usage";

export const Route = createFileRoute("/_app/")({
  loader: ({ context: { queryClient } }) => {
    void queryClient.prefetchQuery(balanceQuery);
    void queryClient.prefetchQuery(subscriptionsQuery);
    void queryClient.prefetchQuery(keysQuery);
  },
  component: Overview,
});

function Overview() {
  const { t } = useTranslation();
  const today = useDayRange(1);
  const fortnight = useDayRange(14);
  const balance = useQuery(balanceQuery);
  const subs = useQuery(subscriptionsQuery);
  const todayUsage = useQuery(usageSummaryQuery({ granularity: "hour", from: today.from, to: today.to }));
  const history = useQuery(usageSummaryQuery({ granularity: "hour", from: fortnight.from, to: fortnight.to }));
  const recent = useQuery({
    queryKey: ["me", "usage", "logs", { limit: 6 }],
    queryFn: () => unwrap(api.GET("/usage/logs", { params: { query: { limit: 6 } } })),
  });

  const active = (subs.data?.items ?? []).filter((s) => s.status === "active");
  const planRemaining = active.reduce((a, s) => a + Math.max(0, s.current_period?.remaining_micro ?? 0), 0);
  const split = spendSplit(todayUsage.data?.items ?? []);
  const hit = cacheHitRate(todayUsage.data?.items ?? []);
  const series = tierSeries(history.data?.items ?? [], "day", fortnight.fromDate, fortnight.toDate);
  const facts = seriesFacts(series, formatUSD);
  const lowBalance = balance.data ? balance.data.available_micro < 1_000_000 : false;

  return (
    <>
      <PageHeader code="OB-01" title={t("overview.title")} />
      <div className="flex flex-col gap-6">
        <StatStrip
          loading={balance.isPending || subs.isPending || todayUsage.isPending}
          stats={[
            {
              label: t("overview.balance"),
              value: formatUSD(balance.data?.available_micro ?? 0),
              note: t("overview.held", { amount: formatUSD(balance.data?.held_micro ?? 0) }),
              alert: lowBalance,
            },
            {
              label: t("overview.planRemaining"),
              value: active.length ? formatUSD(planRemaining) : "—",
              note: active.length ? t("overview.activePlans", { count: active.length }) : t("overview.noActivePlan"),
            },
            {
              label: t("overview.todaySpend"),
              value: formatUSD(split.total),
              note: t("overview.spendSplit", { plan: formatUSD(split.plan), balance: formatUSD(split.balance) }),
            },
            {
              label: t("overview.cacheHit"),
              value: hit === null ? "—" : formatPercent(hit * 100),
              note: t("overview.cacheHitNote"),
            },
          ]}
        />
        {balance.error ? <ErrorBox error={balance.error} onRetry={() => balance.refetch()} /> : null}

        <AvailabilityBlock n={2} />

        {/* Top-aligned row: the chart keeps a fixed 280px height and is never stretched by the Plan cards. */}
        <div className="grid items-start gap-6 xl:grid-cols-[1.4fr_1fr]" data-testid="spend-row">
          <Section n={3} title={t("overview.spend14")} aside={<span className="num text-[12px] text-muted">{facts.total}</span>} id="spend14">
            {history.isPending ? (
              <Skeleton className="h-[280px]" />
            ) : history.error ? (
              <ErrorBox error={history.error} onRetry={() => history.refetch()} />
            ) : (
              <TierStackedBars height={280} data={series} description={t("charts.tierSeries", { title: t("overview.spend14"), ...facts })} />
            )}
          </Section>
          <Section n={4} title={t("overview.myPlans")} aside={<Link to="/plans" className="text-[13px] font-semibold underline underline-offset-4">{t("common.viewAll")}</Link>}>
            {subs.isPending ? (
              <Skeleton className="h-60" />
            ) : (subs.data?.items ?? []).filter((s) => s.status === "active" || s.status === "pending_assignment").length === 0 ? (
              <EmptyState
                title={t("overview.noPlanTitle")}
                description={t("overview.noPlanHint")}
                action={
                  <Button asChild variant="primary" size="sm">
                    <Link to="/plans">{t("overview.browsePlans")}</Link>
                  </Button>
                }
              />
            ) : (
              <div className="flex flex-col gap-4">
                {(subs.data?.items ?? [])
                  .filter((s) => s.status === "active" || s.status === "pending_assignment")
                  .slice(0, 2)
                  .map((s) => (
                    <PlanCard key={s.id} sub={s} />
                  ))}
              </div>
            )}
          </Section>
        </div>

        <div className="grid gap-6 xl:grid-cols-[1.4fr_1fr]">
          <Section
            n={5}
            title={t("overview.recent")}
            aside={
              <Link to="/usage" className="inline-flex items-center gap-1 text-[13px] font-semibold underline underline-offset-4">
                {t("overview.allUsage")}
                <ArrowRight className="size-3.5" aria-hidden />
              </Link>
            }
            bodyClassName="p-0"
          >
            {recent.isPending ? (
              <div className="flex flex-col gap-2 p-4">
                {Array.from({ length: 4 }, (_, i) => (
                  <Skeleton key={i} className="h-8" />
                ))}
              </div>
            ) : recent.error ? (
              <ErrorBox className="m-4" error={recent.error} onRetry={() => recent.refetch()} />
            ) : recent.data.items.length === 0 ? (
              <EmptyState className="m-4" title={t("overview.noRequests")} description={t("overview.noRequestsHint")} />
            ) : (
              <ul className="divide-y divide-rule">
                {recent.data.items.map((r) => (
                  <li key={r.request_id} className="grid grid-cols-[1fr_auto] items-center gap-x-3 gap-y-1 px-4 py-2.5 text-[13px] sm:grid-cols-[140px_1fr_auto_auto]">
                    <span className="num text-muted max-sm:order-3 max-sm:text-[12px]">{formatDateTime(r.created_at)}</span>
                    <span className="min-w-0 truncate font-semibold max-sm:order-1">{r.model}</span>
                    <span className="flex items-center justify-end gap-2 max-sm:order-4">
                      <TierTag tier={r.tier} />
                      <span className="num text-muted">{formatTokens(r.input_tokens + r.cache_read_tokens + r.cache_write_tokens + r.output_tokens)} tok</span>
                    </span>
                    <span className={r.status_code === 200 ? "num text-right font-medium max-sm:order-2" : "num text-right max-sm:order-2"}>
                      {r.status_code === 200 ? formatUSD(r.billed_micro) : <span className="bg-red px-1 text-on-red">{r.status_code}</span>}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Section>
          <MyKeys />
        </div>
      </div>
    </>
  );
}

function MyKeys() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery(keysQuery);
  return (
    <Section
      n={6}
      title={t("overview.myKeys")}
      aside={
        <Button asChild size="sm">
          <Link to="/keys" search={{ new: true }}>
            <Plus aria-hidden />
            {t("keys.new")}
          </Link>
        </Button>
      }
      bodyClassName="p-0"
    >
      {q.isPending ? (
        <div className="p-4">
          <Skeleton className="h-16" />
        </div>
      ) : q.error ? (
        <ErrorBox className="m-4" error={q.error} onRetry={() => q.refetch()} />
      ) : q.data.items.length === 0 ? (
        <EmptyState className="m-4" title={t("keys.emptyTitle")} description={t("keys.emptyHint")} />
      ) : (
        <ul className="divide-y divide-rule">
          {q.data.items.map((k) => (
            <li key={k.id} className="flex items-center gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate font-semibold">{k.name || t("keys.unnamed")}</span>
                  {!k.enabled ? <span className="label-mono border border-rule px-1 text-muted">{t("keys.disabled")}</span> : null}
                </div>
                <div className="num mt-0.5 text-[12px] text-muted">{k.prefix}</div>
                <TierChain tiers={k.tiers} className="mt-1.5" />
              </div>
              <CopyButton label={t("keys.copy")} value={async () => (await qc.fetchQuery(keySecretQuery(k.id))).key} />
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}
