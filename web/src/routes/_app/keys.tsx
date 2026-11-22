import { useEffect, useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useForm, Controller } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Eye, EyeOff, Pencil, Plus, RotateCcw, Trash2 } from "lucide-react";
import { Dialog as D } from "radix-ui";
import { toast } from "sonner";
import { api, unwrap, type Schemas } from "@/api/client";
import { keyDailyQuery, keyLimitsQuery, keySecretQuery, keys, keysQuery, putKey, subscriptionsQuery } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { DataTable, columnHelper } from "@/components/DataTable";
import { TierChain } from "@/components/Tags";
import { CopyButton, EmptyState } from "@/components/Feedback";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { TierEditor } from "@/components/TierEditor";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, SheetContent } from "@/components/ui/dialog";
import { FieldGroup, KeyLimitFields } from "@/components/KeyLimitFields";
import { KeyLimitsPanel } from "@/components/KeyLimitsPanel";
import { KeyStatusTag } from "@/components/KeyStatusTag";
import { TierStackedBars } from "@/components/Charts";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorBox } from "@/components/Feedback";
import { draftFromKey, draftToBody, type LimitDraft, type LimitErrors } from "@/lib/keyLimits";
import { dailyTierPoints, seriesFacts } from "@/lib/usage";
import { Input, NativeSelect } from "@/components/ui/input";
import { Field, Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useApiMutation } from "@/lib/mutations";
import { rowsFromTiers, tiersFromRows, validateTiers, type TierRow } from "@/lib/tiers";
import { formatDateTime, formatUSD } from "@/lib/format";
import { shortId } from "@/lib/utils";

export const Route = createFileRoute("/_app/keys")({
  validateSearch: (s: Record<string, unknown>): { new?: boolean } => ({ new: s.new === true || s.new === "true" ? true : undefined }),
  loader: ({ context: { queryClient } }) => {
    void queryClient.prefetchQuery(keysQuery);
    void queryClient.prefetchQuery(subscriptionsQuery);
  },
  component: KeysPage,
});

type Key = Schemas["ApiKey"];

