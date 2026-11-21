import { useMemo, useState, type ReactNode } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useInfiniteQuery, useQuery, useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { FlaskConical, Trash2 } from "lucide-react";
import { ApiError, api, unwrap, type Schemas } from "@/api/client";
import { accountCostQuery, accountEstimateQuery, accountKTrendQuery, accountQuery, accountQuotaQuery, keys, putAccount, subscriptionsInfinite } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { ProgressBar } from "@/components/Progress";
import { StatusTag } from "@/components/Tags";
import { HeadroomValue } from "@/components/AccountCard";
import { PairBars, TimeLines } from "@/components/Charts";
import { EmptyState, ErrorBox } from "@/components/Feedback";
import { ValidationResult } from "@/components/ValidationResult";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog";
import { Input, Textarea } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Skeleton } from "@/components/ui/skeleton";
import { useApiMutation } from "@/lib/mutations";
import { accountKind, accountNote, reasonLabel } from "@/lib/accounts";
import { formatDate, formatDateTime, formatPercent, formatUSD } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { errorText } from "@/lib/errors";
import { shortId } from "@/lib/utils";
import { toast } from "sonner";

export const Route = createFileRoute("/_app/admin/accounts/$accountId")({
  loader: ({ context: { queryClient }, params }) => queryClient.ensureQueryData(accountQuery(params.accountId)),
  component: AccountDetail,
});

type Account = Schemas["Account"];

