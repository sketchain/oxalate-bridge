import { useMemo, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { Schemas } from "@/api/client";
import { adminLedgerInfinite } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { DataTable, RowCard, columnHelper } from "@/components/DataTable";
import { EmptyState } from "@/components/Feedback";
import { Input, NativeSelect } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { LEDGER_TYPES } from "@/lib/constants";
import { formatDateTime, formatUSD } from "@/lib/format";
import { useDebounced } from "@/lib/hooks";
import { shortId } from "@/lib/utils";

const uuid = (v: unknown) => (typeof v === "string" && /^[0-9a-f-]{36}$/i.test(v) ? v : undefined);

export const Route = createFileRoute("/_app/admin/ledger")({
  validateSearch: (s: Record<string, unknown>): { user?: string; type?: Schemas["LedgerType"] } => ({
    user: uuid(s.user),
    type: LEDGER_TYPES.includes(s.type as Schemas["LedgerType"]) ? (s.type as Schemas["LedgerType"]) : undefined,
  }),
  component: AdminLedger,
});

type Entry = Schemas["LedgerEntry"];

function AdminLedger() {
  const { t } = useTranslation();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: "/admin/ledger" });
  const [user, setUser] = useState(search.user ?? "");
  const debounced = useDebounced(user.trim(), 400);
  const q = useInfiniteQuery({ ...adminLedgerInfinite({ user_id: uuid(debounced), type: search.type, limit: 100 }), placeholderData: keepPreviousData });
  const rows = useMemo(() => q.data?.pages.flatMap((p) => p.items) ?? [], [q.data]);
  const col = columnHelper<Entry>();
  const columns = [
    col.accessor("created_at", { header: t("billing.time"), cell: (c) => <span className="num text-[12px]">{formatDateTime(c.getValue())}</span> }),
    col.accessor("user_id", {
      header: t("adminOverview.user"),
      cell: (c) => (
        <Link to="/admin/users/$userId" params={{ userId: c.getValue() }} className="num text-[12px] underline underline-offset-4">
          {shortId(c.getValue())}
        </Link>
      ),
    }),
    col.accessor("type", { header: t("billing.type"), cell: (c) => <span className="font-semibold">{t(`ledgerType.${c.getValue()}`)}</span> }),
    col.accessor("amount_micro", { header: t("billing.amount"), cell: (c) => <span className="num">{(c.getValue() > 0 ? "+" : "") + formatUSD(c.getValue())}</span> }),
    col.accessor("balance_after_micro", { header: t("billing.balanceAfter"), cell: (c) => <span className="num">{formatUSD(c.getValue())}</span> }),
    col.display({ id: "ref", header: t("ledger.ref"), cell: (c) => <span className="num text-[12px] text-muted">{c.row.original.ref_type ? `${c.row.original.ref_type}:${c.row.original.ref_id.slice(0, 12)}` : "—"}</span> }),
    col.accessor("operator_id", { header: t("ledger.operator"), cell: (c) => <span className="num text-[12px]">{c.getValue() ? shortId(c.getValue()!) : "—"}</span> }),
    col.accessor("note", { header: t("billing.note"), cell: (c) => <span className="block max-w-[220px] truncate text-[12px] text-muted">{c.getValue() || "—"}</span> }),
  ];
  return (
    <>
      <PageHeader code="OB-13" title={t("ledger.title")} />
      <div className="flex flex-col gap-6">
        <div className="grid gap-3 border-2 border-ink p-4 sm:grid-cols-2">
          <Field label={t("logs.userId")} htmlFor="ld-user">
            <Input id="ld-user" className="num" placeholder="uuid" value={user} onChange={(e) => setUser(e.target.value)} />
          </Field>
          <Field label={t("billing.type")} htmlFor="ld-type">
            <NativeSelect id="ld-type" value={search.type ?? ""} onChange={(e) => navigate({ search: (s) => ({ ...s, type: (e.target.value || undefined) as Schemas["LedgerType"] | undefined }), replace: true })}>
              <option value="">{t("common.all")}</option>
              {LEDGER_TYPES.map((x) => (
                <option key={x} value={x}>
                  {t(`ledgerType.${x}`)}
                </option>
              ))}
            </NativeSelect>
          </Field>
        </div>
        <Section n={1} title={t("ledger.list")} bodyClassName="p-0 sm:p-4">
          <DataTable
            label={t("ledger.list")}
            columns={columns}
            data={rows}
            rowKey={(e) => e.id}
            loading={q.isPending}
            error={q.error}
            onRetry={() => q.refetch()}
            hasMore={q.hasNextPage}
            fetchingMore={q.isFetchingNextPage}
            onLoadMore={() => void q.fetchNextPage()}
            className="max-sm:p-4"
            empty={<EmptyState className="m-4 sm:m-0" title={t("billing.empty")} />}
            renderCard={(e) => (
              <RowCard
                title={t(`ledgerType.${e.type}`)}
                aside={<span className="num">{(e.amount_micro > 0 ? "+" : "") + formatUSD(e.amount_micro)}</span>}
                lines={[
                  [t("billing.time"), <span className="num">{formatDateTime(e.created_at)}</span>],
                  [t("adminOverview.user"), <span className="num">{shortId(e.user_id)}</span>],
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
