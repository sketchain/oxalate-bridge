import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Controller, useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { adminPlansQuery, keys, modelsQuery } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { DataTable, RowCard, columnHelper } from "@/components/DataTable";
import { EmptyState } from "@/components/Feedback";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog";
import { Input, NativeSelect, Textarea } from "@/components/ui/input";
import { Field, Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useApiMutation } from "@/lib/mutations";
import { formatUSD, microToInput, parseUSD } from "@/lib/format";

export const Route = createFileRoute("/_app/admin/plans")({
  loader: ({ context: { queryClient } }) => queryClient.prefetchQuery(adminPlansQuery),
  component: AdminPlans,
});

type Plan = Schemas["Plan"];

function AdminPlans() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery(adminPlansQuery);
  const [editing, setEditing] = useState<Plan | "new" | null>(null);
  const [deleting, setDeleting] = useState<Plan | null>(null);
  const remove = useApiMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/admin/plans/{planId}", { params: { path: { planId: id } } })),
    success: t("adminPlans.deleted"),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.plans });
      void qc.invalidateQueries({ queryKey: keys.plansOnSale });
    },
  });
  const col = columnHelper<Plan>();
  const columns = [
    col.accessor("sort_order", { header: "#", cell: (c) => <span className="num text-[12px]">{c.getValue()}</span> }),
    col.accessor("name", { header: t("adminPlans.name"), cell: (c) => <span className="font-semibold">{c.getValue()}</span> }),
    col.accessor("weekly_quota_micro", { header: t("plans.spec"), cell: (c) => <span className="num">{formatUSD(c.getValue())} / {t("plans.week")}</span> }),
    col.accessor("price_micro", { header: t("plans.price"), cell: (c) => <span className="num">{formatUSD(c.getValue())}</span> }),
    col.display({ id: "dur", header: t("plans.duration"), cell: (c) => t(`duration.${c.row.original.duration_unit}`, { count: c.row.original.duration_value }) }),
    col.display({ id: "models", header: t("plans.models"), cell: (c) => <span className="block max-w-[220px] truncate text-[12px]">{c.row.original.allowed_models.join(", ") || t("plans.allModels")}</span> }),
    col.accessor("on_sale", {
      header: t("adminPlans.onSale"),
      cell: (c) => (c.getValue() ? <span className="label-mono bg-ink px-1.5 py-0.5 text-bg">{t("adminPlans.selling")}</span> : <span className="label-mono border border-rule px-1.5 py-0.5 text-muted">{t("adminPlans.offSale")}</span>),
    }),
    col.display({
      id: "actions",
      header: () => <span className="sr-only">{t("common.actions")}</span>,
      cell: (c) => (
        <div className="flex justify-end gap-1">
          <Button variant="ghost" size="icon-sm" aria-label={t("adminPlans.edit", { name: c.row.original.name })} onClick={() => setEditing(c.row.original)}>
            <Pencil aria-hidden />
          </Button>
          <Button variant="ghost" size="icon-sm" aria-label={t("adminPlans.delete", { name: c.row.original.name })} onClick={() => setDeleting(c.row.original)}>
            <Trash2 aria-hidden />
          </Button>
        </div>
      ),
    }),
  ];
  return (
    <>
      <PageHeader
        code="OB-10"
        title={t("adminPlans.title")}
        actions={
          <Button variant="primary" onClick={() => setEditing("new")}>
            <Plus aria-hidden />
            {t("adminPlans.new")}
          </Button>
        }
      />
      <Section n={1} title={t("adminPlans.list")} bodyClassName="p-0 sm:p-4">
        <DataTable
          label={t("adminPlans.list")}
          columns={columns}
          data={q.data?.items ?? []}
          rowKey={(p) => p.id}
          loading={q.isPending}
          error={q.error}
          onRetry={() => q.refetch()}
          className="max-sm:p-4"
          empty={
            <EmptyState
              className="m-4 sm:m-0"
              title={t("adminPlans.empty")}
              description={t("adminPlans.emptyHint")}
              action={
                <Button size="sm" variant="primary" onClick={() => setEditing("new")}>
                  {t("adminPlans.new")}
                </Button>
              }
            />
          }
          renderCard={(p) => (
            <RowCard
              onClick={() => setEditing(p)}
              title={p.name}
              aside={<span className="num">{formatUSD(p.price_micro)}</span>}
              lines={[
                [t("plans.spec"), <span className="num">{formatUSD(p.weekly_quota_micro)}</span>],
                [t("plans.duration"), t(`duration.${p.duration_unit}`, { count: p.duration_value })],
                [t("adminPlans.onSale"), p.on_sale ? t("adminPlans.selling") : t("adminPlans.offSale")],
              ]}
            />
          )}
        />
      </Section>
      {editing ? <PlanDialog plan={editing === "new" ? null : editing} onClose={() => setEditing(null)} /> : null}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t("adminPlans.deleteTitle", { name: deleting?.name })}
        description={t("adminPlans.deleteBody")}
        confirmLabel={t("common.delete")}
        onConfirm={() => remove.mutateAsync(deleting!.id)}
      />
    </>
  );
}