function AccountDetail() {
  const { t, i18n } = useTranslation();
  const { accountId } = Route.useParams();
  const { data: a } = useSuspenseQuery({ ...accountQuery(accountId), refetchInterval: 30_000 });
  const qc = useQueryClient();
  const navigate = useNavigate();
  const now = useNow(30_000);
  const [test, setTest] = useState<Schemas["AccountTestResult"] | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [blocking, setBlocking] = useState<Schemas["BlockingSubscription"][] | null>(null);

  const enable = useApiMutation({
    mutationFn: (enabled: boolean) => unwrap(api.PUT("/admin/accounts/{accountId}/enabled", { params: { path: { accountId } }, body: { enabled } })),
    onMutate: async (enabled) => {
      await qc.cancelQueries({ queryKey: keys.account(accountId) });
      const prev = qc.getQueryData<Account>(keys.account(accountId));
      if (prev) qc.setQueryData<Account>(keys.account(accountId), { ...prev, enabled });
      return { prev };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(keys.account(accountId), ctx.prev);
    },
    onSuccess: (acc) => {
      putAccount(qc, acc);
      void qc.invalidateQueries({ queryKey: keys.overview });
    },
  });
  const runTest = useApiMutation({
    mutationFn: () => unwrap(api.POST("/admin/accounts/{accountId}/test", { params: { path: { accountId } } })),
    onSuccess: (r) => {
      setTest(r);
      void qc.invalidateQueries({ queryKey: keys.account(accountId) });
      void qc.invalidateQueries({ queryKey: keys.accounts });
    },
  });
  const remove = useApiMutation({
    mutationFn: () => unwrap(api.DELETE("/admin/accounts/{accountId}", { params: { path: { accountId } } })),
    silentError: true,
    onError: (e) => {
      if (e instanceof ApiError && e.code === "account_has_subscriptions") setBlocking(e.blocking ?? []);
      else toast.error(errorText(t, e));
    },
    onSuccess: () => {
      toast.success(t("accounts.deleted"));
      void qc.invalidateQueries({ queryKey: keys.accounts });
      void qc.invalidateQueries({ queryKey: keys.overview });
      void navigate({ to: "/admin/accounts" });
    },
  });

  return (
    <>
      <PageHeader
        code="OB-09"
        title={
          <span className="flex flex-wrap items-center gap-3">
            {a.name}
            <StatusTag kind={accountKind(a)} />
          </span>
        }
        actions={
          <>
            <span className="flex items-center gap-2 border-2 border-ink px-3">
              <label htmlFor="acc-enabled" className="label-mono">
                {t("accounts.enabled")}
              </label>
              <Switch id="acc-enabled" checked={a.enabled} onCheckedChange={(v) => enable.mutate(v)} />
            </span>
            <Button onClick={() => runTest.mutate()} disabled={runTest.isPending}>
              <FlaskConical aria-hidden />
              {runTest.isPending ? t("accounts.testing") : t("accounts.test")}
            </Button>
            <Button variant="danger" onClick={() => setDeleting(true)}>
              <Trash2 aria-hidden />
              {t("common.delete")}
            </Button>
          </>
        }
      />
      <p className="-mt-3 mb-6 text-[13px] text-muted">{accountNote(a, now, i18n.language, t)}</p>
      <div className="flex flex-col gap-6">
        <div className="grid gap-6 xl:grid-cols-2">
          <ConfigForm a={a} />
          <Runtime a={a} />
        </div>
        <Charts a={a} />
        <Estimate a={a} />
        <BoundSubscriptions accountId={accountId} />
      </div>

      <Dialog open={!!test} onOpenChange={(o) => !o && setTest(null)}>
        {test ? (
          <DialogContent title={t("accounts.testResult")}>
            <ValidationResult r={test} />
            <DialogFooter>
              <Button onClick={() => setTest(null)}>{t("common.done")}</Button>
            </DialogFooter>
          </DialogContent>
        ) : null}
      </Dialog>

      <Dialog open={deleting} onOpenChange={(o) => !o && (setDeleting(false), setBlocking(null))}>
        <DialogContent title={blocking ? t("accounts.deleteBlocked") : t("accounts.deleteTitle", { name: a.name })} description={blocking ? t("accounts.deleteBlockedHint") : t("accounts.deleteBody")}>
          {blocking ? (
            <ul className="divide-y divide-rule border-y-2 border-ink">
              {blocking.map((s) => (
                <li key={s.id} className="flex flex-wrap items-center gap-3 py-2.5 text-[13px]">
                  <span className="min-w-0 flex-1">
                    <span className="font-semibold">{s.plan_name}</span>
                    <span className="num ml-2 text-muted">No.{shortId(s.id)}</span>
                    <span className="ml-2">{s.username}</span>
                  </span>
                  <span className="label-mono border border-ink px-1.5 py-0.5">{t(`subStatus.${s.status}`)}</span>
                  <Button asChild size="sm">
                    <Link to="/admin/subscriptions" search={{ tab: "all", account: a.id, rebind: s.id }}>
                      {t("accounts.goRebind")}
                    </Link>
                  </Button>
                </li>
              ))}
            </ul>
          ) : null}
          <DialogFooter>
            <Button onClick={() => (setDeleting(false), setBlocking(null))}>{blocking ? t("common.close") : t("common.cancel")}</Button>
            {!blocking ? (
              <Button variant="danger" disabled={remove.isPending} onClick={() => remove.mutate()}>
                {t("common.delete")}
              </Button>
            ) : null}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function ConfigForm({ a }: { a: Account }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const optNum = (max: number) =>
    z
      .string()
      .trim()
      .refine((v) => v === "" || (/^\d+(\.\d+)?$/.test(v) && Number(v) <= max), t("validation.range", { max }));
  const schema = z.object({
    name: z.string().trim().min(1, t("validation.required")).max(100),
    base_url: z.string().trim().url(t("validation.url")),
    cpa_key: z.string(),
    quota_token: z.string(),
    watermark: optNum(100),
    max_concurrency: z
      .string()
      .trim()
      .refine((v) => v === "" || /^\d+$/.test(v), t("validation.nonNegativeInt")),
    note: z.string().max(1000),
  });
  type Values = z.infer<typeof schema>;
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    values: {
      name: a.name,
      base_url: a.base_url,
      cpa_key: "",
      quota_token: "",
      watermark: a.watermark_5h_percent != null ? String(a.watermark_5h_percent) : "",
      max_concurrency: a.max_concurrency != null ? String(a.max_concurrency) : "",
      note: a.note,
    },
  });
  const errs = form.formState.errors;
  const save = useApiMutation({
    mutationFn: (v: Values) =>
      unwrap(
        api.PATCH("/admin/accounts/{accountId}", {
          params: { path: { accountId: a.id } },
          body: {
            name: v.name,
            base_url: v.base_url,
            ...(v.cpa_key ? { cpa_key: v.cpa_key } : {}),
            ...(v.quota_token ? { quota_token: v.quota_token } : {}),
            watermark_5h_percent: v.watermark === "" ? null : Number(v.watermark),
            max_concurrency: v.max_concurrency === "" ? null : Number(v.max_concurrency),
            note: v.note,
          },
        }),
      ),
    success: t("accounts.saved"),
    onSuccess: (acc) => putAccount(qc, acc),
  });
  return (
    <Section n={1} title={t("accounts.config")}>
      <form onSubmit={form.handleSubmit((v) => save.mutate(v))} noValidate className="flex flex-col gap-4">
        <Field label={t("accounts.name")} htmlFor="ac-name" error={errs.name?.message}>
          <Input id="ac-name" aria-invalid={!!errs.name} {...form.register("name")} />
        </Field>
        <Field label={t("accounts.baseUrl")} htmlFor="ac-url" error={errs.base_url?.message}>
          <Input id="ac-url" className="num" aria-invalid={!!errs.base_url} {...form.register("base_url")} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("accounts.cpaKey")} htmlFor="ac-key" hint={t("accounts.secretKeep")}>
            <Input id="ac-key" type="password" autoComplete="off" className="num" {...form.register("cpa_key")} />
          </Field>
          <Field label={t("accounts.quotaToken")} htmlFor="ac-token" hint={t("accounts.secretKeep")}>
            <Input id="ac-token" type="password" autoComplete="off" className="num" {...form.register("quota_token")} />
          </Field>
          <Field label={t("accounts.watermark")} htmlFor="ac-wm" hint={t("accounts.watermarkHint", { v: formatPercent(a.watermark_5h_percent_effective, 0) })} error={errs.watermark?.message}>
            <Input id="ac-wm" inputMode="decimal" className="num" aria-invalid={!!errs.watermark} {...form.register("watermark")} />
          </Field>
          <Field label={t("accounts.maxConcurrency")} htmlFor="ac-conc" hint={t("keys.emptyUnlimited")} error={errs.max_concurrency?.message}>
            <Input id="ac-conc" inputMode="numeric" className="num" aria-invalid={!!errs.max_concurrency} {...form.register("max_concurrency")} />
          </Field>
        </div>
        <Field label={t("users.note")} htmlFor="ac-note">
          <Textarea id="ac-note" rows={2} {...form.register("note")} />
        </Field>
        <p className="text-[12px] text-muted">{t("accounts.saveValidates")}</p>
        <div>
          <Button type="submit" variant="primary" disabled={save.isPending || !form.formState.isDirty}>
            {save.isPending ? t("accounts.validating") : t("common.save")}
          </Button>
        </div>
      </form>
    </Section>
  );
}

