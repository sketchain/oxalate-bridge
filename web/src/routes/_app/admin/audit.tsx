import { useMemo, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Dialog } from "radix-ui";
import type { Schemas } from "@/api/client";
import { auditInfinite } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { DataTable, RowCard, columnHelper } from "@/components/DataTable";
import { EmptyState } from "@/components/Feedback";
import { SheetContent } from "@/components/ui/dialog";
import { Input, NativeSelect } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { formatDateTime } from "@/lib/format";
import { useDebounced } from "@/lib/hooks";
import { shortId } from "@/lib/utils";

export const Route = createFileRoute("/_app/admin/audit")({
  component: AuditLogs,
});

type Log = Schemas["AuditLog"];
const TARGETS = ["user", "account", "plan", "subscription", "settings", "api_key", "prices", "reconcile"];

function AuditLogs() {
  const { t } = useTranslation();
  const [operator, setOperator] = useState("");
  const [target, setTarget] = useState("");
  const debounced = useDebounced(operator.trim(), 400);
  const op = /^[0-9a-f-]{36}$/i.test(debounced) ? debounced : undefined;
  const q = useInfiniteQuery({ ...auditInfinite({ operator_id: op, target_type: target || undefined, limit: 100 }), placeholderData: keepPreviousData });
  const rows = useMemo(() => q.data?.pages.flatMap((p) => p.items) ?? [], [q.data]);
  const [open, setOpen] = useState<Log | null>(null);
  const col = columnHelper<Log>();
  const columns = [
    col.accessor("created_at", { header: t("billing.time"), cell: (c) => <span className="num text-[12px]">{formatDateTime(c.getValue())}</span> }),
    col.accessor("action", { header: t("audit.action"), cell: (c) => <span className="num font-semibold">{c.getValue()}</span> }),
    col.display({ id: "target", header: t("audit.target"), cell: (c) => <span className="num text-[12px]">{c.row.original.target_type}{c.row.original.target_id ? `:${c.row.original.target_id.slice(0, 18)}` : ""}</span> }),
    col.accessor("operator_id", { header: t("ledger.operator"), cell: (c) => <span className="num text-[12px]">{c.getValue() ? shortId(c.getValue()!) : "—"}</span> }),
    col.accessor("ip", { header: "IP", cell: (c) => <span className="num text-[12px] text-muted">{c.getValue() || "—"}</span> }),
  ];
  return (
    <>
      <PageHeader code="OB-14" title={t("audit.title")} />
      <div className="flex flex-col gap-6">
        <div className="grid gap-3 border-2 border-ink p-4 sm:grid-cols-2">
          <Field label={t("audit.operatorId")} htmlFor="au-op">
            <Input id="au-op" className="num" placeholder="uuid" value={operator} onChange={(e) => setOperator(e.target.value)} />
          </Field>
          <Field label={t("audit.targetType")} htmlFor="au-target">
            <NativeSelect id="au-target" value={target} onChange={(e) => setTarget(e.target.value)}>
              <option value="">{t("common.all")}</option>
              {TARGETS.map((x) => (
                <option key={x} value={x}>
                  {x}
                </option>
              ))}
            </NativeSelect>
          </Field>
        </div>
        <Section n={1} title={t("audit.list")} bodyClassName="p-0 sm:p-4">
          <DataTable
            label={t("audit.list")}
            columns={columns}
            data={rows}
            rowKey={(r) => r.id}
            loading={q.isPending}
            error={q.error}
            onRetry={() => q.refetch()}
            hasMore={q.hasNextPage}
            fetchingMore={q.isFetchingNextPage}
            onLoadMore={() => void q.fetchNextPage()}
            onRowClick={setOpen}
            className="max-sm:p-4"
            empty={<EmptyState className="m-4 sm:m-0" title={t("audit.empty")} />}
            renderCard={(r) => (
              <RowCard
                onClick={() => setOpen(r)}
                title={r.action}
                lines={[
                  [t("billing.time"), <span className="num">{formatDateTime(r.created_at)}</span>],
                  [t("audit.target"), r.target_type],
                ]}
              />
            )}
          />
        </Section>
      </div>
      <Dialog.Root open={!!open} onOpenChange={(o) => !o && setOpen(null)}>
        {open ? (
          <SheetContent title={open.action}>
            <div className="flex flex-col gap-4 p-5 text-[13px]">
              <dl className="divide-y divide-rule border-y-2 border-ink">
                {(
                  [
                    [t("billing.time"), formatDateTime(open.created_at)],
                    [t("audit.target"), `${open.target_type} ${open.target_id}`],
                    [t("ledger.operator"), open.operator_id ?? "—"],
                    ["IP", open.ip || "—"],
                  ] as const
                ).map(([k, v]) => (
                  <div key={k} className="grid grid-cols-[100px_1fr] gap-3 py-2">
                    <dt className="label-mono text-muted">{k}</dt>
                    <dd className="num break-all">{v}</dd>
                  </div>
                ))}
              </dl>
              <h3 className="label-mono">{t("audit.diff")}</h3>
              <pre className="num max-h-[60dvh] overflow-auto border border-ink p-3 text-[12px]">{JSON.stringify(open.diff, null, 2)}</pre>
            </div>
          </SheetContent>
        ) : null}
      </Dialog.Root>
    </>
  );
}
