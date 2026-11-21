import { useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { CheckCircle2, Diamond, Download, ShieldCheck } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { accountsQuery, keys } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { StatStrip } from "@/components/StatStrip";
import { AccountCard } from "@/components/AccountCard";
import { EmptyState, ErrorBox } from "@/components/Feedback";
import { ValidationResult } from "@/components/ValidationResult";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { useApiMutation } from "@/lib/mutations";
import { isAssignable, needsAttention } from "@/lib/accounts";
import { formatUSD } from "@/lib/format";
import { cn } from "@/lib/utils";

type Filter = "all" | "assignable" | "attention";

export const Route = createFileRoute("/_app/admin/accounts/")({
  validateSearch: (s: Record<string, unknown>): { filter?: Filter; import?: boolean } => ({
    filter: s.filter === "assignable" || s.filter === "attention" ? s.filter : undefined,
    import: s.import === true || s.import === "true" ? true : undefined,
  }),
  loader: ({ context: { queryClient } }) => queryClient.prefetchQuery(accountsQuery),
  component: AccountsPage,
});

function AccountsPage() {
  const { t } = useTranslation();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: "/admin/accounts/" });
  const filter: Filter = search.filter ?? "all";
  const q = useQuery(accountsQuery);
  const items = q.data?.items ?? [];
  const shown = items.filter((a) => (filter === "assignable" ? isAssignable(a) : filter === "attention" ? needsAttention(a) : true));
  const attention = items.filter(needsAttention).length;
  const importing = !!search.import;
  const setImporting = (v: boolean) => navigate({ search: (s) => ({ ...s, import: v ? true : undefined }), replace: true });

  return (
    <>
      <PageHeader
        code="OB-09"
        title={t("accounts.title")}
        actions={
          <Button variant="primary" onClick={() => setImporting(true)}>
            <Download aria-hidden />
            {t("accounts.import")}
          </Button>
        }
      />
      <div className="flex flex-col gap-6">
        <StatStrip
          loading={q.isPending}
          stats={[
            { label: t("accounts.total"), value: items.length, note: t("accounts.enabledCount", { n: items.filter((a) => a.enabled).length }) },
            { label: t("accounts.assignable"), value: items.filter(isAssignable).length, note: t("accounts.assignableNote") },
            {
              label: t("accounts.attention"),
              value: attention,
              note: t("accounts.attentionNote"),
              alert: attention > 0,
              icon: <Diamond className="size-3.5 fill-current" aria-label={attention > 0 ? t("accounts.attentionIcon") : undefined} aria-hidden={attention === 0} />,
            },
            { label: t("accounts.headroomTotal"), value: formatUSD(items.reduce((s, a) => s + Math.max(0, a.runtime.headroom_micro), 0)), note: t("accounts.headroomNote") },
            { label: t("accounts.reservedTotal"), value: formatUSD(items.reduce((s, a) => s + a.runtime.reserved_micro, 0)), note: t("accounts.reservedNote") },
          ]}
        />

        <div role="group" aria-label={t("accounts.filter")} className="flex w-fit border-2 border-ink">
          {(["all", "assignable", "attention"] as const).map((f) => (
            <button
              key={f}
              type="button"
              aria-pressed={filter === f}
              onClick={() => navigate({ search: (s) => ({ ...s, filter: f === "all" ? undefined : f }), replace: true })}
              className={cn("h-9 border-l border-ink px-4 text-[13px] font-semibold first:border-l-0", filter === f ? "bg-yellow text-on-yellow" : "hover:bg-rule")}
            >
              {t(`accounts.filters.${f}`)}
              <span className="num ml-1.5 text-[11px]">
                {f === "all" ? items.length : f === "assignable" ? items.filter(isAssignable).length : attention}
              </span>
            </button>
          ))}
        </div>

        {q.error ? (
          <ErrorBox error={q.error} onRetry={() => q.refetch()} />
        ) : q.isPending ? (
          <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
            {Array.from({ length: 3 }, (_, i) => (
              <Skeleton key={i} className="h-72" />
            ))}
          </div>
        ) : items.length === 0 ? (
          <EmptyState
            title={t("accounts.emptyTitle")}
            description={t("accounts.emptyHint")}
            action={
              <Button variant="primary" size="sm" onClick={() => setImporting(true)}>
                {t("accounts.import")}
              </Button>
            }
          />
        ) : shown.length === 0 ? (
          <EmptyState title={t("accounts.filterEmpty")} icon={<CheckCircle2 className="size-4" aria-hidden />} />
        ) : (
          <ul className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3" aria-label={t("accounts.title")}>
            {shown.map((a) => (
              <li key={a.id}>
                <AccountCard a={a} />
              </li>
            ))}
          </ul>
        )}
      </div>
      {importing ? <ImportDialog onClose={() => setImporting(false)} /> : null}
    </>
  );
}

