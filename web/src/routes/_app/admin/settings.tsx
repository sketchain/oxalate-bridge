import { useMemo } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useFieldArray, useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Plus, RefreshCw, Scale, Trash2 } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { keys, modelsQuery, overviewQuery, pricesQuery, settingsQuery } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { ErrorBox } from "@/components/Feedback";
import { Button } from "@/components/ui/button";
import { Input, NativeSelect } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { useApiMutation } from "@/lib/mutations";
import { DURATION_FACTORS, formatDateTime, formatDuration, formatUSD, microToInput, parseUSD, toDuration, type DurationUnit } from "@/lib/format";

export const Route = createFileRoute("/_app/admin/settings")({
  loader: ({ context: { queryClient } }) => queryClient.ensureQueryData(settingsQuery),
  component: AdminSettings,
});

type Settings = Schemas["Settings"];
type NumKey = { [K in keyof Settings]: Settings[K] extends number ? K : never }[keyof Settings];

/** How a numeric setting is typed in and shown. Durations are stored in `base` units. */
type Spec =
  | { key: NumKey; kind: "usd" }
  | { key: NumKey; kind: "int"; min?: number; max?: number }
  | { key: NumKey; kind: "float"; min?: number; max?: number; step?: string }
  | { key: NumKey; kind: "duration"; base: "s" | "min" | "h" };

const UNITS: DurationUnit[] = ["s", "min", "h", "d"];
const BASE_SECONDS = { s: 1, min: 60, h: 3600 } as const;

function useSave() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  return useApiMutation({
    mutationFn: (patch: Schemas["SettingsPatch"]) => unwrap(api.PATCH("/admin/settings", { body: patch })),
    success: t("adminSettings.saved"),
    onSuccess: (s) => {
      qc.setQueryData(keys.settings, s);
      void qc.invalidateQueries({ queryKey: keys.availability });
    },
  });
}

/** A group of numeric settings as one form; only changed fields are sent. */
function NumberGroup({ n, title, settings, specs, note }: { n: number; title: string; settings: Settings; specs: Spec[]; note?: string }) {
  const { t } = useTranslation();
  const save = useSave();
  const initial = useMemo(() => {
    const v: Record<string, string> = {};
    for (const s of specs) {
      const raw = settings[s.key];
      if (s.kind === "usd") v[s.key] = microToInput(raw);
      else if (s.kind === "duration") {
        const d = toDuration(raw * BASE_SECONDS[s.base]);
        v[s.key] = String(d.value);
        v[`${s.key}__unit`] = d.unit;
      } else v[s.key] = String(raw);
    }
    return v;
  }, [settings, specs]);
  const schema = z.record(z.string(), z.string()).superRefine((vals, ctx) => {
    for (const s of specs) {
      const v = (vals[s.key] ?? "").trim();
      const bad = (message: string) => ctx.addIssue({ code: "custom", path: [s.key], message });
      if (s.kind === "usd") {
        if (!Number.isFinite(parseUSD(v)) || parseUSD(v) < 0) bad(t("validation.amountNonNegative"));
      } else if (s.kind === "int" || s.kind === "duration") {
        if (!/^\d+(\.\d+)?$/.test(v)) bad(t("validation.nonNegative"));
        else if (s.kind === "int" && !/^\d+$/.test(v)) bad(t("validation.nonNegativeInt"));
        else if (s.kind === "int" && ((s.min !== undefined && Number(v) < s.min) || (s.max !== undefined && Number(v) > s.max))) bad(t("validation.between", { min: s.min ?? 0, max: s.max ?? "∞" }));
      } else if (s.kind === "float") {
        if (!/^\d+(\.\d+)?$/.test(v)) bad(t("validation.nonNegative"));
        else if ((s.min !== undefined && Number(v) < s.min) || (s.max !== undefined && Number(v) > s.max)) bad(t("validation.between", { min: s.min ?? 0, max: s.max ?? "∞" }));
      }
    }
  });
  const form = useForm<Record<string, string>>({ resolver: zodResolver(schema), values: initial });
  const errs = form.formState.errors;
  const submit = form.handleSubmit((v) => {
    const patch: Record<string, number> = {};
    for (const s of specs) {
      let num: number;
      if (s.kind === "usd") num = parseUSD(v[s.key]!);
      else if (s.kind === "duration") {
        const seconds = Number(v[s.key]) * DURATION_FACTORS[(v[`${s.key}__unit`] ?? "s") as DurationUnit];
        num = Math.round(seconds / BASE_SECONDS[s.base]);
      } else num = Number(v[s.key]);
      if (num !== settings[s.key]) patch[s.key] = num;
    }
    if (Object.keys(patch).length) save.mutate(patch as Schemas["SettingsPatch"]);
  });
  return (
    <Section n={n} title={title}>
      <form onSubmit={submit} noValidate className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          {specs.map((s) => {
            const id = `set-${s.key}`;
            const err = errs[s.key]?.message as string | undefined;
            const current =
              s.kind === "usd" ? formatUSD(settings[s.key]) : s.kind === "duration" ? formatDuration(settings[s.key] * BASE_SECONDS[s.base]) : String(settings[s.key]);
            return (
              <Field key={s.key} label={t(`adminSettings.f.${s.key}`)} htmlFor={id} hint={t("adminSettings.current", { value: current })} error={err}>
                {s.kind === "duration" ? (
                  <div className="flex gap-2">
                    <Input id={id} inputMode="decimal" className="num" aria-invalid={!!err} {...form.register(s.key)} />
                    <NativeSelect aria-label={t("adminSettings.unit")} className="w-24" {...form.register(`${s.key}__unit`)}>
                      {UNITS.map((u) => (
                        <option key={u} value={u}>
                          {t(`adminSettings.units.${u}`)}
                        </option>
                      ))}
                    </NativeSelect>
                  </div>
                ) : (
                  <div className="relative">
                    {s.kind === "usd" ? <span className="num pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-muted">$</span> : null}
                    <Input id={id} inputMode="decimal" className={s.kind === "usd" ? "num pl-6" : "num"} aria-invalid={!!err} {...form.register(s.key)} />
                  </div>
                )}
              </Field>
            );
          })}
        </div>
        {note ? <p className="text-[12px] text-muted">{note}</p> : null}
        <div>
          <Button type="submit" variant="primary" disabled={save.isPending || !form.formState.isDirty}>
            {t("common.save")}
          </Button>
        </div>
      </form>
    </Section>
  );
}