function Runtime({ a }: { a: Account }) {
  const { t } = useTranslation();
  const rt = a.runtime;
  const rows: [string, ReactNode][] = [
    [t("accounts.rt.status"), <StatusTag kind={accountKind(a)} />],
    [t("accounts.rt.reason"), <ReasonText key="r" code={rt.status_reason_code} detail={rt.status_reason} />],
    [t("accounts.rt.state"), rt.state || "—"],
    [t("accounts.rt.safety"), rt.safety_factor != null ? rt.safety_factor.toFixed(2) : "—"],
    [t("accounts.rt.observed"), formatDateTime(rt.observed_at)],
    [t("accounts.rt.fetched"), formatDateTime(rt.fetched_at)],
    [t("accounts.rt.cooldown"), formatDateTime(rt.cooldown_until)],
    [t("accounts.rt.weight"), rt.weight != null ? rt.weight.toFixed(3) : "—"],
    [t("accounts.headroom"), <HeadroomValue key="h" micro={rt.headroom_micro} />],
    [t("accounts.reserved"), formatUSD(rt.reserved_micro)],
    [t("accounts.rt.spend"), formatUSD(rt.spend_since_observed_micro)],
    [t("accounts.rt.sold"), `${formatUSD(a.sold_upstream_micro)} / ${a.week_total_micro != null ? formatUSD(a.week_total_micro) : "—"}`],
    [t("accounts.concurrency"), `${rt.current_concurrency} / ${a.max_concurrency ?? "∞"}`],
    [t("accounts.rt.database"), `${rt.database_status || "—"} · ${rt.estimates_loaded ? t("accounts.rt.estimatesLoaded") : t("accounts.rt.estimatesMissing")}`],
    [t("accounts.rt.lastError"), rt.last_error || "—"],
    [t("accounts.rt.models"), rt.models.join(", ") || "—"],
  ];
  const win = (w: Schemas["QuotaWindow"] | null | undefined, label: string, watermark?: number) => (
    <div>
      <div className="num mb-1 flex justify-between text-[12px]">
        <span className="label-mono">{label}</span>
        <span>
          {w ? `${formatPercent(w.used_percent)} · ${t("accounts.remainingOf", { remaining: formatUSD(w.remaining_usd * 1e6), total: formatUSD(w.total_usd * 1e6) })}` : "—"}
        </span>
      </div>
      <ProgressBar value={w?.used_percent ?? 0} watermark={watermark} label={label} />
      {w?.reset_at ? <div className="num mt-1 text-[11px] text-muted">{t("accounts.resetAt", { time: formatDateTime(w.reset_at) })}</div> : null}
    </div>
  );
  return (
    <Section n={2} title={t("accounts.runtime")}>
      <div className="mb-4 flex flex-col gap-4">
        {win(rt.five_hour, "5H", a.watermark_5h_percent_effective)}
        {win(rt.week, "7D")}
      </div>
      <dl className="divide-y divide-rule border-y-2 border-ink text-[13px]">
        {rows.map(([k, v]) => (
          <div key={k} className="grid grid-cols-[140px_1fr] gap-3 py-1.5">
            <dt className="label-mono self-center text-muted">{k}</dt>
            <dd className="num min-w-0 break-words">{v}</dd>
          </div>
        ))}
      </dl>
    </Section>
  );
}