function KeysPage() {
  const { t } = useTranslation();
  const search = Route.useSearch();
  const navigate = useNavigate();
  const q = useQuery(keysQuery);
  const subs = useQuery(subscriptionsQuery);
  const [editing, setEditing] = useState<Key | "new" | null>(null);
  const [created, setCreated] = useState<Schemas["ApiKeyWithSecret"] | null>(null);
  const [deleting, setDeleting] = useState<Key | null>(null);
  const [detail, setDetail] = useState<string | null>(null);
  const qc = useQueryClient();

  useEffect(() => {
    if (search.new) {
      setEditing("new");
      void navigate({ to: "/keys", search: {}, replace: true });
    }
  }, [search.new, navigate]);

  const toggle = useApiMutation({
    mutationFn: (v: { id: string; enabled: boolean }) => unwrap(api.PATCH("/keys/{keyId}", { params: { path: { keyId: v.id } }, body: { enabled: v.enabled } })),
    onMutate: async (v) => {
      await qc.cancelQueries({ queryKey: keys.keys });
      const prev = qc.getQueryData<Schemas["ApiKeyList"]>(keys.keys);
      qc.setQueryData<Schemas["ApiKeyList"]>(keys.keys, (old) => (old ? { items: old.items.map((k) => (k.id === v.id ? { ...k, enabled: v.enabled } : k)) } : old));
      return { prev };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(keys.keys, ctx.prev);
    },
    onSuccess: (key) => putKey(qc, key),
  });

  const remove = useApiMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/keys/{keyId}", { params: { path: { keyId: id } } })),
    success: t("keys.deleted"),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.keys }),
  });

  const subName = (id?: string | null) => {
    if (!id) return "—";
    const s = subs.data?.items.find((x) => x.id === id);
    return s ? `${s.plan_name} · No.${shortId(s.id)}` : `No.${shortId(id)}`;
  };

  const col = columnHelper<Key>();
  const columns = [
    col.accessor("name", {
      header: t("keys.name"),
      cell: (c) => (
        <button type="button" className="text-left font-semibold underline-offset-4 hover:underline" onClick={() => setDetail(c.row.original.id)} aria-label={t("keys.openDetail", { name: c.row.original.name || c.row.original.prefix })}>
          {c.getValue() || t("keys.unnamed")}
        </button>
      ),
    }),
    col.display({ id: "key", header: t("keys.key"), cell: (c) => <SecretCell k={c.row.original} /> }),
    col.display({ id: "tiers", header: t("keys.tiers"), cell: (c) => <TierChain tiers={c.row.original.tiers} /> }),
    col.display({ id: "sub", header: t("keys.subscription"), cell: (c) => <span className="text-[12px]">{subName(c.row.original.subscription_id)}</span> }),
    col.display({ id: "status", header: t("keys.status"), cell: (c) => <KeyStatusTag status={c.row.original.status} /> }),
    col.display({ id: "quota", header: t("keys.quotaCol"), cell: (c) => <QuotaCell k={c.row.original} /> }),
    col.display({
      id: "limits",
      header: t("keys.limits"),
      cell: (c) => (
        <span className="num text-[12px]">
          {c.row.original.max_concurrency ?? "∞"} / {c.row.original.rpm ?? "∞"}
        </span>
      ),
    }),
    col.display({
      id: "enabled",
      header: t("keys.enabled"),
      cell: (c) => (
        <Switch
          checked={c.row.original.enabled}
          aria-label={t("keys.toggle", { name: c.row.original.name || c.row.original.prefix })}
          onCheckedChange={(v) => toggle.mutate({ id: c.row.original.id, enabled: v })}
        />
      ),
    }),
    col.accessor("last_used_at", { header: t("keys.lastUsed"), cell: (c) => <span className="num text-[12px] text-muted">{formatDateTime(c.getValue())}</span> }),
    col.display({
      id: "actions",
      header: () => <span className="sr-only">{t("common.actions")}</span>,
      cell: (c) => (
        <div className="flex justify-end gap-1">
          <Button variant="ghost" size="icon-sm" aria-label={t("keys.edit", { name: c.row.original.name })} onClick={() => setEditing(c.row.original)}>
            <Pencil aria-hidden />
          </Button>
          <Button variant="ghost" size="icon-sm" aria-label={t("keys.delete", { name: c.row.original.name })} onClick={() => setDeleting(c.row.original)}>
            <Trash2 aria-hidden />
          </Button>
        </div>
      ),
    }),
  ];

  return (
    <>
      <PageHeader
        code="OB-02"
        title={t("keys.title")}
        actions={
          <Button variant="primary" onClick={() => setEditing("new")}>
            <Plus aria-hidden />
            {t("keys.new")}
          </Button>
        }
      />
      <Section n={1} title={t("keys.list")} aside={<span className="num text-[12px] text-muted">{q.data ? t("common.count", { count: q.data.items.length }) : ""}</span>}>
        <DataTable
          label={t("keys.list")}
          columns={columns}
          data={q.data?.items ?? []}
          rowKey={(k) => k.id}
          loading={q.isPending}
          error={q.error}
          onRetry={() => q.refetch()}
          empty={
            <EmptyState
              title={t("keys.emptyTitle")}
              description={t("keys.emptyHint")}
              action={
                <Button variant="primary" size="sm" onClick={() => setEditing("new")}>
                  <Plus aria-hidden />
                  {t("keys.new")}
                </Button>
              }
            />
          }
          renderCard={(k) => (
            <div className="border border-ink p-3">
              <div className="flex items-center justify-between gap-2">
                <button type="button" className="truncate text-left font-semibold underline underline-offset-4" onClick={() => setDetail(k.id)} aria-label={t("keys.openDetail", { name: k.name || k.prefix })}>
                  {k.name || t("keys.unnamed")}
                </button>
                <Switch checked={k.enabled} aria-label={t("keys.toggle", { name: k.name || k.prefix })} onCheckedChange={(v) => toggle.mutate({ id: k.id, enabled: v })} />
              </div>
              <div className="mt-2">
                <SecretCell k={k} />
              </div>
              <TierChain tiers={k.tiers} className="mt-2" />
              <dl className="mt-2 grid grid-cols-2 gap-1 text-[12px]">
                <dt className="label-mono text-muted">{t("keys.status")}</dt>
                <dd className="text-right">
                  <KeyStatusTag status={k.status} />
                </dd>
                <dt className="label-mono text-muted">{t("keys.quotaCol")}</dt>
                <dd className="text-right">
                  <QuotaCell k={k} />
                </dd>
                <dt className="label-mono text-muted">{t("keys.subscription")}</dt>
                <dd className="text-right">{subName(k.subscription_id)}</dd>
                <dt className="label-mono text-muted">{t("keys.lastUsed")}</dt>
                <dd className="num text-right">{formatDateTime(k.last_used_at)}</dd>
              </dl>
              <div className="mt-3 flex gap-2 [&>*]:flex-1">
                <Button size="sm" onClick={() => setEditing(k)}>
                  <Pencil aria-hidden />
                  {t("common.edit")}
                </Button>
                <Button size="sm" variant="danger" onClick={() => setDeleting(k)}>
                  <Trash2 aria-hidden />
                  {t("common.delete")}
                </Button>
              </div>
            </div>
          )}
        />
      </Section>

      {editing ? (
        <KeyDialog
          initial={editing === "new" ? null : editing}
          subs={subs.data?.items ?? []}
          onClose={() => setEditing(null)}
          onCreated={(k) => {
            setEditing(null);
            setCreated(k);
          }}
        />
      ) : null}

      <D.Root open={!!detail} onOpenChange={(o) => !o && setDetail(null)}>
        {detail ? (
          <KeyDrawer
            k={q.data?.items.find((k) => k.id === detail)}
            onEdit={(k) => {
              setDetail(null);
              setEditing(k);
            }}
          />
        ) : null}
      </D.Root>

      <Dialog open={!!created} onOpenChange={(o) => !o && setCreated(null)}>
        {created ? (
          <DialogContent title={t("keys.createdTitle")} description={t("keys.createdHint")}>
            <div className="border-2 border-ink">
              <div className="label-mono bg-yellow px-3 py-1.5 text-on-yellow">{created.name || t("keys.unnamed")}</div>
              <div className="num break-all px-3 py-3 text-[15px] select-all" data-testid="new-key">
                {created.key}
              </div>
            </div>
            <DialogFooter>
              <CopyButton value={created.key} label={t("keys.copyFull")} showLabel variant="primary" className="h-10 px-4" />
              <Button onClick={() => setCreated(null)}>{t("common.done")}</Button>
            </DialogFooter>
          </DialogContent>
        ) : null}
      </Dialog>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t("keys.deleteTitle")}
        description={t("keys.deleteBody", { name: deleting?.name || deleting?.prefix })}
        confirmLabel={t("common.delete")}
        onConfirm={() => remove.mutateAsync(deleting!.id)}
      />
    </>
  );
}