function PlanDialog({ plan, onClose }: { plan: Plan | null; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const models = useQuery(modelsQuery);
  const usd = z.string().refine((v) => Number.isFinite(parseUSD(v)) && parseUSD(v) >= 0, t("validation.amountNonNegative"));
  const schema = z.object({
    name: z.string().trim().min(1, t("validation.required")).max(100),
    description: z.string().max(1000),
    weekly_quota: usd,
    price: usd,
    duration_value: z.string().regex(/^[1-9]\d*$/, t("validation.positiveInt")),
    duration_unit: z.enum(["day", "week", "month"]),
    allowed_models: z.string(),
    on_sale: z.boolean(),
    sort_order: z.string().regex(/^-?\d+$/, t("validation.int")),
  });
  type Values = z.infer<typeof schema>;
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: {
      name: plan?.name ?? "",
      description: plan?.description ?? "",
      weekly_quota: plan ? microToInput(plan.weekly_quota_micro) : "",
      price: plan ? microToInput(plan.price_micro) : "",
      duration_value: String(plan?.duration_value ?? 1),
      duration_unit: plan?.duration_unit ?? "month",
      allowed_models: plan?.allowed_models.join(", ") ?? "",
      on_sale: plan?.on_sale ?? true,
      sort_order: String(plan?.sort_order ?? 0),
    },
  });
  const errs = form.formState.errors;
  const save = useApiMutation({
    mutationFn: (v: Values) => {
      const body: Schemas["PlanInput"] = {
        name: v.name,
        description: v.description,
        subscription: "claude",
        weekly_quota_micro: parseUSD(v.weekly_quota),
        price_micro: parseUSD(v.price),
        duration_value: Number(v.duration_value),
        duration_unit: v.duration_unit,
        allowed_models: v.allowed_models
          .split(/[,\s]+/)
          .map((m) => m.trim())
          .filter(Boolean),
        on_sale: v.on_sale,
        sort_order: Number(v.sort_order),
      };
      return plan ? unwrap(api.PUT("/admin/plans/{planId}", { params: { path: { planId: plan.id } }, body })) : unwrap(api.POST("/admin/plans", { body }));
    },
    success: t("adminPlans.saved"),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.plans });
      void qc.invalidateQueries({ queryKey: keys.plansOnSale });
      onClose();
    },
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={plan ? t("adminPlans.editTitle") : t("adminPlans.new")} description={plan ? t("adminPlans.editHint") : undefined} wide>
        <form onSubmit={form.handleSubmit((v) => save.mutate(v))} noValidate className="flex flex-col gap-4">
          <Field label={t("adminPlans.name")} htmlFor="pl-name" error={errs.name?.message}>
            <Input id="pl-name" aria-invalid={!!errs.name} {...form.register("name")} />
          </Field>
          <Field label={t("adminPlans.description")} htmlFor="pl-desc">
            <Textarea id="pl-desc" rows={2} {...form.register("description")} />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("adminPlans.weeklyQuotaUsd")} htmlFor="pl-quota" error={errs.weekly_quota?.message}>
              <Input id="pl-quota" inputMode="decimal" className="num" aria-invalid={!!errs.weekly_quota} {...form.register("weekly_quota")} />
            </Field>
            <Field label={t("adminPlans.priceUsd")} htmlFor="pl-price" error={errs.price?.message}>
              <Input id="pl-price" inputMode="decimal" className="num" aria-invalid={!!errs.price} {...form.register("price")} />
            </Field>
            <Field label={t("plans.duration")} htmlFor="pl-dur" error={errs.duration_value?.message}>
              <div className="flex gap-2">
                <Input id="pl-dur" inputMode="numeric" className="num w-24" aria-invalid={!!errs.duration_value} {...form.register("duration_value")} />
                <NativeSelect aria-label={t("adminPlans.durationUnit")} {...form.register("duration_unit")}>
                  <option value="day">{t("adminPlans.unit.day")}</option>
                  <option value="week">{t("adminPlans.unit.week")}</option>
                  <option value="month">{t("adminPlans.unit.month")}</option>
                </NativeSelect>
              </div>
            </Field>
            <Field label={t("adminPlans.sortOrder")} htmlFor="pl-sort" error={errs.sort_order?.message}>
              <Input id="pl-sort" inputMode="numeric" className="num" aria-invalid={!!errs.sort_order} {...form.register("sort_order")} />
            </Field>
          </div>
          <Field label={t("plans.models")} htmlFor="pl-models" hint={t("adminPlans.modelsHint", { list: models.data?.items.join(", ") || "—" })}>
            <Input id="pl-models" className="num" list="pl-model-list" {...form.register("allowed_models")} />
          </Field>
          <datalist id="pl-model-list">
            {models.data?.items.map((m) => (
              <option key={m} value={m} />
            ))}
          </datalist>
          <div className="flex items-center justify-between border border-ink px-3 py-2.5">
            <Label htmlFor="pl-sale">{t("adminPlans.onSale")}</Label>
            <Controller control={form.control} name="on_sale" render={({ field }) => <Switch id="pl-sale" checked={field.value} onCheckedChange={field.onChange} />} />
          </div>
          <DialogFooter className="mt-0">
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {t("common.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