/** Translated reason code (never the raw code), unknown codes as "其他原因" + the code in small mono; the raw detail below. */
function ReasonText({ code, detail }: { code: string; detail: string }) {
  const { t } = useTranslation();
  if (!code) return <>—</>;
  const r = reasonLabel(t, code);
  return (
    <span className="flex flex-col">
      <span className="font-sans">
        {r.text}
        {r.unknown ? <code className="num ml-1.5 text-[11px] text-muted">{code}</code> : null}
      </span>
      {detail ? <span className="text-[11px] break-words text-muted">{detail}</span> : null}
    </span>
  );
}

function ChartBox({ title, q, children }: { title: string; q: { isPending: boolean; error: unknown; refetch: () => unknown }; children: ReactNode }) {
  return (
    <div className="border border-ink">
      <h3 className="label-mono border-b border-ink px-3 py-2">{title}</h3>
      <div className="p-3">{q.isPending ? <Skeleton className="h-56" /> : q.error ? <ErrorBox error={q.error} onRetry={() => q.refetch()} /> : children}</div>
    </div>
  );
}

function Charts({ a }: { a: Account }) {
  const { t } = useTranslation();
  const q5 = useQuery(accountQuotaQuery(a.id, "5h"));
  const q7 = useQuery(accountQuotaQuery(a.id, "7d"));
  const k = useQuery(accountKTrendQuery(a.id, 30));
  const range = useMemo(() => {
    const to = new Date();
    to.setMinutes(0, 0, 0);
    to.setHours(to.getHours() + 1);
    return { from: new Date(to.getTime() - 14 * 86_400_000).toISOString(), to: to.toISOString() };
  }, []);
  const cost = useQuery(accountCostQuery(a.id, { from: range.from, to: range.to, bucket: "day" }));

  const series = (s: Schemas["QuotaSeries"] | undefined) => {
    const pts = (s?.items ?? []).map((p) => ({ t: new Date(p.at).getTime(), used: p.used_percent }));
    const resets = [...new Set((s?.items ?? []).map((p) => p.reset_at).filter((x): x is string => !!x))]
      .map((r) => new Date(r).getTime())
      .filter((r) => pts.length && r >= pts[0]!.t && r <= pts[pts.length - 1]!.t);
    return { pts, marks: resets.map((r) => ({ t: r, label: t("accounts.reset") })) };
  };
  const s5 = series(q5.data);
  const s7 = series(q7.data);
  const kp = (k.data?.items ?? []).filter((d) => d.k != null).map((d) => ({ t: new Date(`${d.day}T00:00:00Z`).getTime(), k: d.k as number }));
  const cp = (cost.data?.items ?? []).map((r) => ({ label: formatDate(r.bucket).slice(5), a: r.cpa_quota_cost_micro, b: r.bridge_upstream_cost_micro }));
  const pct = (v: number) => `${Math.round(v)}%`;
  return (
    <Section n={3} title={t("accounts.charts")}>
      <div className="grid gap-4 lg:grid-cols-2">
        <ChartBox title={t("accounts.chart5h")} q={q5}>
          {s5.pts.length ? (
            <TimeLines data={s5.pts} series={[{ key: "used", name: t("accounts.used"), color: "ink" }]} yFormat={pct} yDomain={[0, 100]} marks={s5.marks} description={t("charts.quota", { window: "5h", n: s5.pts.length, resets: s5.marks.length })} />
          ) : (
            <EmptyState title={t("accounts.noSeries")} />
          )}
        </ChartBox>
        <ChartBox title={t("accounts.chart7d")} q={q7}>
          {s7.pts.length ? (
            <TimeLines data={s7.pts} series={[{ key: "used", name: t("accounts.used"), color: "ink" }]} yFormat={pct} yDomain={[0, 100]} marks={s7.marks} description={t("charts.quota", { window: "7d", n: s7.pts.length, resets: s7.marks.length })} />
          ) : (
            <EmptyState title={t("accounts.noSeries")} />
          )}
        </ChartBox>
        <ChartBox title={t("accounts.chartK")} q={k}>
          {kp.length ? (
            <TimeLines dateOnly data={kp} series={[{ key: "k", name: "k", color: "ink" }]} yFormat={(v) => v.toFixed(2)} description={t("charts.k", { n: kp.length, last: kp[kp.length - 1]!.k.toFixed(2) })} />
          ) : (
            <EmptyState title={t("accounts.noSeries")} />
          )}
        </ChartBox>
        <ChartBox title={t("accounts.chartCost")} q={cost}>
          {cp.length ? (
            <PairBars
              data={cp}
              a="cpa-quota"
              b={t("accounts.bridge")}
              description={t("charts.cost", { a: formatUSD(cost.data?.cpa_quota_total_micro ?? 0), b: formatUSD(cost.data?.bridge_total_micro ?? 0), diff: formatUSD(cost.data?.diff_micro ?? 0) })}
            />
          ) : (
            <EmptyState title={t("accounts.noSeries")} />
          )}
          {cost.data ? (
            <p className="num mt-2 text-[12px] text-muted">
              {t("charts.cost", { a: formatUSD(cost.data.cpa_quota_total_micro), b: formatUSD(cost.data.bridge_total_micro), diff: formatUSD(cost.data.diff_micro) })}
            </p>
          ) : null}
        </ChartBox>
      </div>
    </Section>
  );
}

