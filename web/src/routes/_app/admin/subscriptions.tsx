import { useEffect, useMemo, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { keepPreviousData, useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { AlertTriangle, ArrowLeftRight, Ban, Gift, Link2 } from "lucide-react";
import { toast } from "sonner";
import { api, unwrap, type Schemas } from "@/api/client";
import { accountsQuery, adminPlansQuery, keys, settingsQuery, subscriptionsInfinite } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { DataTable, columnHelper } from "@/components/DataTable";
import { CountBadge } from "@/components/Tags";
import { EmptyState } from "@/components/Feedback";
import { AccountPicker, UserPicker } from "@/components/Pickers";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog";
import { Input, NativeSelect } from "@/components/ui/input";
import { Field, Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useApiMutation } from "@/lib/mutations";
import { SUB_STATUSES } from "@/lib/constants";
import { formatDateTime, formatUSD, parseUSD } from "@/lib/format";
import { shortId } from "@/lib/utils";

interface Search {
  tab?: "pending" | "all";
  status?: Schemas["SubscriptionStatus"];
  user?: string;
  account?: string;
  rebind?: string;
}

export const Route = createFileRoute("/_app/admin/subscriptions")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    tab: s.tab === "all" ? "all" : s.tab === "pending" ? "pending" : undefined,
    status: SUB_STATUSES.includes(s.status as Schemas["SubscriptionStatus"]) ? (s.status as Schemas["SubscriptionStatus"]) : undefined,
    user: typeof s.user === "string" && s.user ? s.user : undefined,
    account: typeof s.account === "string" && s.account ? s.account : undefined,
    rebind: typeof s.rebind === "string" && s.rebind ? s.rebind : undefined,
  }),
  component: AdminSubscriptions,
});

type Sub = Schemas["Subscription"];

function StatusPlate({ s }: { s: Schemas["SubscriptionStatus"] }) {
  const { t } = useTranslation();
  const cls = s === "active" ? "bg-ink text-bg" : s === "pending_assignment" ? "border border-ink bg-yellow text-on-yellow" : "border border-rule text-muted";
  return <span className={`label-mono inline-flex h-[22px] items-center px-1.5 ${cls}`}>{t(`subStatus.${s}`)}</span>;
}

