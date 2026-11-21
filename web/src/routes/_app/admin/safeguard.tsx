import { useMemo, useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { Schemas } from "@/api/client";
import { safeguardInfinite } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { DataTable, RowCard, columnHelper } from "@/components/DataTable";
import { EmptyState } from "@/components/Feedback";
import { Input } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { formatDateTime } from "@/lib/format";
import { useDebounced } from "@/lib/hooks";
import { shortId } from "@/lib/utils";

export const Route = createFileRoute("/_app/admin/safeguard")({
  component: Safeguard,
});

type Ev = Schemas["SafeguardEvent"];

function Safeguard() {
  const { t } = useTranslation();
  const [user, setUser] = useState("");
  const debounced = useDebounced(user.trim(), 400);
  const uid = /^[0-9a-f-]{36}$/i.test(debounced) ? debounced : undefined;
  const q = useInfiniteQuery({ ...safeguardInfinite({ user_id: uid, limit: 100 }), placeholderData: keepPreviousData });
  const rows = useMemo(() => q.data?.pages.flatMap((p) => p.items) ?? [], [q.data]);
  const col = columnHelper<Ev>();
  const columns = [
    col.accessor("created_at", { header: t("billing.time"), cell: (c) => <span className="num text-[12px]">{formatDateTime(c.getValue())}</span> }),
    col.accessor("user_id", {
      header: t("adminOverview.user"),
      cell: (c) =>
        c.getValue() ? (
          <Link to="/admin/users/$userId" params={{ userId: c.getValue()! }} className="num text-[12px] underline underline-offset-4">
            {shortId(c.getValue()!)}
          </Link>
        ) : (
          "—"
        ),
    }),
    col.accessor("model", { header: t("usage.model"), cell: (c) => <span className="font-semibold">{c.getValue()}</span> }),
    col.accessor("category", { header: t("safeguard.category"), cell: (c) => <span className="label-mono bg-red px-1.5 py-0.5 text-on-red">{c.getValue() || "—"}</span> }),
    col.accessor("reason", { header: t("safeguard.reason"), cell: (c) => <span className="block max-w-[420px] truncate text-[12px]" title={c.getValue()}>{c.getValue()}</span> }),
    col.accessor("request_id", { header: t("usage.requestId"), cell: (c) => <span className="num text-[12px] text-muted">{shortId(c.getValue())}</span> }),
  ];
  return (
    <>
      <PageHeader code="OB-15" title={t("safeguard.title")} />
      <div className="flex flex-col gap-6">
        <div className="grid gap-3 border-2 border-ink p-4 sm:grid-cols-2">
          <Field label={t("logs.userId")} htmlFor="sg-user">
            <Input id="sg-user" className="num" placeholder="uuid" value={user} onChange={(e) => setUser(e.target.value)} />
          </Field>
        </div>
        <Section n={1} title={t("safeguard.list")} bodyClassName="p-0 sm:p-4">
          <DataTable
            label={t("safeguard.list")}
            columns={columns}
            data={rows}
            rowKey={(r) => r.id}
            loading={q.isPending}
            error={q.error}
            onRetry={() => q.refetch()}
            hasMore={q.hasNextPage}
            fetchingMore={q.isFetchingNextPage}
            onLoadMore={() => void q.fetchNextPage()}
            className="max-sm:p-4"
            empty={<EmptyState className="m-4 sm:m-0" title={t("safeguard.empty")} />}
            renderCard={(r) => (
              <RowCard
                title={r.model}
                aside={<span className="label-mono bg-red px-1.5 py-0.5 text-on-red">{r.category}</span>}
                lines={[
                  [t("billing.time"), <span className="num">{formatDateTime(r.created_at)}</span>],
                  [t("safeguard.reason"), r.reason],
                ]}
              />
            )}
          />
        </Section>
      </div>
    </>
  );
}