function Estimate({ a }: { a: Account }) {
  const { t } = useTranslation();
  const q = useQuery(accountEstimateQuery(a.id));
  const e = q.data;
  return (
    <Section n={4} title={t("accounts.estimate")}>
      {q.isPending ? (
        <Skeleton className="h-32" />
      ) : q.error ? (
        q.error instanceof ApiError && q.error.code === "not_found" ? (
          <EmptyState title={t("accounts.noEstimate")} description={t("accounts.noEstimateHint")} />
        ) : (
          <ErrorBox error={q.error} onRetry={() => q.refetch()} />
        )
      ) : e ? (
        <>
          <dl className="grid grid-cols-2 border-2 border-ink text-[13px] sm:grid-cols-4">
            {(
              [
                ["state", e.state + (e.pause_reason ? ` (${e.pause_reason})` : "")],
                ["k", e.k != null ? `${e.k.toFixed(3)} · ${e.k_source}` : `— · ${e.k_source}`],
                ["safety", e.safety_factor.toFixed(2)],
                ["basis", `${e.basis} · ${e.segment_window}`],
                ["5h used", `${formatPercent(e.used_5h)} / $${e.five_hour_total_usd.toFixed(2)}`],
                ["7d used", `${formatPercent(e.used_7d)} / $${e.weekly_total_usd.toFixed(2)}`],
                ["remaining 5h", `$${e.remaining_five_hour_usd.toFixed(2)}`],
                ["remaining 7d", `$${e.remaining_week_usd.toFixed(2)}`],
                ["blocks", `${e.clean_blocks}/${e.total_blocks}`],
                ["segments", `${e.segments} (−${e.excluded_segments})`],
                ["ratio", e.ratio.toFixed(3)],
                ["estimated", formatDateTime(e.estimated_at)],
              ] as const
            ).map(([k, v], i) => (
              <div key={k} className={`border-ink p-2 ${i % 2 ? "border-l" : ""} ${i >= 2 ? "border-t" : ""} sm:border-l sm:[&:nth-child(4n+1)]:border-l-0 sm:[&:nth-child(-n+4)]:border-t-0`}>
                <dt className="label-mono text-muted">{k}</dt>
                <dd className="num mt-0.5">{v}</dd>
              </div>
            ))}
          </dl>
          <details className="mt-4 border border-ink">
            <summary className="cursor-pointer px-3 py-2 text-[13px] font-semibold">{t("accounts.rawJson")}</summary>
            <pre className="num max-h-96 overflow-auto border-t border-ink p-3 text-[12px]">{JSON.stringify(e.detail, null, 2)}</pre>
          </details>
        </>
      ) : null}
    </Section>
  );
}

