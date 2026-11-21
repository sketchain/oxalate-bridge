import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { RefreshCw, ShoppingCart } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { balanceQuery, keys, plansOnSaleQuery, subscriptionsQuery } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { PlanCard } from "@/components/PlanCard";
import { EmptyState, ErrorBox } from "@/components/Feedback";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { Checkbox, Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { useApiMutation } from "@/lib/mutations";
import { formatUSD } from "@/lib/format";

export const Route = createFileRoute("/_app/plans")({
  loader: ({ context: { queryClient } }) => {
    void queryClient.prefetchQuery(subscriptionsQuery);
    void queryClient.prefetchQuery(plansOnSaleQuery);
    void queryClient.prefetchQuery(balanceQuery);
  },
  component: PlansPage,
});

type Sub = Schemas["MySubscription"];

/** Price / balance lines shown before paying. */
function PayLines({ price, balance }: { price: number; balance: number }) {
  const { t } = useTranslation();
  const after = balance - price;
  return (
    <dl className="mt-1 divide-y divide-rule border-y-2 border-ink text-[14px]">
      {(
        [
          [t("plans.price"), formatUSD(price)],
          [t("plans.balanceNow"), formatUSD(balance)],
          [t("plans.balanceAfter"), formatUSD(after)],
        ] as const
      ).map(([k, v], i) => (
        <div key={k} className={i === 2 ? "flex justify-between py-2 font-bold" : "flex justify-between py-2"}>
          <dt>{k}</dt>
          <dd className="num">{v}</dd>
        </div>
      ))}
      {after < 0 ? (
        <p role="alert" className="py-2 text-[13px] font-semibold">
          <span className="label-mono mr-1.5 bg-red px-1 text-on-red">!</span>
          {t("errors.insufficient_balance")}
        </p>
      ) : null}
    </dl>
  );
}

function PlansPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const subs = useQuery(subscriptionsQuery);
  const plans = useQuery(plansOnSaleQuery);
  const balance = useQuery(balanceQuery);
  const [buying, setBuying] = useState<Schemas["Plan"] | null>(null);
  const [autoRenew, setAutoRenew] = useState(false);
  const [renewing, setRenewing] = useState<Sub | null>(null);
  const available = balance.data?.available_micro ?? 0;

  const afterMoney = () => {
    void qc.invalidateQueries({ queryKey: keys.subscriptions });
    void qc.invalidateQueries({ queryKey: keys.balance });
    void qc.invalidateQueries({ queryKey: keys.availability });
    void qc.invalidateQueries({ queryKey: ["me", "ledger"] });
    void qc.invalidateQueries({ queryKey: keys.me });
  };

  const purchase = useApiMutation({
    mutationFn: (v: { id: string; auto: boolean }) => unwrap(api.POST("/plans/{planId}/purchase", { params: { path: { planId: v.id } }, body: { auto_renew: v.auto } })),
    success: t("plans.purchased"),
    onSuccess: afterMoney,
  });
  const renew = useApiMutation({
    mutationFn: (id: string) => unwrap(api.POST("/subscriptions/{subscriptionId}/renew", { params: { path: { subscriptionId: id } } })),
    success: t("plans.renewed"),
    onSuccess: afterMoney,
  });
  const auto = useApiMutation({
    mutationFn: (v: { id: string; on: boolean }) =>
      unwrap(api.PUT("/subscriptions/{subscriptionId}/auto-renew", { params: { path: { subscriptionId: v.id } }, body: { auto_renew: v.on } })),
    onMutate: async (v) => {
      await qc.cancelQueries({ queryKey: keys.subscriptions });
      const prev = qc.getQueryData<Schemas["MySubscriptionList"]>(keys.subscriptions);
      qc.setQueryData<Schemas["MySubscriptionList"]>(keys.subscriptions, (old) => (old ? { items: old.items.map((s) => (s.id === v.id ? { ...s, auto_renew: v.on } : s)) } : old));
      return { prev };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(keys.subscriptions, ctx.prev);
    },
    onSettled: () => qc.invalidateQueries({ queryKey: keys.subscriptions }),
  });

  const current = (subs.data?.items ?? []).filter((s) => s.status === "active" || s.status === "pending_assignment");
  const past = (subs.data?.items ?? []).filter((s) => s.status === "expired" || s.status === "cancelled");

  const card = (s: Sub) => (
    <PlanCard
      key={s.id}
      sub={s}
      extra={
        s.status === "active" ? (
          <div className="flex items-center justify-between gap-3 px-4 py-3 text-[13px]">
            <Label htmlFor={`auto-${s.id}`} className="text-muted">
              {t("plans.autoRenew")}
            </Label>
            <Switch id={`auto-${s.id}`} checked={s.auto_renew} onCheckedChange={(on) => auto.mutate({ id: s.id, on })} />
          </div>
        ) : null
      }
      actions={
        s.status === "active" || s.status === "expired" ? (
          <Button variant="primary" onClick={() => setRenewing(s)}>
            <RefreshCw aria-hidden />
            {t("plans.renew")}
          </Button>
        ) : undefined
      }
    />
  );

  return (
    <>
      <PageHeader code="OB-04" title={t("plans.title")} />
      <div className="flex flex-col gap-6">
        <Section n={1} title={t("plans.mine")}>
          {subs.isPending ? (
            <Skeleton className="h-72" />
          ) : subs.error ? (
            <ErrorBox error={subs.error} onRetry={() => subs.refetch()} />
          ) : current.length === 0 ? (
            <EmptyState title={t("overview.noPlanTitle")} description={t("plans.noneHint")} />
          ) : (
            <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">{current.map(card)}</div>
          )}
          {past.length ? (
            <details className="mt-4 border border-ink">
              <summary className="cursor-pointer px-3 py-2 text-[13px] font-semibold">{t("plans.past", { count: past.length })}</summary>
              <div className="grid gap-4 p-3 md:grid-cols-2 2xl:grid-cols-3">{past.map(card)}</div>
            </details>
          ) : null}
        </Section>

        <Section n={2} title={t("plans.onSale")}>
          {plans.isPending ? (
            <Skeleton className="h-48" />
          ) : plans.error ? (
            <ErrorBox error={plans.error} onRetry={() => plans.refetch()} />
          ) : plans.data.items.length === 0 ? (
            <EmptyState title={t("plans.noneOnSale")} description={t("plans.noneOnSaleHint")} />
          ) : (
            <ul className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
              {plans.data.items.map((p) => (
                <li key={p.id} className="flex flex-col border-2 border-ink">
                  <div className="border-b border-ink px-4 py-3">
                    <div className="label-mono text-muted">PLAN · {t(`duration.${p.duration_unit}`, { count: p.duration_value })}</div>
                    <h3 className="font-display mt-1 text-[24px]">{p.name}</h3>
                    {p.description ? <p className="mt-1 text-[13px] text-muted">{p.description}</p> : null}
                  </div>
                  <dl className="flex-1 divide-y divide-rule text-[13px]">
                    {(
                      [
                        [t("plans.spec"), `${formatUSD(p.weekly_quota_micro)} / ${t("plans.week")}`],
                        [t("plans.duration"), t(`duration.${p.duration_unit}`, { count: p.duration_value })],
                        [t("plans.models"), p.allowed_models.length ? p.allowed_models.join(", ") : t("plans.allModels")],
                      ] as const
                    ).map(([k, v]) => (
                      <div key={k} className="grid grid-cols-[88px_1fr] gap-3 px-4 py-2">
                        <dt className="label-mono self-center text-muted">{k}</dt>
                        <dd className="num min-w-0 break-words">{v}</dd>
                      </div>
                    ))}
                  </dl>
                  <div className="flex items-center justify-between gap-3 border-t-2 border-ink px-4 py-3">
                    <span className="num text-[24px] font-medium">{formatUSD(p.price_micro)}</span>
                    <Button
                      variant="primary"
                      onClick={() => {
                        setAutoRenew(false);
                        setBuying(p);
                      }}
                    >
                      <ShoppingCart aria-hidden />
                      {t("plans.buy")}
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Section>
      </div>

      <ConfirmDialog
        open={!!buying}
        onOpenChange={(o) => !o && setBuying(null)}
        title={t("plans.buyTitle", { name: buying?.name })}
        danger={false}
        confirmLabel={t("plans.confirmBuy")}
        onConfirm={() => purchase.mutateAsync({ id: buying!.id, auto: autoRenew })}
        description={
          buying ? (
            <div className="flex flex-col gap-4">
              <p className="text-[13px] text-muted">{t("plans.buyHint")}</p>
              <PayLines price={buying.price_micro} balance={available} />
              <div className="flex items-center gap-2">
                <Checkbox id="buy-auto" checked={autoRenew} onCheckedChange={(v) => setAutoRenew(v === true)} />
                <Label htmlFor="buy-auto" className="font-sans text-[13px] font-normal normal-case">
                  {t("plans.autoRenewOnBuy")}
                </Label>
              </div>
            </div>
          ) : null
        }
      />
      <ConfirmDialog
        open={!!renewing}
        onOpenChange={(o) => !o && setRenewing(null)}
        title={t("plans.renewTitle", { name: renewing?.plan_name })}
        danger={false}
        confirmLabel={t("plans.renew")}
        onConfirm={() => renew.mutateAsync(renewing!.id)}
        description={renewing ? <PayLines price={renewing.price_micro} balance={available} /> : null}
      />
    </>
  );
}