function Multipliers({ settings }: { settings: Settings }) {
  const { t } = useTranslation();
  const save = useSave();
  const models = useQuery(modelsQuery);
  const mult = z.string().refine((v) => /^\d+(\.\d+)?$/.test(v.trim()) && Number(v) >= 0 && Number(v) <= 100, t("validation.between", { min: 0, max: 100 }));
  const schema = z
    .object({
      plan: mult,
      standard: mult,
      force: mult,
      models: z.array(z.object({ model: z.string().trim().min(1, t("validation.required")), value: mult })),
    })
    .superRefine((v, ctx) => {
      const seen = new Set<string>();
      v.models.forEach((m, i) => {
        if (seen.has(m.model.trim())) ctx.addIssue({ code: "custom", path: ["models", i, "model"], message: t("validation.duplicate") });
        seen.add(m.model.trim());
      });
    });
  type Values = z.infer<typeof schema>;
  const initial: Values = useMemo(
    () => ({
      plan: String(settings.tier_multipliers.plan),
      standard: String(settings.tier_multipliers.standard),
      force: String(settings.tier_multipliers.force),
      models: Object.entries(settings.model_multipliers)
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([model, value]) => ({ model, value: String(value) })),
    }),
    [settings],
  );
  const form = useForm<Values>({ resolver: zodResolver(schema), values: initial });
  const rows = useFieldArray({ control: form.control, name: "models" });
  const errs = form.formState.errors;
  return (
    <Section n={1} title={t("adminSettings.multipliers")}>
      <form
        noValidate
        className="flex flex-col gap-5"
        onSubmit={form.handleSubmit((v) =>
          save.mutate({
            tier_multipliers: { plan: Number(v.plan), standard: Number(v.standard), force: Number(v.force) },
            model_multipliers: Object.fromEntries(v.models.map((m) => [m.model.trim(), Number(m.value)])),
          }),
        )}
      >
        <div className="grid gap-4 sm:grid-cols-3">
          {(["plan", "standard", "force"] as const).map((k) => (
            <Field key={k} label={t(`adminSettings.tierMult`, { tier: t(`tier.${k}`) })} htmlFor={`mult-${k}`} error={errs[k]?.message}>
              <Input id={`mult-${k}`} inputMode="decimal" className="num" aria-invalid={!!errs[k]} {...form.register(k)} />
            </Field>
          ))}
        </div>
        <div>
          <h3 className="label-mono mb-2" id="model-mult-label">
            {t("adminSettings.modelMult")}
          </h3>
          <table className="w-full text-[13px]" aria-labelledby="model-mult-label">
            <thead>
              <tr className="border-t-2 border-b border-ink">
                <th scope="col" className="label-mono px-2 py-2 text-left">
                  {t("usage.model")}
                </th>
                <th scope="col" className="label-mono w-32 px-2 py-2 text-left">
                  {t("adminSettings.multiplier")}
                </th>
                <th scope="col" className="w-12">
                  <span className="sr-only">{t("common.actions")}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.fields.length === 0 ? (
                <tr className="border-b border-rule">
                  <td colSpan={3} className="px-2 py-3 text-muted">
                    {t("adminSettings.noModelMult")}
                  </td>
                </tr>
              ) : null}
              {rows.fields.map((f, i) => (
                <tr key={f.id} className="border-b border-rule align-top">
                  <td className="px-2 py-2">
                    <Input
                      aria-label={t("adminSettings.modelName", { n: i + 1 })}
                      list="settings-models"
                      className="num h-9"
                      aria-invalid={!!errs.models?.[i]?.model}
                      {...form.register(`models.${i}.model`)}
                    />
                    {errs.models?.[i]?.model ? <p className="mt-1 text-[12px] font-semibold">{errs.models[i]!.model!.message}</p> : null}
                  </td>
                  <td className="px-2 py-2">
                    <Input aria-label={t("adminSettings.multiplierFor", { n: i + 1 })} inputMode="decimal" className="num h-9" aria-invalid={!!errs.models?.[i]?.value} {...form.register(`models.${i}.value`)} />
                    {errs.models?.[i]?.value ? <p className="mt-1 text-[12px] font-semibold">{errs.models[i]!.value!.message}</p> : null}
                  </td>
                  <td className="px-2 py-2">
                    <Button variant="ghost" size="icon-sm" aria-label={t("adminSettings.removeRow", { n: i + 1 })} onClick={() => rows.remove(i)}>
                      <Trash2 aria-hidden />
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <datalist id="settings-models">
            {models.data?.items.map((m) => (
              <option key={m} value={m} />
            ))}
          </datalist>
          <Button size="sm" className="mt-3" onClick={() => rows.append({ model: "", value: "1" })}>
            <Plus aria-hidden />
            {t("adminSettings.addRow")}
          </Button>
        </div>
        <div>
          <Button type="submit" variant="primary" disabled={save.isPending || !form.formState.isDirty}>
            {t("common.save")}
          </Button>
        </div>
      </form>
    </Section>
  );
}

function PricesAndReconcile({ settings }: { settings: Settings }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const prices = useQuery(pricesQuery);
  const overview = useQuery(overviewQuery);
  const save = useSave();
  const form = useForm<{ url: string }>({
    resolver: zodResolver(z.object({ url: z.string().trim().url(t("validation.url")) })),
    values: { url: settings.price_url },
  });
  const refresh = useApiMutation({
    mutationFn: () => unwrap(api.POST("/admin/prices/refresh")),
    success: t("adminSettings.pricesRefreshed"),
    onSuccess: (p) => qc.setQueryData(keys.prices, p),
  });
  const reconcile = useApiMutation({
    mutationFn: () => unwrap(api.POST("/admin/reconcile")),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.overview }),
    success: t("adminOverview.reconcileDone"),
  });
  const p = prices.data;
  const r = overview.data?.last_reconcile;
  return (
    <div className="grid gap-6 xl:grid-cols-2">
      <Section
        n={5}
        title={t("adminSettings.prices")}
        aside={
          <Button size="sm" onClick={() => refresh.mutate()} disabled={refresh.isPending}>
            <RefreshCw aria-hidden />
            {t("adminSettings.refreshNow")}
          </Button>
        }
      >
        {prices.error ? <ErrorBox error={prices.error} onRetry={() => prices.refetch()} /> : null}
        <dl className="mb-4 divide-y divide-rule border-y-2 border-ink text-[13px]">
          {(
            [
              [t("adminSettings.priceOrigin"), p ? t(`adminSettings.origin.${p.origin}`) : "—"],
              [t("adminSettings.priceModels"), p ? String(p.models) : "—"],
              [t("adminSettings.priceFetched"), formatDateTime(p?.fetched_at)],
              [t("adminSettings.priceAttempt"), formatDateTime(p?.last_attempt_at)],
              [t("adminSettings.priceError"), p?.last_error || "—"],
            ] as const
          ).map(([k, v]) => (
            <div key={k} className="grid grid-cols-[130px_1fr] gap-3 py-2">
              <dt className="label-mono self-center text-muted">{k}</dt>
              <dd className="num break-all">{v}</dd>
            </div>
          ))}
        </dl>
        <form noValidate className="flex flex-col gap-3" onSubmit={form.handleSubmit((v) => v.url !== settings.price_url && save.mutate({ price_url: v.url }))}>
          <Field label={t("adminSettings.f.price_url")} htmlFor="price-url" error={form.formState.errors.url?.message}>
            <Input id="price-url" className="num" aria-invalid={!!form.formState.errors.url} {...form.register("url")} />
          </Field>
          <div>
            <Button type="submit" variant="primary" disabled={save.isPending || !form.formState.isDirty}>
              {t("common.save")}
            </Button>
          </div>
        </form>
      </Section>
      <Section
        n={6}
        title={t("adminOverview.reconcile")}
        aside={
          <Button size="sm" onClick={() => reconcile.mutate()} disabled={reconcile.isPending}>
            <Scale aria-hidden />
            {t("adminOverview.reconcileNow")}
          </Button>
        }
      >
        {overview.isPending ? (
          <Skeleton className="h-20" />
        ) : r ? (
          <dl className="divide-y divide-rule border-y-2 border-ink text-[13px]">
            {(
              [
                [t("adminSettings.reconcileResult"), <span className={r.ok ? "label-mono bg-ink px-1.5 py-0.5 text-bg" : "label-mono bg-red px-1.5 py-0.5 text-on-red"}>{r.ok ? t("adminOverview.reconcileOk") : t("adminOverview.reconcileBad")}</span>],
                [t("adminSettings.reconcileAt"), formatDateTime(r.finished_at)],
                [t("adminSettings.balanceMismatches"), String(r.balance_mismatches)],
                [t("adminSettings.chargeDiff"), formatUSD(r.charge_diff_micro)],
              ] as const
            ).map(([k, v]) => (
              <div key={k} className="grid grid-cols-[130px_1fr] gap-3 py-2">
                <dt className="label-mono self-center text-muted">{k}</dt>
                <dd className="num">{v}</dd>
              </div>
            ))}
          </dl>
        ) : (
          <p className="text-[13px] text-muted">{t("adminOverview.neverReconciled")}</p>
        )}
      </Section>
    </div>
  );
}