function BoundSubscriptions({ accountId }: { accountId: string }) {
  const { t } = useTranslation();
  const q = useInfiniteQuery(subscriptionsInfinite({ account_id: accountId, limit: 100 }));
  const rows = useMemo(() => (q.data?.pages.flatMap((p) => p.items) ?? []).filter((s) => s.status === "active" || s.status === "pending_assignment"), [q.data]);
  return (
    <Section
      n={5}
      title={t("accounts.bound")}
      aside={
        <Link to="/admin/subscriptions" search={{ tab: "all", account: accountId }} className="text-[13px] font-semibold underline underline-offset-4">
          {t("common.viewAll")}
        </Link>
      }
      bodyClassName="p-0"
    >
      {q.isPending ? (
        <Skeleton className="m-4 h-16" />
      ) : rows.length === 0 ? (
        <EmptyState className="m-4" title={t("accounts.noBound")} />
      ) : (
        <ul className="divide-y divide-rule">
          {rows.map((s) => (
            <li key={s.id} className="grid gap-1 px-4 py-3 text-[13px] sm:grid-cols-[1fr_auto_auto_auto] sm:items-center sm:gap-4">
              <span>
                <span className="font-semibold">{s.plan_name}</span>
                <span className="num ml-2 text-muted">No.{shortId(s.id)}</span>
              </span>
              <Link to="/admin/users/$userId" params={{ userId: s.user_id }} className="underline underline-offset-4">
                {s.username || shortId(s.user_id)}
              </Link>
              <span className="num">{formatUSD(s.weekly_quota_micro)} / {t("plans.week")}</span>
              <span className="label-mono border border-ink px-1.5 py-0.5">{t(`subStatus.${s.status}`)}</span>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}
