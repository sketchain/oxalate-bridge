import { useMemo, useState, type ReactNode } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Dialog } from "radix-ui";
import type { Schemas, Tier } from "@/api/client";
import { keysQuery, modelsQuery, usageLogsInfinite, usageSummaryQuery } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { DataTable, RowCard, columnHelper } from "@/components/DataTable";
import { CategoryBars, TierStackedBars } from "@/components/Charts";
import { RangeFilter, resolveRange, type RangePreset, type RangeValue } from "@/components/RangeFilter";
import { StatusCode, TierTag } from "@/components/Tags";
import { EmptyState, ErrorBox } from "@/components/Feedback";
import { SheetContent } from "@/components/ui/dialog";
import { NativeSelect } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { formatDateTime, formatInt, formatTokens, formatUSD } from "@/lib/format";
import { cacheHitRate } from "@/lib/availability";
import { modelSeries, seriesFacts, tierSeries } from "@/lib/usage";
import { cn } from "@/lib/utils";

interface Search extends Partial<RangeValue> {
  key?: string;
  model?: string;
  tier?: Tier;
  gran?: "day" | "hour";
}

const TIERS: Tier[] = ["plan", "standard", "force"];

export const Route = createFileRoute("/_app/usage")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    range: (["today", "30d", "custom"] as const).includes(s.range as "today") ? (s.range as RangePreset) : undefined,
    from: typeof s.from === "string" ? s.from : undefined,
    to: typeof s.to === "string" ? s.to : undefined,
    key: typeof s.key === "string" && s.key ? s.key : undefined,
    model: typeof s.model === "string" && s.model ? s.model : undefined,
    tier: TIERS.includes(s.tier as Tier) ? (s.tier as Tier) : undefined,
    gran: s.gran === "hour" ? "hour" : s.gran === "day" ? "day" : undefined,
  }),
  component: UsagePage,
});

type Log = Schemas["MyRequestLog"];