function AdminSubscriptions() {
  const { t } = useTranslation();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: "/admin/subscriptions" });
  const tab = search.tab ?? "pending";
  const set = (patch: Partial<Search>) => navigate({ search: (s) => ({ ...s, ...patch }), replace: true });
  const pending = useInfiniteQuery(subscriptionsInfinite({ status: "pending_assignment", limit: 100 }));
  const all = useInfiniteQuery({
    ...subscriptionsInfinite({ status: search.status, user_id: search.user, account_id: search.account, limit: 100 }),
    placeholderData: keepPreviousData,
    enabled: tab === "all",
  });
  const accounts = useQuery(accountsQuery);
  const pendingRows = useMemo(() => pending.data?.pages.flatMap((p) => p.items) ?? [], [pending.data]);
  const allRows = useMemo(() => all.data?.pages.flatMap((p) => p.items) ?? [], [all.data]);
  const [assigning, setAssigning] = useState<Sub | null>(null);
  const [rebinding, setRebinding] = useState<Sub | null>(null);
  const [cancelling, setCancelling] = useState<Sub | null>(null);
  const [granting, setGranting] = useState(false);

  // Deep link from the account delete conflict: open the rebind dialog.
  useEffect(() => {
    if (!search.rebind) return;
    const s = allRows.find((x) => x.id === search.rebind);
    if (s) {
      setRebinding(s);
      set({ rebind: undefined });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search.rebind, allRows]);

  const col = columnHelper<Sub>();
  const base = [
    col.display({ id: "no", header: "No.", cell: (c) => <span className="num text-[12px]">{shortId(c.row.original.id)}</span> }),
    col.display({
      id: "user",
      header: t("subs.user"),
      cell: (c) => (
        <Link to="/admin/users/$userId" params={{ userId: c.row.original.user_id }} className="font-semibold underline underline-offset-4">
          {c.row.original.username || shortId(c.row.original.user_id)}
        </Link>
      ),
    }),
    col.accessor("plan_name", { header: t("subs.plan"), cell: (c) => c.getValue() }),
    col.accessor("weekly_quota_micro", { header: t("subs.quota"), cell: (c) => <span className="num">{formatUSD(c.getValue())}</span> }),
  ];
  const pendingCols = [
    ...base,
    col.accessor("created_at", { header: t("subs.createdAt"), cell: (c) => <span className="num text-[12px]">{formatDateTime(c.getValue())}</span> }),
    col.display({
      id: "assign",
      header: () => <span className="sr-only">{t("common.actions")}</span>,
      cell: (c) => (
        <Button size="sm" variant="primary" onClick={() => setAssigning(c.row.original)}>
          <Link2 aria-hidden />
          {t("subs.assign")}
        </Button>
      ),
    }),
  ];
  const allCols = [
    ...base,
    col.display({ id: "account", header: t("subs.account"), cell: (c) => <span className="text-[12px]">{c.row.original.account_name || t("subs.unbound")}</span> }),
    col.accessor("status", { header: t("subs.status"), cell: (c) => <StatusPlate s={c.getValue()} /> }),
    col.display({
      id: "remaining",
      header: t("subs.periodRemaining"),
      cell: (c) => <span className="num text-[12px]">{c.row.original.current_period ? formatUSD(Math.max(0, c.row.original.current_period.remaining_micro)) : "—"}</span>,
    }),
    col.accessor("expires_at", { header: t("plans.expiresAt"), cell: (c) => <span className="num text-[12px]">{formatDateTime(c.getValue())}</span> }),
    col.display({
      id: "actions",
      header: () => <span className="sr-only">{t("common.actions")}</span>,
      cell: (c) => {
        const s = c.row.original;
        const open = s.status === "active" || s.status === "pending_assignment";
        return (
          <div className="flex justify-end gap-1">
            {s.status === "pending_assignment" ? (
              <Button size="sm" variant="primary" onClick={() => setAssigning(s)}>
                {t("subs.assign")}
              </Button>
            ) : null}
            {s.status === "active" ? (
              <Button size="sm" onClick={() => setRebinding(s)}>
                <ArrowLeftRight aria-hidden />
                {t("subs.rebind")}
              </Button>
            ) : null}
            {open ? (
              <Button size="sm" variant="danger" onClick={() => setCancelling(s)}>
                <Ban aria-hidden />
                {t("subs.cancel")}
              </Button>
            ) : null}
          </div>
        );
      },
    }),
  ];

  const card = (s: Sub) => (
    <div className="border border-ink p-3">
      <div className="flex items-center justify-between gap-2">
        <span className="font-semibold">
          {s.plan_name} <span className="num text-[12px] text-muted">No.{shortId(s.id)}</span>
        </span>
        <StatusPlate s={s.status} />
      </div>
      <dl className="mt-2 grid grid-cols-2 gap-1 text-[12px]">
        <dt className="label-mono text-muted">{t("subs.user")}</dt>
        <dd className="text-right">{s.username}</dd>
        <dt className="label-mono text-muted">{t("subs.account")}</dt>
        <dd className="text-right">{s.account_name || t("subs.unbound")}</dd>
        <dt className="label-mono text-muted">{t("subs.quota")}</dt>
        <dd className="num text-right">{formatUSD(s.weekly_quota_micro)}</dd>
      </dl>
      <div className="mt-3 flex gap-2 [&>*]:flex-1">
        {s.status === "pending_assignment" ? (
          <Button size="sm" variant="primary" onClick={() => setAssigning(s)}>
            {t("subs.assign")}
          </Button>
        ) : null}
        {s.status === "active" ? (
          <Button size="sm" onClick={() => setRebinding(s)}>
            {t("subs.rebind")}
          </Button>
        ) : null}
        {s.status === "active" || s.status === "pending_assignment" ? (
          <Button size="sm" variant="danger" onClick={() => setCancelling(s)}>
            {t("subs.cancel")}
          </Button>
        ) : null}
      </div>
    </div>
  );

  return (
    <>
      <PageHeader
        code="OB-11"
        title={t("subs.title")}
        actions={
          <Button variant="primary" onClick={() => setGranting(true)}>
            <Gift aria-hidden />
            {t("subs.grant")}
          </Button>
        }
      />
      <Tabs value={tab} onValueChange={(v) => set({ tab: v === "all" ? "all" : undefined })}>
        <TabsList>
          <TabsTrigger value="pending">
            {t("subs.pendingTab")}
            <CountBadge n={pendingRows.length} />
          </TabsTrigger>
          <TabsTrigger value="all">{t("subs.allTab")}</TabsTrigger>
        </TabsList>
        <TabsContent value="pending" className="pt-6">
          <Section n={1} title={t("subs.pendingTab")} bodyClassName="p-0 sm:p-4">
            <DataTable
              label={t("subs.pendingTab")}
              columns={pendingCols}
              data={pendingRows}
              rowKey={(s) => s.id}
              loading={pending.isPending}
              error={pending.error}
              onRetry={() => pending.refetch()}
              hasMore={pending.hasNextPage}
              fetchingMore={pending.isFetchingNextPage}
              onLoadMore={() => void pending.fetchNextPage()}
              renderCard={card}
              className="max-sm:p-4"
              empty={<EmptyState className="m-4 sm:m-0" title={t("subs.noPending")} description={t("subs.noPendingHint")} />}
            />
          </Section>
        </TabsContent>
        <TabsContent value="all" className="pt-6">
          <div className="mb-4 grid gap-3 border-2 border-ink p-4 sm:grid-cols-3">
            <Field label={t("subs.status")} htmlFor="fs-status">
              <NativeSelect id="fs-status" value={search.status ?? ""} onChange={(e) => set({ status: (e.target.value || undefined) as Search["status"] })}>
                <option value="">{t("common.all")}</option>
                {SUB_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {t(`subStatus.${s}`)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field label={t("subs.account")} htmlFor="fs-account">
              <NativeSelect id="fs-account" value={search.account ?? ""} onChange={(e) => set({ account: e.target.value || undefined })}>
                <option value="">{t("common.all")}</option>
                {accounts.data?.items.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <div className="flex flex-col gap-1.5">
              <Label>{t("subs.user")}</Label>
              {search.user ? (
                <Button className="justify-between" onClick={() => set({ user: undefined })}>
                  <span className="num">{shortId(search.user)}</span>
                  <span className="text-[12px]">{t("common.clear")}</span>
                </Button>
              ) : (
                <p className="flex h-10 items-center text-[12px] text-muted">{t("subs.userFilterHint")}</p>
              )}
            </div>
          </div>
          <Section n={2} title={t("subs.allTab")} bodyClassName="p-0 sm:p-4">
            <DataTable
              label={t("subs.allTab")}
              columns={allCols}
              data={allRows}
              rowKey={(s) => s.id}
              loading={all.isPending}
              error={all.error}
              onRetry={() => all.refetch()}
              hasMore={all.hasNextPage}
              fetchingMore={all.isFetchingNextPage}
              onLoadMore={() => void all.fetchNextPage()}
              renderCard={card}
              className="max-sm:p-4"
              empty={<EmptyState className="m-4 sm:m-0" title={t("subs.none")} />}
            />
          </Section>
        </TabsContent>
      </Tabs>

      {assigning ? <BindDialog mode="assign" sub={assigning} onClose={() => setAssigning(null)} /> : null}
      {rebinding ? <BindDialog mode="rebind" sub={rebinding} onClose={() => setRebinding(null)} /> : null}
      {cancelling ? <CancelDialog sub={cancelling} onClose={() => setCancelling(null)} /> : null}
      {granting ? <GrantDialog onClose={() => setGranting(false)} /> : null}
    </>
  );
}

function useInvalidateSubs() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["admin", "subscriptions"] });
    void qc.invalidateQueries({ queryKey: keys.overview });
    void qc.invalidateQueries({ queryKey: keys.accounts });
    void qc.invalidateQueries({ queryKey: ["admin", "users"] });
  };
}

