import { useMemo, useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { keepPreviousData, useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Ban, Download, Plus } from "lucide-react";
import { toast } from "sonner";
import { api, unwrap, type Schemas } from "@/api/client";
import { keys, redeemBatchesInfinite, redeemCodesInfinite } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { DataTable, RowCard, columnHelper } from "@/components/DataTable";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { EmptyState } from "@/components/Feedback";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog";
import { Input, NativeSelect } from "@/components/ui/input";
import { Field, Label } from "@/components/ui/label";
import { useApiMutation } from "@/lib/mutations";
import { errorText } from "@/lib/errors";
import { formatDateTime, formatUSD, parseUSD } from "@/lib/format";
import { shortId } from "@/lib/utils";

type Status = Schemas["RedeemCodeStatus"];
const STATUSES: Status[] = ["unused", "used", "void"];

export const Route = createFileRoute("/_app/admin/redeem")({
  validateSearch: (s: Record<string, unknown>): { batch?: string; status?: Status } => ({
    batch: typeof s.batch === "string" && s.batch ? s.batch : undefined,
    status: STATUSES.includes(s.status as Status) ? (s.status as Status) : undefined,
  }),
  component: RedeemAdminPage,
});

type Batch = Schemas["RedeemBatch"];
type Code = Schemas["RedeemCode"];

/** Downloads a batch's CSV (with the full codes). */
async function exportBatch(b: Batch) {
  const res = await fetch(`/api/v1/admin/redeem-batches/${b.id}/export`, { credentials: "include" });
  if (!res.ok) throw new Error(`export failed: ${res.status}`);
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `redeem-${b.prefix || "codes"}-${b.created_at.slice(0, 10)}-${shortId(b.id)}.csv`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

function RedeemAdminPage() {
  const { t } = useTranslation();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: "/admin/redeem" });
  const qc = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [voiding, setVoiding] = useState<Code | null>(null);
  const batches = useInfiniteQuery(redeemBatchesInfinite);
  const batchRows = useMemo(() => batches.data?.pages.flatMap((p) => p.items) ?? [], [batches.data]);
  const codes = useInfiniteQuery({ ...redeemCodesInfinite({ batch_id: search.batch, status: search.status, limit: 100 }), placeholderData: keepPreviousData });
  const codeRows = useMemo(() => codes.data?.pages.flatMap((p) => p.items) ?? [], [codes.data]);

  const voidCode = useApiMutation({
    mutationFn: (id: string) => unwrap(api.POST("/admin/redeem-codes/{codeId}/void", { params: { path: { codeId: id } } })),
    success: t("redeemAdmin.voided"),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "redeem"] }),
  });
  const doExport = async (b: Batch) => {
    try {
      await exportBatch(b);
      toast.success(t("redeemAdmin.exported"));
    } catch (e) {
      toast.error(errorText(t, e));
    }
  };

  const bcol = columnHelper<Batch>();
  const batchColumns = [
    bcol.accessor("created_at", { header: t("redeemAdmin.created"), cell: (c) => <span className="num text-[12px]">{formatDateTime(c.getValue())}</span> }),
    bcol.accessor("prefix", { header: t("redeemAdmin.prefix"), cell: (c) => <span className="num font-semibold">{c.getValue() || "—"}</span> }),
    bcol.accessor("amount_micro", { header: t("redeemAdmin.amount"), cell: (c) => <span className="num">{formatUSD(c.getValue())}</span> }),
    bcol.display({
      id: "counts",
      header: t("redeemAdmin.counts"),
      cell: (c) => (
        <span className="num text-[12px]">
          {c.row.original.used} / {c.row.original.unused} / {c.row.original.voided}
        </span>
      ),
    }),
    bcol.accessor("expires_at", { header: t("redeemAdmin.expires"), cell: (c) => <span className="num text-[12px]">{c.getValue() ? formatDateTime(c.getValue()) : t("keyLimits.never")}</span> }),
    bcol.accessor("note", { header: t("redeemAdmin.note"), cell: (c) => <span className="text-[12px] text-muted">{c.getValue() || "—"}</span> }),
    bcol.display({
      id: "actions",
      header: () => <span className="sr-only">{t("common.actions")}</span>,
      cell: (c) => (
        <div className="flex justify-end gap-1">
          <Button size="sm" variant="ghost" onClick={() => navigate({ search: { batch: c.row.original.id, status: search.status }, replace: true })}>
            {t("redeemAdmin.viewCodes")}
          </Button>
          <Button size="sm" onClick={() => void doExport(c.row.original)} aria-label={t("redeemAdmin.exportBatch", { prefix: c.row.original.prefix || shortId(c.row.original.id) })}>
            <Download aria-hidden />
            CSV
          </Button>
        </div>
      ),
    }),
  ];

  const ccol = columnHelper<Code>();
  const codeColumns = [
    ccol.accessor("hint", { header: t("redeemAdmin.code"), cell: (c) => <span className="num font-semibold">{c.getValue()}</span> }),
    ccol.accessor("status", { header: t("redeemAdmin.status"), cell: (c) => <CodeStatus c={c.row.original} /> }),
    ccol.accessor("amount_micro", { header: t("redeemAdmin.amount"), cell: (c) => <span className="num">{formatUSD(c.getValue())}</span> }),
    ccol.display({
      id: "used",
      header: t("redeemAdmin.usedBy"),
      cell: (c) =>
        c.row.original.used_by ? (
          <span className="num text-[12px]">
            No.{shortId(c.row.original.used_by)} · {formatDateTime(c.row.original.used_at)}
          </span>
        ) : (
          <span className="text-muted">—</span>
        ),
    }),
    ccol.display({
      id: "actions",
      header: () => <span className="sr-only">{t("common.actions")}</span>,
      cell: (c) =>
        c.row.original.status === "unused" ? (
          <div className="flex justify-end">
            <Button size="sm" variant="ghost" onClick={() => setVoiding(c.row.original)} aria-label={t("redeemAdmin.voidCode", { code: c.row.original.hint })}>
              <Ban aria-hidden />
              {t("redeemAdmin.void")}
            </Button>
          </div>
        ) : null,
    }),
  ];

  return (
    <>
      <PageHeader
        code="OB-18"
        title={t("redeemAdmin.title")}
        actions={
          <Button variant="primary" onClick={() => setCreating(true)}>
            <Plus aria-hidden />
            {t("redeemAdmin.newBatch")}
          </Button>
        }
      />
      <div className="flex flex-col gap-6">
        <Section n={1} title={t("redeemAdmin.batches")} bodyClassName="p-0 sm:p-4">
          <DataTable
            label={t("redeemAdmin.batches")}
            columns={batchColumns}
            data={batchRows}
            rowKey={(b) => b.id}
            loading={batches.isPending}
            error={batches.error}
            onRetry={() => batches.refetch()}
            hasMore={batches.hasNextPage}
            fetchingMore={batches.isFetchingNextPage}
            onLoadMore={() => void batches.fetchNextPage()}
            empty={<EmptyState className="m-4 sm:m-0" title={t("redeemAdmin.noBatches")} description={t("redeemAdmin.noBatchesHint")} />}
            className="max-sm:p-4"
            renderCard={(b) => (
              <RowCard
                title={<span className="num">{b.prefix || "—"} · {formatUSD(b.amount_micro)}</span>}
                aside={
                  <Button size="sm" onClick={() => void doExport(b)}>
                    <Download aria-hidden />
                    CSV
                  </Button>
                }
                lines={[
                  [t("redeemAdmin.created"), <span className="num">{formatDateTime(b.created_at)}</span>],
                  [t("redeemAdmin.counts"), <span className="num">{b.used} / {b.unused} / {b.voided}</span>],
                  [t("redeemAdmin.expires"), b.expires_at ? formatDateTime(b.expires_at) : t("keyLimits.never")],
                ]}
                onClick={() => navigate({ search: { batch: b.id, status: search.status }, replace: true })}
              />
            )}
          />
        </Section>
        <Section
          n={2}
          title={t("redeemAdmin.codes")}
          aside={
            <div className="flex flex-wrap items-center gap-2">
              <Label htmlFor="rc-batch" className="text-muted">
                {t("redeemAdmin.batch")}
              </Label>
              <NativeSelect id="rc-batch" className="h-8 w-44" value={search.batch ?? ""} onChange={(e) => navigate({ search: { batch: e.target.value || undefined, status: search.status }, replace: true })}>
                <option value="">{t("common.all")}</option>
                {batchRows.map((b) => (
                  <option key={b.id} value={b.id}>
                    {(b.prefix || "—") + " · " + formatUSD(b.amount_micro) + " · " + b.created_at.slice(0, 10)}
                  </option>
                ))}
              </NativeSelect>
              <Label htmlFor="rc-status" className="text-muted">
                {t("redeemAdmin.status")}
              </Label>
              <NativeSelect id="rc-status" className="h-8 w-28" value={search.status ?? ""} onChange={(e) => navigate({ search: { batch: search.batch, status: (e.target.value || undefined) as Status | undefined }, replace: true })}>
                <option value="">{t("common.all")}</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {t(`redeemStatus.${s}`)}
                  </option>
                ))}
              </NativeSelect>
            </div>
          }
          bodyClassName="p-0 sm:p-4"
        >
          <DataTable
            label={t("redeemAdmin.codes")}
            columns={codeColumns}
            data={codeRows}
            rowKey={(c) => c.id}
            loading={codes.isPending}
            error={codes.error}
            onRetry={() => codes.refetch()}
            hasMore={codes.hasNextPage}
            fetchingMore={codes.isFetchingNextPage}
            onLoadMore={() => void codes.fetchNextPage()}
            virtualizeFrom={60}
            empty={<EmptyState className="m-4 sm:m-0" title={t("redeemAdmin.noCodes")} description={t("redeemAdmin.noCodesHint")} />}
            className="max-sm:p-4"
            renderCard={(c) => (
              <RowCard
                title={<span className="num">{c.hint}</span>}
                aside={<CodeStatus c={c} />}
                lines={[
                  [t("redeemAdmin.amount"), <span className="num">{formatUSD(c.amount_micro)}</span>],
                  [t("redeemAdmin.usedBy"), c.used_by ? `No.${shortId(c.used_by)}` : "—"],
                ]}
                onClick={c.status === "unused" ? () => setVoiding(c) : undefined}
              />
            )}
          />
        </Section>
      </div>
      {creating ? <BatchDialog onClose={() => setCreating(false)} /> : null}
      <ConfirmDialog
        open={!!voiding}
        onOpenChange={(o) => !o && setVoiding(null)}
        title={t("redeemAdmin.voidTitle")}
        description={t("redeemAdmin.voidBody", { code: voiding?.hint })}
        confirmLabel={t("redeemAdmin.void")}
        onConfirm={() => voidCode.mutateAsync(voiding!.id)}
      />
    </>
  );
}

