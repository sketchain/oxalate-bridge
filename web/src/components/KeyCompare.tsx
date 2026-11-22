import { useMemo } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { Schemas } from "@/api/client";
import { keyCompareQuery } from "@/api/queries";
import { TimeLines, type LineSeries } from "./Charts";
import { DataTable, RowCard, columnHelper } from "./DataTable";
import { EmptyState, ErrorBox } from "./Feedback";
import { Section } from "./Section";
import { Skeleton } from "./ui/skeleton";
import { formatInt, formatTokens, formatUSD } from "@/lib/format";

type Total = Schemas["KeyUsageTotal"];

const COLORS: LineSeries["color"][] = ["ink", "chart", "yellow"];

/** Daily series rows ({t, <keyId>: billed}) for the top keys, zero-filled. */
export function compareRows(top: Schemas["KeySeries"][]): { t: number; [k: string]: number }[] {
  const days = new Set<string>();
  for (const s of top) for (const p of s.points) days.add(p.day);
  return [...days].sort().map((day) => {
    const [y, m, d] = day.split("-").map(Number);
    const row: { t: number; [k: string]: number } = { t: new Date(y ?? 1970, (m ?? 1) - 1, d ?? 1).getTime() };
    for (const s of top) row[s.key_id] = s.points.find((p) => p.day === day)?.billed_micro ?? 0;
    return row;
  });
}

/** "按 Key 对比": totals per key and the daily billed lines of the top 10. */
export function KeyCompare({ from, to }: { from: string; to: string }) {
  const { t } = useTranslation();
  const q = useQuery({ ...keyCompareQuery({ from, to }), placeholderData: keepPreviousData });
  const items = useMemo(() => q.data?.items ?? [], [q.data]);
  const total = items.reduce((a, b) => a + b.billed_micro, 0);
  const label = (k: Pick<Total, "name" | "prefix" | "deleted">) => `${k.name || k.prefix}${k.deleted ? ` (${t("usage.compare.deleted")})` : ""}`;
  const series: LineSeries[] = (q.data?.top ?? []).map((s, i) => {
    const k = items.find((x) => x.key_id === s.key_id);
    return { key: s.key_id, name: k ? label(k) : s.key_id.slice(0, 8), color: COLORS[i % 3] ?? "ink", dashed: i >= 3 };
  });
  const rows = useMemo(() => compareRows(q.data?.top ?? []), [q.data]);

  const col = columnHelper<Total>();
  const columns = [
    col.display({ id: "name", header: t("usage.key"), cell: (c) => <span className="font-semibold">{label(c.row.original)}</span> }),
    col.accessor("requests", { header: t("usage.requests"), cell: (c) => <span className="num">{formatInt(c.getValue())}</span> }),
    col.display({
      id: "tokens",
      header: t("usage.tokens"),
      cell: (c) => {
        const r = c.row.original;
        return <span className="num text-[12px]">{formatTokens(r.input_tokens + r.cache_read_tokens + r.cache_write_tokens)} / {formatTokens(r.output_tokens)}</span>;
      },
    }),
    col.accessor("billed_micro", { header: t("usage.billed"), cell: (c) => <span className="num font-medium">{formatUSD(c.getValue())}</span> }),
    col.display({
      id: "share",
      header: t("usage.compare.share"),
      cell: (c) => <span className="num text-[12px]">{total > 0 ? `${((c.row.original.billed_micro / total) * 100).toFixed(1)}%` : "—"}</span>,
    }),
  ];

  return (
    <div className="flex flex-col gap-6" data-testid="key-compare">
      <Section n={1} title={t("usage.compare.chart")}>
        {q.isPending ? (
          <Skeleton className="h-64" />
        ) : q.error ? (
          <ErrorBox error={q.error} onRetry={() => q.refetch()} />
        ) : rows.length === 0 ? (
          <EmptyState title={t("usage.noData")} description={t("usage.noDataHint")} />
        ) : (
          <TimeLines
            data={rows}
            series={series}
            dateOnly
            height={300}
            yFormat={formatUSD}
            description={t("usage.compare.chartDesc", { list: items.slice(0, 10).map((k) => `${label(k)} ${formatUSD(k.billed_micro)}`).join("；") })}
          />
        )}
      </Section>
      <Section n={2} title={t("usage.compare.table")} aside={<span className="num text-[12px] text-muted">{formatUSD(total)}</span>} bodyClassName="p-0 sm:p-4">
        <DataTable
          label={t("usage.compare.table")}
          columns={columns}
          data={items}
          rowKey={(k) => k.key_id}
          loading={q.isPending}
          error={q.error}
          onRetry={() => q.refetch()}
          empty={<EmptyState className="m-4 sm:m-0" title={t("usage.noData")} description={t("usage.noDataHint")} />}
          className="max-sm:p-4"
          renderCard={(k) => (
            <RowCard
              title={label(k)}
              aside={<span className="num font-semibold">{formatUSD(k.billed_micro)}</span>}
              lines={[
                [t("usage.requests"), <span className="num">{formatInt(k.requests)}</span>],
                [t("usage.compare.share"), <span className="num">{total > 0 ? `${((k.billed_micro / total) * 100).toFixed(1)}%` : "—"}</span>],
              ]}
            />
          )}
        />
      </Section>
    </div>
  );
}