/** Masked key with reveal and copy (the full key is fetched on demand). */
function SecretCell({ k }: { k: Key }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [shown, setShown] = useState(false);
  const secret = useQuery({ ...keySecretQuery(k.id), enabled: shown });
  return (
    <span className="inline-flex max-w-full items-center gap-1">
      <span className="num truncate text-[12px]">{shown && secret.data ? secret.data.key : k.prefix}</span>
      <Button variant="ghost" size="icon-sm" aria-label={shown ? t("keys.hide") : t("keys.reveal")} aria-pressed={shown} onClick={() => setShown(!shown)}>
        {shown ? <EyeOff aria-hidden /> : <Eye aria-hidden />}
      </Button>
      <CopyButton variant="ghost" label={t("keys.copy")} value={async () => (await qc.fetchQuery(keySecretQuery(k.id))).key} />
    </span>
  );
}

function KeyDialog({
  initial,
  subs,
  onClose,
  onCreated,
}: {
  initial: Key | null;
  subs: Schemas["MySubscription"][];
  onClose: () => void;
  onCreated: (k: Schemas["ApiKeyWithSecret"]) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const bindable = subs.filter((s) => s.status === "active" || s.status === "pending_assignment" || s.id === initial?.subscription_id);
  const [rows, setRows] = useState<TierRow[]>(() => rowsFromTiers(initial?.tiers ?? (bindable.length ? ["plan", "standard"] : ["standard"])));
  const [tierError, setTierError] = useState<string | null>(null);
  const optionalInt = z
    .string()
    .trim()
    .refine((v) => v === "" || (/^\d+$/.test(v) && Number(v) <= 100000), t("validation.nonNegativeInt"));
  const schema = z.object({
    name: z.string().trim().max(100, t("validation.max", { n: 100 })),
    subscription_id: z.string(),
    max_concurrency: optionalInt,
    rpm: optionalInt,
    enabled: z.boolean(),
  });
  type Values = z.infer<typeof schema>;
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: {
      name: initial?.name ?? "",
      subscription_id: initial?.subscription_id ?? bindable[0]?.id ?? "",
      max_concurrency: initial?.max_concurrency != null ? String(initial.max_concurrency) : "",
      rpm: initial?.rpm != null ? String(initial.rpm) : "",
      enabled: initial?.enabled ?? true,
    },
  });
  const errs = form.formState.errors;
  const planOn = tiersFromRows(rows).includes("plan");
  const [draft, setDraft] = useState<LimitDraft>(() => draftFromKey(initial));
  const [limitErrors, setLimitErrors] = useState<LimitErrors>({});

  const save = useApiMutation({
    mutationFn: async (v: Values) => {
      const body = {
        ...draftToBody(draft).body,
        name: v.name,
        tiers: tiersFromRows(rows),
        subscription_id: planOn ? v.subscription_id : null,
        max_concurrency: v.max_concurrency === "" ? null : Number(v.max_concurrency),
        rpm: v.rpm === "" ? null : Number(v.rpm),
        enabled: v.enabled,
      };
      if (initial) return { kind: "edit" as const, key: await unwrap(api.PATCH("/keys/{keyId}", { params: { path: { keyId: initial.id } }, body })) };
      return { kind: "new" as const, key: await unwrap(api.POST("/keys", { body })) };
    },
    onSuccess: (res) => {
      void qc.invalidateQueries({ queryKey: keys.keys });
      if (res.kind === "new") onCreated(res.key as Schemas["ApiKeyWithSecret"]);
      else {
        toast.success(t("keys.saved"));
        onClose();
      }
    },
  });

  const onSubmit = form.handleSubmit((v) => {
    const le = draftToBody(draft).errors;
    setLimitErrors(le);
    if (Object.keys(le).length) return;
    const problem = validateTiers(rows, v.subscription_id);
    if (problem) {
      setTierError(t(`keys.problem.${problem}`));
      return;
    }
    setTierError(null);
    save.mutate(v);
  });

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={initial ? t("keys.editTitle") : t("keys.newTitle")} wide>
        <form onSubmit={onSubmit} noValidate className="flex flex-col gap-5">
          <FieldGroup title={t("keyLimits.groupBasic")}>
          <Field label={t("keys.name")} htmlFor="key-name" error={errs.name?.message}>
            <Input id="key-name" placeholder={t("keys.namePlaceholder")} aria-invalid={!!errs.name} {...form.register("name")} />
          </Field>
          <div className="flex items-center justify-between border border-ink px-3 py-2.5">
            <Label htmlFor="key-enabled">{t("keys.enabled")}</Label>
            <Controller control={form.control} name="enabled" render={({ field }) => <Switch id="key-enabled" checked={field.value} onCheckedChange={field.onChange} />} />
          </div>
          </FieldGroup>
          <FieldGroup title={t("keyLimits.groupTiers")}>
          <div className="flex flex-col gap-1.5">
            <Label id="tier-label">{t("keys.tierOrder")}</Label>
            <p className="text-[12px] text-muted">{t("keys.tierOrderHint")}</p>
            <TierEditor rows={rows} onChange={(r) => (setRows(r), setTierError(null))} labelledBy="tier-label" />
            <div className="flex items-center gap-2 text-[12px]">
              <span className="label-mono text-muted">{t("keys.preview")}</span>
              {tiersFromRows(rows).length ? <TierChain tiers={tiersFromRows(rows)} /> : <span className="text-muted">—</span>}
            </div>
            {tierError ? (
              <p role="alert" className="text-[12px] font-semibold">
                <span className="label-mono mr-1.5 inline-block bg-red px-1 text-on-red">!</span>
                {tierError}
              </p>
            ) : null}
          </div>
          {planOn ? (
            <Field label={t("keys.bindSubscription")} htmlFor="key-sub" hint={bindable.length ? t("keys.bindHint") : t("keys.noSubscription")}>
              <NativeSelect id="key-sub" {...form.register("subscription_id", { onChange: () => setTierError(null) })}>
                <option value="">{t("keys.chooseSubscription")}</option>
                {bindable.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.plan_name} · No.{shortId(s.id)} · {t(`subStatus.${s.status}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
          ) : null}
          </FieldGroup>
          <FieldGroup title={t("keyLimits.groupRate")}>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("keys.maxConcurrency")} htmlFor="key-conc" hint={t("keys.emptyUnlimited")} error={errs.max_concurrency?.message}>
              <Input id="key-conc" inputMode="numeric" className="num" aria-invalid={!!errs.max_concurrency} {...form.register("max_concurrency")} />
            </Field>
            <Field label={t("keys.rpm")} htmlFor="key-rpm" hint={t("keys.emptyUnlimited")} error={errs.rpm?.message}>
              <Input id="key-rpm" inputMode="numeric" className="num" aria-invalid={!!errs.rpm} {...form.register("rpm")} />
            </Field>
          </div>
          </FieldGroup>
          <KeyLimitFields draft={draft} onChange={setDraft} errors={limitErrors} idPrefix="key" />
          {Object.keys(limitErrors).length ? (
            <p role="alert" className="text-[12px] font-semibold">
              <span className="label-mono mr-1.5 inline-block bg-red px-1 text-on-red">!</span>
              {t("keyLimits.fixErrors")}
            </p>
          ) : null}
          <DialogFooter className="mt-0">
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {initial ? t("common.save") : t("keys.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}


/** Persisted quota usage of the list row (the drawer shows real-time figures). */
function QuotaCell({ k }: { k: Key }) {
  const { t } = useTranslation();
  const used = k.quota_scope === "balance" ? k.used_balance_micro : k.used_all_micro;
  return (
    <span className="num text-[12px]">
      {k.quota_limit_micro == null ? t("keyLimits.unlimited") : `${formatUSD(used)} / ${formatUSD(k.quota_limit_micro)}`}
    </span>
  );
}

/** Side drawer of one key: limits with real-time usage, reset and the daily usage chart. */
function KeyDrawer({ k, onEdit }: { k?: Key; onEdit: (k: Key) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [days, setDays] = useState(30);
  const [confirmReset, setConfirmReset] = useState(false);
  const limits = useQuery({ ...keyLimitsQuery(k?.id ?? ""), enabled: !!k });
  const daily = useQuery({ ...keyDailyQuery(k?.id ?? "", days), enabled: !!k });
  const reset = useApiMutation({
    mutationFn: (id: string) => unwrap(api.POST("/keys/{keyId}/reset-usage", { params: { path: { keyId: id } } })),
    success: t("keyLimits.resetDone"),
    onSuccess: (key) => {
      putKey(qc, key);
      void qc.invalidateQueries({ queryKey: keys.keyLimits(key.id) });
    },
  });
  if (!k) return null;
  const series = dailyTierPoints(daily.data?.items ?? [], days);
  const facts = seriesFacts(series, formatUSD);
  const access: [string, string][] = [
    [t("keyLimits.expires"), k.expires_at ? formatDateTime(k.expires_at) : t("keyLimits.never")],
    [t("keyLimits.models"), k.model_allowlist?.length ? k.model_allowlist.join(", ") : t("keyLimits.any")],
    [t("keyLimits.ips"), k.ip_allowlist?.length ? k.ip_allowlist.join(", ") : t("keyLimits.any")],
    [t("keyLimits.ownerQuery"), k.allow_owner_query ? t("common.yes") : t("common.no")],
  ];
  return (
    <SheetContent title={k.name || t("keys.unnamed")} data-testid="key-drawer" className="sm:w-[560px]">
      <div className="flex flex-col gap-5 p-5">
        <div className="flex flex-wrap items-center gap-2">
          <KeyStatusTag status={k.status} />
          <span className="num text-[12px] text-muted">{k.prefix}</span>
          <TierChain tiers={k.tiers} />
        </div>
        <section aria-labelledby="kd-limits" className="border-2 border-ink">
          <div className="flex items-center justify-between gap-2 border-b border-ink px-3 py-2">
            <h3 id="kd-limits" className="text-[14px] font-bold">
              {t("keyLimits.usageTitle")}
            </h3>
            <Button size="sm" onClick={() => setConfirmReset(true)}>
              <RotateCcw aria-hidden />
              {t("keyLimits.reset")}
            </Button>
          </div>
          <div className="px-3 pb-3">
            {limits.isPending ? <Skeleton className="mt-3 h-40" /> : limits.error ? <ErrorBox error={limits.error} onRetry={() => limits.refetch()} /> : <KeyLimitsPanel limits={limits.data} />}
          </div>
        </section>
        <section aria-labelledby="kd-daily" className="border-2 border-ink">
          <div className="flex items-center justify-between gap-2 border-b border-ink px-3 py-2">
            <h3 id="kd-daily" className="text-[14px] font-bold">
              {t("keyLimits.daily")}
            </h3>
            <div role="group" aria-label={t("keyLimits.dailyRange")} className="flex border-2 border-ink">
              {[7, 30, 90].map((d) => (
                <button
                  key={d}
                  type="button"
                  aria-pressed={days === d}
                  onClick={() => setDays(d)}
                  className={days === d ? "h-7 border-l border-ink bg-yellow px-2 text-[12px] font-semibold text-on-yellow first:border-l-0" : "h-7 border-l border-ink px-2 text-[12px] font-semibold first:border-l-0 hover:bg-rule"}
                >
                  {t("keyLimits.days", { n: d })}
                </button>
              ))}
            </div>
          </div>
          <div className="p-3">
            {daily.isPending ? <Skeleton className="h-56" /> : daily.error ? <ErrorBox error={daily.error} onRetry={() => daily.refetch()} /> : <TierStackedBars data={series} height={220} description={t("charts.tierSeries", { title: t("keyLimits.daily"), ...facts })} />}
          </div>
        </section>
        <dl className="divide-y divide-rule border-y-2 border-ink text-[13px]">
          {access.map(([label, v]) => (
            <div key={label} className="grid grid-cols-[130px_1fr] gap-3 py-2">
              <dt className="label-mono self-center text-muted">{label}</dt>
              <dd className="num min-w-0 break-words">{v}</dd>
            </div>
          ))}
        </dl>
        <Button onClick={() => onEdit(k)}>
          <Pencil aria-hidden />
          {t("keyLimits.editLimits")}
        </Button>
      </div>
      <ConfirmDialog
        open={confirmReset}
        onOpenChange={setConfirmReset}
        title={t("keyLimits.resetTitle")}
        description={t("keyLimits.resetBody")}
        confirmLabel={t("keyLimits.reset")}
        onConfirm={() => reset.mutateAsync(k.id)}
      />
    </SheetContent>
  );
}