function CodeStatus({ c }: { c: Code }) {
  const { t } = useTranslation();
  if (c.status === "unused" && c.expired) return <span className="label-mono border border-dashed border-ink px-1.5 py-0.5 text-muted">{t("redeemStatus.expired")}</span>;
  const cls =
    c.status === "unused"
      ? "label-mono border border-ink bg-yellow px-1.5 py-0.5 text-on-yellow"
      : c.status === "used"
        ? "label-mono border border-ink px-1.5 py-0.5"
        : "label-mono border border-dashed border-ink px-1.5 py-0.5 text-muted line-through";
  return <span className={cls}>{t(`redeemStatus.${c.status}`)}</span>;
}

function BatchDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [amount, setAmount] = useState("10");
  const [count, setCount] = useState("20");
  const [prefix, setPrefix] = useState("OXA");
  const [expires, setExpires] = useState("");
  const [note, setNote] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const create = useApiMutation({
    mutationFn: (body: Schemas["RedeemBatchCreate"]) => unwrap(api.POST("/admin/redeem-batches", { body })),
    success: t("redeemAdmin.batchCreated"),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.redeemBatches });
      void qc.invalidateQueries({ queryKey: ["admin", "redeem", "codes"] });
      onClose();
    },
  });
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const errs: Record<string, string> = {};
    const micro = parseUSD(amount);
    const n = Number(count);
    if (!Number.isFinite(micro) || micro <= 0) errs.amount = t("validation.positiveAmount");
    if (!Number.isInteger(n) || n < 1 || n > 1000) errs.count = t("validation.between", { min: 1, max: 1000 });
    if (prefix && !/^[A-Za-z0-9]{1,8}$/.test(prefix)) errs.prefix = t("redeemAdmin.prefixRule");
    let expiresAt: string | null = null;
    if (expires) {
      const d = new Date(expires);
      if (Number.isNaN(d.getTime()) || d.getTime() <= Date.now()) errs.expires = t("validation.futureDate");
      else expiresAt = d.toISOString();
    }
    setErrors(errs);
    if (Object.keys(errs).length) return;
    create.mutate({ amount_micro: micro, count: n, prefix: prefix.toUpperCase(), expires_at: expiresAt, note });
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={t("redeemAdmin.newBatch")} description={t("redeemAdmin.newBatchHint")}>
        <form onSubmit={submit} noValidate className="flex flex-col gap-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("redeemAdmin.amountUsd")} htmlFor="rb-amount" error={errors.amount}>
              <Input id="rb-amount" inputMode="decimal" className="num" value={amount} onChange={(e) => setAmount(e.target.value)} aria-invalid={!!errors.amount} />
            </Field>
            <Field label={t("redeemAdmin.count")} htmlFor="rb-count" hint={t("redeemAdmin.countHint")} error={errors.count}>
              <Input id="rb-count" inputMode="numeric" className="num" value={count} onChange={(e) => setCount(e.target.value)} aria-invalid={!!errors.count} />
            </Field>
          </div>
          <Field label={t("redeemAdmin.prefix")} htmlFor="rb-prefix" hint={t("redeemAdmin.prefixHint")} error={errors.prefix}>
            <Input id="rb-prefix" className="num uppercase" maxLength={8} value={prefix} onChange={(e) => setPrefix(e.target.value)} aria-invalid={!!errors.prefix} />
          </Field>
          <Field label={t("redeemAdmin.expires")} htmlFor="rb-expires" hint={t("keyLimits.expiresHint")} error={errors.expires}>
            <Input id="rb-expires" type="datetime-local" className="num" value={expires} onChange={(e) => setExpires(e.target.value)} />
          </Field>
          <Field label={t("redeemAdmin.note")} htmlFor="rb-note">
            <Input id="rb-note" maxLength={500} value={note} onChange={(e) => setNote(e.target.value)} />
          </Field>
          <DialogFooter className="mt-0">
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {t("redeemAdmin.generate")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