/** Client-side preview of the backend's overcommit check (same figures). */
function useOvercommit(account: Schemas["Account"] | null, quota: number, alreadyOn: boolean) {
  const settings = useQuery(settingsQuery);
  if (!account || account.week_total_micro == null) return null;
  const mult = settings.data?.tier_multipliers.plan || 1;
  const sold = account.sold_upstream_micro + (alreadyOn ? 0 : quota / mult);
  return sold > account.week_total_micro ? { sold, total: account.week_total_micro } : null;
}

function BindDialog({ mode, sub, onClose }: { mode: "assign" | "rebind"; sub: Sub; onClose: () => void }) {
  const { t } = useTranslation();
  const invalidate = useInvalidateSubs();
  const [account, setAccount] = useState<Schemas["Account"] | null>(null);
  const [warning, setWarning] = useState<string | null>(null);
  const over = useOvercommit(account, sub.weekly_quota_micro, false);
  const bind = useApiMutation({
    mutationFn: (accountId: string) =>
      mode === "assign"
        ? unwrap(api.POST("/admin/subscriptions/{subscriptionId}/assign", { params: { path: { subscriptionId: sub.id } }, body: { account_id: accountId } }))
        : unwrap(api.POST("/admin/subscriptions/{subscriptionId}/rebind", { params: { path: { subscriptionId: sub.id } }, body: { account_id: accountId } })),
    onSuccess: (res) => {
      invalidate();
      if (res.warning) {
        setWarning(res.warning);
        toast.warning(t("subs.overcommitDone"));
      } else {
        toast.success(mode === "assign" ? t("subs.assigned") : t("subs.rebound"));
        onClose();
      }
    },
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={mode === "assign" ? t("subs.assignTitle") : t("subs.rebindTitle")} description={`${sub.plan_name} · No.${shortId(sub.id)} · ${sub.username} · ${formatUSD(sub.weekly_quota_micro)} / ${t("plans.week")}`} wide>
        {warning ? (
          <div role="alert" className="flex gap-3 border-2 border-ink bg-yellow p-3 text-on-yellow">
            <AlertTriangle className="size-5" aria-hidden />
            <div>
              <p className="font-semibold">{t("subs.overcommitDone")}</p>
              <p className="mt-1 text-[13px]">{warning}</p>
            </div>
          </div>
        ) : (
          <div className="flex flex-col gap-4">
            <AccountPicker label={t("subs.chooseAccount")} value={account?.id ?? ""} onChange={setAccount} exclude={mode === "rebind" ? sub.account_id : null} />
            {over ? (
              <div role="alert" className="flex gap-3 border-2 border-ink bg-yellow p-3 text-on-yellow">
                <AlertTriangle className="size-5 shrink-0" aria-hidden />
                <p className="text-[13px]">{t("subs.overcommitPreview", { sold: formatUSD(over.sold), total: formatUSD(over.total) })}</p>
              </div>
            ) : null}
          </div>
        )}
        <DialogFooter>
          <Button onClick={onClose}>{warning ? t("common.done") : t("common.cancel")}</Button>
          {!warning ? (
            <Button variant="primary" disabled={!account || bind.isPending} onClick={() => account && bind.mutate(account.id)}>
              {over ? t("subs.confirmAnyway") : t("common.confirm")}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function CancelDialog({ sub, onClose }: { sub: Sub; onClose: () => void }) {
  const { t } = useTranslation();
  const invalidate = useInvalidateSubs();
  const schema = z.object({
    refund: z.string().refine((v) => v.trim() === "" || (Number.isFinite(parseUSD(v)) && parseUSD(v) >= 0), t("validation.amountNonNegative")),
    note: z.string().max(500),
  });
  type Values = z.infer<typeof schema>;
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { refund: "", note: "" } });
  const errs = form.formState.errors;
  const cancel = useApiMutation({
    mutationFn: (v: Values) =>
      unwrap(
        api.POST("/admin/subscriptions/{subscriptionId}/cancel", {
          params: { path: { subscriptionId: sub.id } },
          body: { refund_micro: v.refund.trim() === "" ? 0 : parseUSD(v.refund), note: v.note },
        }),
      ),
    success: t("subs.cancelled"),
    onSuccess: () => {
      invalidate();
      onClose();
    },
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={t("subs.cancelTitle")} description={t("subs.cancelBody", { plan: sub.plan_name, user: sub.username, price: formatUSD(sub.price_micro) })}>
        <form onSubmit={form.handleSubmit((v) => cancel.mutate(v))} noValidate className="flex flex-col gap-4">
          <Field label={t("subs.refundUsd")} htmlFor="cx-refund" hint={t("subs.refundHint")} error={errs.refund?.message}>
            <Input id="cx-refund" inputMode="decimal" className="num" placeholder="0.00" aria-invalid={!!errs.refund} {...form.register("refund")} />
          </Field>
          <Field label={t("users.note")} htmlFor="cx-note">
            <Input id="cx-note" {...form.register("note")} />
          </Field>
          <DialogFooter className="mt-0">
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" variant="danger" disabled={cancel.isPending}>
              {t("subs.confirmCancel")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function GrantDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const invalidate = useInvalidateSubs();
  const plans = useQuery(adminPlansQuery);
  const [userId, setUserId] = useState("");
  const [planId, setPlanId] = useState("");
  const [account, setAccount] = useState<Schemas["Account"] | null>(null);
  const [charge, setCharge] = useState(false);
  const [warning, setWarning] = useState<string | null>(null);
  const plan = plans.data?.items.find((p) => p.id === planId);
  const over = useOvercommit(account, plan?.weekly_quota_micro ?? 0, false);
  const grant = useApiMutation({
    mutationFn: () => unwrap(api.POST("/admin/subscriptions", { body: { user_id: userId, plan_id: planId, account_id: account!.id, charge } })),
    onSuccess: (res) => {
      invalidate();
      if (res.warning) setWarning(res.warning);
      else {
        toast.success(t("subs.granted"));
        onClose();
      }
    },
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={t("subs.grant")} description={t("subs.grantHint")} wide>
        {warning ? (
          <div role="alert" className="flex gap-3 border-2 border-ink bg-yellow p-3 text-on-yellow">
            <AlertTriangle className="size-5 shrink-0" aria-hidden />
            <p className="text-[13px]">{warning}</p>
          </div>
        ) : (
          <div className="flex flex-col gap-4">
            <UserPicker label={t("subs.user")} value={userId} onChange={(id) => setUserId(id)} />
            <Field label={t("subs.plan")} htmlFor="gr-plan">
              <NativeSelect id="gr-plan" value={planId} onChange={(e) => setPlanId(e.target.value)}>
                <option value="">{t("subs.choosePlan")}</option>
                {plans.data?.items.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name} · {formatUSD(p.weekly_quota_micro)}/{t("plans.week")} · {formatUSD(p.price_micro)}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <AccountPicker label={t("subs.chooseAccount")} value={account?.id ?? ""} onChange={setAccount} />
            <div className="flex items-center gap-2">
              <Checkbox id="gr-charge" checked={charge} onCheckedChange={(v) => setCharge(v === true)} />
              <Label htmlFor="gr-charge" className="font-sans text-[13px] font-normal normal-case">
                {t("subs.charge", { price: plan ? formatUSD(plan.price_micro) : "—" })}
              </Label>
            </div>
            {over ? (
              <div role="alert" className="flex gap-3 border-2 border-ink bg-yellow p-3 text-on-yellow">
                <AlertTriangle className="size-5 shrink-0" aria-hidden />
                <p className="text-[13px]">{t("subs.overcommitPreview", { sold: formatUSD(over.sold), total: formatUSD(over.total) })}</p>
              </div>
            ) : null}
          </div>
        )}
        <DialogFooter>
          <Button onClick={onClose}>{warning ? t("common.done") : t("common.cancel")}</Button>
          {!warning ? (
            <Button variant="primary" disabled={!userId || !planId || !account || grant.isPending} onClick={() => grant.mutate()}>
              {t("subs.grantConfirm")}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