/** Import: validate the connection first, show what was detected, then save. */
function ImportDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [checked, setChecked] = useState<Schemas["AccountTestResult"] | null>(null);
  const [checkError, setCheckError] = useState<unknown>(null);
  const schema = z.object({
    name: z.string().trim().min(1, t("validation.required")).max(100),
    base_url: z.string().trim().url(t("validation.url")),
    cpa_key: z.string().trim().min(1, t("validation.required")),
    quota_token: z.string().trim().min(1, t("validation.required")),
  });
  type Values = z.infer<typeof schema>;
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { name: "", base_url: "", cpa_key: "", quota_token: "" } });
  const errs = form.formState.errors;
  // Any edit of the connection invalidates the last check.
  const resetCheck = () => setChecked(null);

  const validate = form.handleSubmit(async (v) => {
    setCheckError(null);
    try {
      setChecked(await unwrap(api.POST("/admin/accounts/validate", { body: { base_url: v.base_url, cpa_key: v.cpa_key, quota_token: v.quota_token } })));
    } catch (e) {
      setCheckError(e);
    }
  });
  const save = useApiMutation({
    mutationFn: (v: Values) => unwrap(api.POST("/admin/accounts", { body: { ...v, enabled: true } })),
    success: t("accounts.imported"),
    onSuccess: (a) => {
      void qc.invalidateQueries({ queryKey: keys.accounts });
      void qc.invalidateQueries({ queryKey: keys.overview });
      onClose();
      void navigate({ to: "/admin/accounts/$accountId", params: { accountId: a.id } });
    },
  });

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={t("accounts.importTitle")} description={t("accounts.importHint")} wide>
        <form onSubmit={validate} noValidate className="flex flex-col gap-4">
          <Field label={t("accounts.name")} htmlFor="ia-name" error={errs.name?.message}>
            <Input id="ia-name" aria-invalid={!!errs.name} {...form.register("name")} />
          </Field>
          <Field label={t("accounts.baseUrl")} htmlFor="ia-url" hint="https://cpa.example.com" error={errs.base_url?.message}>
            <Input id="ia-url" className="num" aria-invalid={!!errs.base_url} {...form.register("base_url", { onChange: resetCheck })} />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("accounts.cpaKey")} htmlFor="ia-key" error={errs.cpa_key?.message}>
              <Input id="ia-key" type="password" autoComplete="off" className="num" aria-invalid={!!errs.cpa_key} {...form.register("cpa_key", { onChange: resetCheck })} />
            </Field>
            <Field label={t("accounts.quotaToken")} htmlFor="ia-token" error={errs.quota_token?.message}>
              <Input id="ia-token" type="password" autoComplete="off" className="num" aria-invalid={!!errs.quota_token} {...form.register("quota_token", { onChange: resetCheck })} />
            </Field>
          </div>
          {checkError ? <ErrorBox error={checkError} /> : null}
          {checked ? <ValidationResult r={checked} /> : null}
          <DialogFooter className="mt-0">
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" variant={checked?.ok ? "secondary" : "primary"} disabled={form.formState.isSubmitting}>
              <ShieldCheck aria-hidden />
              {form.formState.isSubmitting ? t("accounts.validating") : t("accounts.validate")}
            </Button>
            <Button variant="primary" disabled={!checked?.ok || save.isPending} onClick={form.handleSubmit((v) => save.mutate(v))}>
              {t("accounts.confirmImport")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
