import { useMemo } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { Schemas } from "@/api/client";
import { balanceQuery, ledgerInfinite } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { StatStrip } from "@/components/StatStrip";
import { DataTable, RowCard, columnHelper } from "@/components/DataTable";
import { EmptyState } from "@/components/Feedback";
import { NativeSelect } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { formatDateTime, formatUSD } from "@/lib/format";
import { LEDGER_TYPES } from "@/lib/constants";

export const Route = createFileRoute("/_app/billing")({
  validateSearch: (s: Record<string, unknown>): { type?: Schemas["LedgerType"] } => ({
    type: LEDGER_TYPES.includes(s.type as Schemas["LedgerType"]) ? (s.type as Schemas["LedgerType"]) : undefined,
  }),
  component: BillingPage,
});

type Entry = Schemas["LedgerEntry"];

function BillingPage() {
  const { t } = useTranslation();
  const { type } = Route.useSearch();
  const navigate = useNavigate({ from: "/billing" });
  const balance = useQuery(balanceQuery);
  const ledger = useInfiniteQuery({ ...ledgerInfinite({ type, limit: 100 }), placeholderData: keepPreviousData });
  const rows = useMemo(() => ledger.data?.pages.flatMap((p) => p.items) ?? [], [ledger.data]);
  const b = balance.data;

  const col = columnHelper<Entry>();
  const columns = [
    col.accessor("created_at", { header: t("billing.time"), cell: (c) => <span className="num text-[12px]">{formatDateTime(c.getValue())}</span> }),
    col.accessor("type", { header: t("billing.type"), cell: (c) => <span className="font-semibold">{t(`ledgerType.${c.getValue()}`)}</span> }),
    col.accessor("amount_micro", {
      header: t("billing.amount"),
      cell: (c) => <span className={c.getValue() >= 0 ? "num font-semibold" : "num"}>{(c.getValue() > 0 ? "+" : "") + formatUSD(c.getValue())}</span>,
    }),
    col.accessor("balance_after_micro", { header: t("billing.balanceAfter"), cell: (c) => <span className="num">{formatUSD(c.getValue())}</span> }),
    col.accessor("note", { header: t("billing.note"), cell: (c) => <span className="text-[12px] text-muted">{c.getValue() || "—"}</span> }),
  ];

  return (
    <>
      <PageHeader code="OB-05" title={t("billing.title")} />
      <div className="flex flex-col gap-6">
        <StatStrip
          loading={balance.isPending}
          stats={[
            { label: t("billing.available"), value: formatUSD(b?.available_micro ?? 0), note: t("billing.availableNote") },
            { label: t("billing.persisted"), value: formatUSD(b?.balance_micro ?? 0), note: t("billing.persistedNote") },
            { label: t("billing.held"), value: formatUSD(b?.held_micro ?? 0), note: t("billing.heldNote") },
            { label: t("billing.pending"), value: formatUSD(b?.pending_micro ?? 0), note: t("billing.pendingNote") },
          ]}
        />
        <Section
          n={1}
          title={t("billing.ledger")}
          aside={
            <div className="flex items-center gap-2">
              <Label htmlFor="ledger-type" className="text-muted">
                {t("billing.type")}
              </Label>
              <NativeSelect
                id="ledger-type"
                className="h-8 w-40"
                value={type ?? ""}
                onChange={(e) => navigate({ search: { type: (e.target.value || undefined) as Schemas["LedgerType"] | undefined }, replace: true })}
              >
                <option value="">{t("common.all")}</option>
                {LEDGER_TYPES.map((x) => (
                  <option key={x} value={x}>
                    {t(`ledgerType.${x}`)}
                  </option>
                ))}
              </NativeSelect>
            </div>
          }
          bodyClassName="p-0 sm:p-4"
        >
          <DataTable
            label={t("billing.ledger")}
            columns={columns}
            data={rows}
            rowKey={(e) => e.id}
            loading={ledger.isPending}
            error={ledger.error}
            onRetry={() => ledger.refetch()}
            hasMore={ledger.hasNextPage}
            fetchingMore={ledger.isFetchingNextPage}
            onLoadMore={() => void ledger.fetchNextPage()}
            empty={<EmptyState className="m-4 sm:m-0" title={t("billing.empty")} description={t("billing.emptyHint")} />}
            className="max-sm:p-4"
            renderCard={(e) => (
              <RowCard
                title={t(`ledgerType.${e.type}`)}
                aside={<span className="num font-semibold">{(e.amount_micro > 0 ? "+" : "") + formatUSD(e.amount_micro)}</span>}
                lines={[
                  [t("billing.time"), <span className="num">{formatDateTime(e.created_at)}</span>],
                  [t("billing.balanceAfter"), <span className="num">{formatUSD(e.balance_after_micro)}</span>],
                  [t("billing.note"), e.note || "—"],
                ]}
              />
            )}
          />
        </Section>
      </div>
    </>
  );
}