function AdminSettings() {
  const { t } = useTranslation();
  const q = useQuery(settingsQuery);
  if (q.error) return <ErrorBox error={q.error} onRetry={() => q.refetch()} />;
  const s = q.data;
  return (
    <>
      <PageHeader code="OB-16" title={t("adminSettings.title")} />
      {!s ? (
        <Skeleton className="h-96" />
      ) : (
        <div className="flex flex-col gap-6">
          <Multipliers settings={s} />
          <div className="grid gap-6 xl:grid-cols-2">
            <NumberGroup
              n={2}
              title={t("adminSettings.routing")}
              settings={s}
              specs={[
                { key: "watermark_5h_percent", kind: "float", min: 0, max: 100 },
                { key: "watermark_penalty", kind: "float", min: 0, max: 1 },
                { key: "standard_min_headroom_micro", kind: "usd" },
                { key: "sticky_ttl_seconds", kind: "duration", base: "s" },
                { key: "sticky_ttl_1h_seconds", kind: "duration", base: "s" },
              ]}
            />
            <NumberGroup
              n={3}
              title={t("adminSettings.holds")}
              settings={s}
              specs={[
                { key: "hold_cap_micro", kind: "usd" },
                { key: "hold_default_max_tokens", kind: "int", min: 1 },
                { key: "max_retries", kind: "int", min: 0, max: 10 },
                { key: "default_user_concurrency", kind: "int", min: 0 },
              ]}
            />
          </div>
          <NumberGroup
            n={4}
            title={t("adminSettings.sync")}
            settings={s}
            note={t("adminSettings.syncNote")}
            specs={[
              { key: "quota_poll_seconds", kind: "duration", base: "s" },
              { key: "stale_after_seconds", kind: "duration", base: "s" },
              { key: "models_poll_minutes", kind: "duration", base: "min" },
              { key: "price_refresh_hours", kind: "duration", base: "h" },
              { key: "upstream_idle_timeout_seconds", kind: "duration", base: "s" },
              { key: "auto_renew_lead_hours", kind: "duration", base: "h" },
              { key: "cooldown_5xx_seconds", kind: "duration", base: "s" },
              { key: "cooldown_429_default_seconds", kind: "duration", base: "s" },
            ]}
          />
          <PricesAndReconcile settings={s} />
        </div>
      )}
    </>
  );
}