function UsagePage() {
  const { t } = useTranslation();
  const raw = Route.useSearch();
  const search = useMemo(() => ({ ...raw, range: raw.range ?? ("7d" as RangePreset) }), [raw]);
  const navigate = useNavigate({ from: "/usage" });
  const set = (patch: Partial<Search>) => navigate({ search: (s) => ({ ...s, ...patch }), replace: true });
  const keyList = useQuery(keysQuery);
  const models = useQuery(modelsQuery);
  const [open, setOpen] = useState<Log | null>(null);

  const { from, to } = useMemo(() => resolveRange(search), [search]);
  const days = Math.round((to.getTime() - from.getTime()) / 86_400_000);
  const gran = search.gran ?? (days <= 1 ? "hour" : "day");
  // Hourly rows are re-bucketed into local days; very long ranges use the API's (UTC) days.
  const apiGran = days > 62 ? "day" : "hour";
  const fromIso = from.toISOString();
  const toIso = to.toISOString();
  const summary = useQuery({
    ...usageSummaryQuery({ granularity: apiGran, from: fromIso, to: toIso, key_id: search.key }),
    placeholderData: keepPreviousData,
  });
  const items = useMemo(
    () => (summary.data?.items ?? []).filter((b) => (!search.model || b.model === search.model) && (!search.tier || b.tier === search.tier)),
    [summary.data, search.model, search.tier],
  );
  const series = tierSeries(items, gran, from, to);
  const facts = seriesFacts(series, formatUSD);
  const byModel = modelSeries(items);
  const totals = items.reduce(
    (a, b) => ({ requests: a.requests + b.requests, billed: a.billed + b.billed_micro, tokens: a.tokens + b.input_tokens + b.output_tokens + b.cache_read_tokens + b.cache_write_tokens }),
    { requests: 0, billed: 0, tokens: 0 },
  );
  const hit = cacheHitRate(items);

  const logs = useInfiniteQuery({
    ...usageLogsInfinite({ from: fromIso, to: toIso, key_id: search.key, model: search.model, tier: search.tier, limit: 100 }),
    placeholderData: keepPreviousData,
  });
  const rows = useMemo(() => logs.data?.pages.flatMap((p) => p.items) ?? [], [logs.data]);
  const keyName = (id?: string | null) => keyList.data?.items.find((k) => k.id === id)?.name || "—";

  const col = columnHelper<Log>();
  const columns = [
    col.accessor("created_at", { header: t("usage.time"), cell: (c) => <span className="num text-[12px]">{formatDateTime(c.getValue())}</span> }),
    col.accessor("model", { header: t("usage.model"), cell: (c) => <span className="font-semibold">{c.getValue()}</span> }),
    col.accessor("tier", { header: t("usage.tier"), cell: (c) => <TierTag tier={c.getValue()} /> }),
    col.display({ id: "key", header: t("usage.key"), cell: (c) => <span className="text-[12px]">{keyName(c.row.original.key_id)}</span> }),
    col.display({
      id: "tokens",
      header: t("usage.tokens"),
      cell: (c) => {
        const r = c.row.original;
        return (
          <span className="num text-[12px]">
            {formatTokens(r.input_tokens + r.cache_read_tokens + r.cache_write_tokens)} / {formatTokens(r.output_tokens)}
          </span>
        );
      },
    }),
    col.accessor("billed_micro", { header: t("usage.billed"), cell: (c) => <span className="num font-medium">{formatUSD(c.getValue())}</span> }),
    col.accessor("latency_ms", { header: t("usage.latency"), cell: (c) => <span className="num text-[12px]">{(c.getValue() / 1000).toFixed(1)}s</span> }),
    col.accessor("status_code", { header: t("usage.status"), cell: (c) => <StatusCode code={c.getValue()} /> }),
  ];

  return (
    <>
      <PageHeader code="OB-03" title={t("usage.title")} />
      <div className="flex flex-col gap-6">
        <div className="grid gap-4 border-2 border-ink p-4 md:grid-cols-[auto_1fr_1fr_1fr] md:items-end">
          <RangeFilter value={search} onChange={(v) => set({ range: v.range === "7d" ? undefined : v.range, from: v.from, to: v.to })} />
          <Field label={t("usage.key")} htmlFor="f-key">
            <NativeSelect id="f-key" value={search.key ?? ""} onChange={(e) => set({ key: e.target.value || undefined })}>
              <option value="">{t("common.all")}</option>
              {keyList.data?.items.map((k) => (
                <option key={k.id} value={k.id}>
                  {k.name || k.prefix}
                </option>
              ))}
            </NativeSelect>
          </Field>
          <Field label={t("usage.model")} htmlFor="f-model">
            <NativeSelect id="f-model" value={search.model ?? ""} onChange={(e) => set({ model: e.target.value || undefined })}>
              <option value="">{t("common.all")}</option>
              {[...new Set([...(models.data?.items ?? []), ...(search.model ? [search.model] : [])])].map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </NativeSelect>
          </Field>
          <Field label={t("usage.tier")} htmlFor="f-tier">
            <NativeSelect id="f-tier" value={search.tier ?? ""} onChange={(e) => set({ tier: (e.target.value || undefined) as Tier | undefined })}>
              <option value="">{t("common.all")}</option>
              {TIERS.map((x) => (
                <option key={x} value={x}>
                  {t(`tier.${x}`)}
                </option>
              ))}
            </NativeSelect>
          </Field>
        </div>

        <div className="grid gap-6 xl:grid-cols-[1.6fr_1fr]">
          <Section
            n={1}
            title={t("usage.summary")}
            aside={
              <div role="group" aria-label={t("usage.granularity")} className="flex border-2 border-ink">
                {(["day", "hour"] as const).map((g) => (
                  <button
                    key={g}
                    type="button"
                    aria-pressed={gran === g}
                    onClick={() => set({ gran: g })}
                    className={cn("h-7 border-l border-ink px-2.5 text-[12px] font-semibold first:border-l-0", gran === g ? "bg-yellow text-on-yellow" : "hover:bg-rule")}
                  >
                    {t(`usage.by.${g}`)}
                  </button>
                ))}
              </div>
            }
          >
            <dl className="num mb-4 grid grid-cols-2 gap-2 text-[12px] sm:grid-cols-4">
              {[
                [t("usage.requests"), formatInt(totals.requests)],
                [t("usage.billed"), formatUSD(totals.billed)],
                [t("usage.tokens"), formatTokens(totals.tokens)],
                [t("overview.cacheHit"), hit === null ? "—" : `${(hit * 100).toFixed(1)}%`],
              ].map(([k, v]) => (
                <div key={k} className="border border-ink px-2 py-1.5">
                  <dt className="label-mono text-muted">{k}</dt>
                  <dd className="text-[16px]">{v}</dd>
                </div>
              ))}
            </dl>
            {summary.isPending ? (
              <Skeleton className="h-60" />
            ) : summary.error ? (
              <ErrorBox error={summary.error} onRetry={() => summary.refetch()} />
            ) : (
              <TierStackedBars data={series} description={t("charts.tierSeries", { title: t("usage.summary"), ...facts })} height={260} />
            )}
          </Section>
          <Section n={2} title={t("usage.byModel")}>
            {summary.isPending ? (
              <Skeleton className="h-48" />
            ) : byModel.length === 0 ? (
              <EmptyState title={t("usage.noData")} description={t("usage.noDataHint")} />
            ) : (
              <CategoryBars
                data={byModel}
                description={t("charts.byModel", { list: byModel.map((m) => `${m.name} ${formatUSD(m.value)}`).join("；") })}
              />
            )}
          </Section>
        </div>

        <Section n={3} title={t("usage.details")} bodyClassName="p-0 sm:p-4">
          <DataTable
            label={t("usage.details")}
            columns={columns}
            data={rows}
            rowKey={(r) => r.request_id}
            loading={logs.isPending}
            error={logs.error}
            onRetry={() => logs.refetch()}
            hasMore={logs.hasNextPage}
            fetchingMore={logs.isFetchingNextPage}
            onLoadMore={() => void logs.fetchNextPage()}
            onRowClick={setOpen}
            virtualizeFrom={40}
            empty={<EmptyState className="m-4 sm:m-0" title={t("usage.noData")} description={t("usage.noDataHint")} />}
            renderCard={(r) => (
              <RowCard
                onClick={() => setOpen(r)}
                title={r.model}
                aside={<TierTag tier={r.tier} />}
                lines={[
                  [t("usage.time"), <span className="num">{formatDateTime(r.created_at)}</span>],
                  [t("usage.billed"), <span className="num">{formatUSD(r.billed_micro)}</span>],
                  [t("usage.status"), <StatusCode code={r.status_code} />],
                ]}
              />
            )}
            className="max-sm:p-4"
          />
        </Section>
      </div>

      <Dialog.Root open={!!open} onOpenChange={(o) => !o && setOpen(null)}>
        {open ? (
          <SheetContent title={t("usage.detailTitle")}>
            <LogDetail log={open} keyName={keyName(open.key_id)} />
          </SheetContent>
        ) : null}
      </Dialog.Root>
    </>
  );
}

function LogDetail({ log, keyName }: { log: Log; keyName: string }) {
  const { t } = useTranslation();
  const rows: [string, ReactNode][] = [
    [t("usage.time"), formatDateTime(log.created_at)],
    [t("usage.requestId"), <span className="break-all">{log.request_id}</span>],
    [t("usage.model"), log.model_raw === log.model ? log.model : `${log.model} (${log.model_raw})`],
    [t("usage.tier"), <TierTag tier={log.tier} />],
    [t("usage.key"), keyName],
    [t("usage.stream"), log.stream ? t("common.yes") : t("common.no")],
    [t("usage.status"), <StatusCode code={log.status_code} />],
  ];
  if (log.error_type) rows.push([t("usage.errorType"), log.error_type]);
  const tokens: [string, number][] = [
    [t("usage.inputTokens"), log.input_tokens],
    [t("usage.cacheRead"), log.cache_read_tokens],
    [t("usage.cacheWrite"), log.cache_write_tokens],
    [t("usage.outputTokens"), log.output_tokens],
    [t("usage.reasoningTokens"), log.reasoning_tokens],
  ];
  return (
    <div className="flex flex-col gap-5 p-5">
      <div className="border-2 border-ink">
        <div className="label-mono bg-yellow px-3 py-1.5 text-on-yellow">{t("usage.billed")}</div>
        <div className="num px-3 py-3 text-[30px]">{formatUSD(log.billed_micro)}</div>
        {log.unpriced || log.usage_missing ? (
          <p className="border-t border-ink px-3 py-2 text-[12px]">{log.unpriced ? t("usage.unpriced") : t("usage.usageMissing")}</p>
        ) : null}
      </div>
      <dl className="divide-y divide-rule border-y-2 border-ink text-[13px]">
        {rows.map(([k, v]) => (
          <div key={k} className="grid grid-cols-[120px_1fr] gap-3 py-2">
            <dt className="label-mono self-center text-muted">{k}</dt>
            <dd className="num min-w-0">{v}</dd>
          </div>
        ))}
      </dl>
      <div>
        <h3 className="label-mono mb-2">{t("usage.tokenBreakdown")}</h3>
        <dl className="grid grid-cols-2 border border-ink text-[13px]">
          {tokens.map(([k, v], i) => (
            <div key={k} className={cn("flex flex-col gap-0.5 border-ink p-2", i % 2 === 1 && "border-l", i >= 2 && "border-t")}>
              <dt className="label-mono text-muted">{k}</dt>
              <dd className="num text-[16px]" title={formatInt(v)}>
                {formatTokens(v)}
              </dd>
            </div>
          ))}
        </dl>
      </div>
      <dl className="grid grid-cols-2 border border-ink text-[13px]">
        <div className="flex flex-col gap-0.5 p-2">
          <dt className="label-mono text-muted">{t("usage.latency")}</dt>
          <dd className="num text-[16px]">{(log.latency_ms / 1000).toFixed(2)}s</dd>
        </div>
        <div className="flex flex-col gap-0.5 border-l border-ink p-2">
          <dt className="label-mono text-muted">{t("usage.firstByte")}</dt>
          <dd className="num text-[16px]">{(log.first_byte_ms / 1000).toFixed(2)}s</dd>
        </div>
      </dl>
      {log.user_agent ? <p className="num text-[11px] break-all text-muted">{log.user_agent}</p> : null}
    </div>
  );
}
