import { useMemo, useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useInfiniteQuery, useQuery, useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { KeyRound, RotateCcw, ShieldAlert, SlidersHorizontal, UserX, UserCheck, Wallet } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { adminUsageQuery, keys, meQuery, subscriptionsInfinite, userKeysQuery, userQuery, userSafeguardQuery } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { StatStrip } from "@/components/StatStrip";
import { TierStackedBars } from "@/components/Charts";
import { TierChain } from "@/components/Tags";
import { KeyLimitFields } from "@/components/KeyLimitFields";
import { KeyStatusTag } from "@/components/KeyStatusTag";
import { draftFromKey, draftToBody, type LimitDraft, type LimitErrors } from "@/lib/keyLimits";
import { UserStatusTag } from "@/components/UserTags";
import { EmptyState, ErrorBox } from "@/components/Feedback";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog";
import { Input, NativeSelect, Textarea } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Skeleton } from "@/components/ui/skeleton";
import { useApiMutation } from "@/lib/mutations";
import { formatDateTime, formatInt, formatUSD, parseUSD } from "@/lib/format";
import { seriesFacts, tierSeries, useDayRange } from "@/lib/usage";
import { shortId } from "@/lib/utils";

export const Route = createFileRoute("/_app/admin/users/$userId")({
  loader: ({ context: { queryClient }, params }) => queryClient.ensureQueryData(userQuery(params.userId)),
  component: UserDetail,
});

type User = Schemas["AdminUser"];

function UserDetail() {
  const { t } = useTranslation();
  const { userId } = Route.useParams();
  const { data: user } = useSuspenseQuery(userQuery(userId));
  const { data: me } = useSuspenseQuery(meQuery);
  const qc = useQueryClient();
  const [adjusting, setAdjusting] = useState(false);
  const [resetting, setResetting] = useState(false);
  const [toggling, setToggling] = useState(false);
  const self = me.id === user.id;

  const patch = useApiMutation({
    mutationFn: (body: Schemas["AdminUserUpdate"]) => unwrap(api.PATCH("/admin/users/{userId}", { params: { path: { userId } }, body })),
    onSuccess: (u) => {
      qc.setQueryData(keys.user(userId), u);
      void qc.invalidateQueries({ queryKey: ["admin", "users", "list"] });
    },
  });

  return (
    <>
      <PageHeader
        code="OB-08"
        title={
          <span className="flex flex-wrap items-center gap-3">
            {user.username}
            <UserStatusTag u={user} />
          </span>
        }
        actions={
          <>
            <Button onClick={() => setResetting(true)}>
              <KeyRound aria-hidden />
              {t("users.resetPassword")}
            </Button>
            {!self ? (
              <Button variant={user.status === "active" ? "danger" : "primary"} onClick={() => setToggling(true)}>
                {user.status === "active" ? <UserX aria-hidden /> : <UserCheck aria-hidden />}
                {user.status === "active" ? t("users.disable") : t("users.enable")}
              </Button>
            ) : null}
          </>
        }
      />
      <div className="flex flex-col gap-6">
        <div className="grid gap-6 xl:grid-cols-[1fr_1fr]">
          <ProfileForm user={user} self={self} saving={patch.isPending} onSave={(body) => patch.mutateAsync(body)} />
          <Section
            n={2}
            title={t("users.balanceSection")}
            aside={
              <Button size="sm" variant="primary" onClick={() => setAdjusting(true)}>
                <Wallet aria-hidden />
                {t("users.adjust")}
              </Button>
            }
          >
            <StatStrip
              className="lg:grid-cols-2"
              stats={[
                { label: t("users.balance"), value: formatUSD(user.balance_micro), note: t("users.balanceNote") },
                { label: t("users.createdAt"), value: <span className="text-[18px]">{formatDateTime(user.created_at)}</span>, note: t("users.updatedAt", { time: formatDateTime(user.updated_at) }) },
              ]}
            />
            <p className="mt-3 text-[12px] text-muted">
              <Link to="/admin/ledger" search={{ user: user.id }} className="underline underline-offset-4">
                {t("users.viewLedger")}
              </Link>
            </p>
          </Section>
        </div>
        <UserKeys userId={userId} />
        <UserSafeguard userId={userId} />
        <UserSubscriptions userId={userId} />
        <UserUsage userId={userId} />
      </div>

      {adjusting ? <AdjustDialog user={user} onClose={() => setAdjusting(false)} /> : null}
      {resetting ? <ResetPasswordDialog onClose={() => setResetting(false)} onSave={(password) => patch.mutateAsync({ password })} /> : null}
      <ConfirmDialog
        open={toggling}
        onOpenChange={setToggling}
        title={user.status === "active" ? t("users.disableTitle", { name: user.username }) : t("users.enableTitle", { name: user.username })}
        description={user.status === "active" ? t("users.disableBody") : t("users.enableBody")}
        danger={user.status === "active"}
        confirmLabel={user.status === "active" ? t("users.disable") : t("users.enable")}
        onConfirm={() => patch.mutateAsync({ status: user.status === "active" ? "disabled" : "active" })}
      />
    </>
  );
}

function ProfileForm({ user, self, saving, onSave }: { user: User; self: boolean; saving: boolean; onSave: (b: Schemas["AdminUserUpdate"]) => Promise<unknown> }) {
  const { t } = useTranslation();
  const optInt = z
    .string()
    .trim()
    .refine((v) => v === "" || /^\d+$/.test(v), t("validation.nonNegativeInt"));
  const schema = z.object({ role: z.enum(["user", "admin"]), max_concurrency: z.string().trim().regex(/^\d+$/, t("validation.nonNegativeInt")), rpm: optInt, note: z.string().max(500) });
  type Values = z.infer<typeof schema>;
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    values: { role: user.role, max_concurrency: String(user.max_concurrency), rpm: user.rpm != null ? String(user.rpm) : "", note: user.note },
  });
  const errs = form.formState.errors;
  return (
    <Section n={1} title={t("users.profile")}>
      <form
        noValidate
        className="flex flex-col gap-4"
        onSubmit={form.handleSubmit(async (v) => {
          try {
            await onSave({ role: v.role, max_concurrency: Number(v.max_concurrency), rpm: v.rpm === "" ? null : Number(v.rpm), note: v.note });
          } catch {
            // reported by the mutation
          }
        })}
      >
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t("users.role")} htmlFor="u-role" hint={self ? t("users.selfRole") : undefined}>
            <NativeSelect id="u-role" disabled={self} {...form.register("role")}>
              <option value="user">{t("role.user")}</option>
              <option value="admin">{t("role.admin")}</option>
            </NativeSelect>
          </Field>
          <Field label={t("users.concurrency")} htmlFor="u-conc" error={errs.max_concurrency?.message}>
            <Input id="u-conc" inputMode="numeric" className="num" aria-invalid={!!errs.max_concurrency} {...form.register("max_concurrency")} />
          </Field>
          <Field label={t("users.rpm")} htmlFor="u-rpm" hint={t("keys.emptyUnlimited")} error={errs.rpm?.message}>
            <Input id="u-rpm" inputMode="numeric" className="num" aria-invalid={!!errs.rpm} {...form.register("rpm")} />
          </Field>
        </div>
        <Field label={t("users.note")} htmlFor="u-note">
          <Textarea id="u-note" rows={2} {...form.register("note")} />
        </Field>
        <div className="flex items-center justify-between border border-ink px-3 py-2 text-[13px]">
          <span className="label-mono">{t("users.status")}</span>
          <span className="font-semibold">{user.status === "active" ? t("users.active") : t("users.disabled")}</span>
        </div>
        <div>
          <Button type="submit" variant="primary" disabled={saving || !form.formState.isDirty}>
            {t("common.save")}
          </Button>
        </div>
      </form>
    </Section>
  );
}

function AdjustDialog({ user, onClose }: { user: User; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const schema = z
    .object({
      type: z.enum(["topup", "adjust"]),
      amount: z.string().refine((v) => Number.isFinite(parseUSD(v)) && parseUSD(v) !== 0, t("validation.amount")),
      note: z.string().trim().min(1, t("validation.required")).max(500),
    })
    .refine((v) => v.type !== "topup" || parseUSD(v.amount) > 0, { path: ["amount"], message: t("validation.positive") });
  type Values = z.infer<typeof schema>;
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { type: "topup", amount: "", note: "" } });
  const errs = form.formState.errors;
  const [idem] = useState(() => crypto.randomUUID());
  const amount = parseUSD(form.watch("amount") || "0");
  const adjust = useApiMutation({
    mutationFn: (v: Values) =>
      unwrap(
        api.POST("/admin/users/{userId}/balance", {
          params: { path: { userId: user.id } },
          body: { type: v.type, amount_micro: parseUSD(v.amount), note: v.note, idempotency_key: idem },
        }),
      ),
    success: t("users.adjusted"),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.user(user.id) });
      void qc.invalidateQueries({ queryKey: ["admin", "ledger"] });
      void qc.invalidateQueries({ queryKey: ["admin", "users", "list"] });
      onClose();
    },
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={t("users.adjustTitle", { name: user.username })}>
        <form onSubmit={form.handleSubmit((v) => adjust.mutate(v))} noValidate className="flex flex-col gap-4">
          <Field label={t("users.adjustType")} htmlFor="adj-type" hint={t("users.adjustTypeHint")}>
            <NativeSelect id="adj-type" {...form.register("type")}>
              <option value="topup">{t("users.topup")}</option>
              <option value="adjust">{t("users.adjustSigned")}</option>
            </NativeSelect>
          </Field>
          <Field label={t("users.amountUsd")} htmlFor="adj-amount" hint={t("users.amountHint")} error={errs.amount?.message}>
            <Input id="adj-amount" inputMode="decimal" className="num" placeholder="10.00" aria-invalid={!!errs.amount} {...form.register("amount")} />
          </Field>
          <Field label={t("users.note")} htmlFor="adj-note" error={errs.note?.message}>
            <Input id="adj-note" aria-invalid={!!errs.note} {...form.register("note")} />
          </Field>
          <dl className="num flex justify-between border-y-2 border-ink py-2 text-[14px]">
            <dt>{t("plans.balanceAfter")}</dt>
            <dd className="font-bold">{formatUSD(user.balance_micro + (Number.isFinite(amount) ? amount : 0))}</dd>
          </dl>
          <DialogFooter className="mt-0">
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" variant="primary" disabled={adjust.isPending}>
              {t("common.confirm")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ResetPasswordDialog({ onClose, onSave }: { onClose: () => void; onSave: (pw: string) => Promise<unknown> }) {
  const { t } = useTranslation();
  const schema = z.object({ password: z.string().min(8, t("validation.minPassword")).max(256) });
  const form = useForm<{ password: string }>({ resolver: zodResolver(schema), defaultValues: { password: "" } });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={t("users.resetPassword")} description={t("users.resetHint")}>
        <form
          noValidate
          className="flex flex-col gap-4"
          onSubmit={form.handleSubmit(async (v) => {
            try {
              await onSave(v.password);
              onClose();
            } catch {
              // reported by the mutation
            }
          })}
        >
          <Field label={t("settings.newPassword")} htmlFor="rp-pw" error={form.formState.errors.password?.message}>
            <Input id="rp-pw" type="password" autoComplete="new-password" aria-invalid={!!form.formState.errors.password} {...form.register("password")} />
          </Field>
          <DialogFooter className="mt-0">
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" variant="danger" disabled={form.formState.isSubmitting}>
              {t("users.resetPassword")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function UserKeys({ userId }: { userId: string }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery(userKeysQuery(userId));
  const toggle = useApiMutation({
    mutationFn: (v: { id: string; enabled: boolean }) => unwrap(api.PUT("/admin/keys/{keyId}/enabled", { params: { path: { keyId: v.id } }, body: { enabled: v.enabled } })),
    onMutate: async (v) => {
      await qc.cancelQueries({ queryKey: keys.userKeys(userId) });
      const prev = qc.getQueryData<Schemas["ApiKeyList"]>(keys.userKeys(userId));
      qc.setQueryData<Schemas["ApiKeyList"]>(keys.userKeys(userId), (old) => (old ? { items: old.items.map((k) => (k.id === v.id ? { ...k, enabled: v.enabled } : k)) } : old));
      return { prev };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(keys.userKeys(userId), ctx.prev);
    },
    onSettled: () => qc.invalidateQueries({ queryKey: keys.userKeys(userId) }),
  });
  const [editing, setEditing] = useState<Schemas["ApiKey"] | null>(null);
  const [resetting, setResetting] = useState<Schemas["ApiKey"] | null>(null);
  const reset = useApiMutation({
    mutationFn: (id: string) => unwrap(api.POST("/admin/keys/{keyId}/reset-usage", { params: { path: { keyId: id } } })),
    success: t("keyLimits.resetDone"),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.userKeys(userId) }),
  });
  return (
    <>
    <Section n={3} title={t("users.keys")} bodyClassName="p-0">
      {q.isPending ? (
        <Skeleton className="m-4 h-16" />
      ) : q.error ? (
        <ErrorBox className="m-4" error={q.error} onRetry={() => q.refetch()} />
      ) : q.data.items.length === 0 ? (
        <EmptyState className="m-4" title={t("users.noKeys")} />
      ) : (
        <ul className="divide-y divide-rule">
          {q.data.items.map((k) => (
            <li key={k.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="font-semibold">{k.name || t("keys.unnamed")}</div>
                <div className="num text-[12px] text-muted">
                  {k.prefix} · {t("keys.lastUsed")} {formatDateTime(k.last_used_at)}
                </div>
              </div>
              <KeyStatusTag status={k.status} />
              <TierChain tiers={k.tiers} />
              <span className="num text-[12px]">
                {k.quota_limit_micro == null
                  ? t("keyLimits.unlimited")
                  : `${formatUSD(k.quota_scope === "balance" ? k.used_balance_micro : k.used_all_micro)} / ${formatUSD(k.quota_limit_micro)}`}
              </span>
              <Button variant="ghost" size="icon-sm" aria-label={t("keyLimits.editFor", { name: k.name || k.prefix })} onClick={() => setEditing(k)}>
                <SlidersHorizontal aria-hidden />
              </Button>
              <Button variant="ghost" size="icon-sm" aria-label={t("keyLimits.resetFor", { name: k.name || k.prefix })} onClick={() => setResetting(k)}>
                <RotateCcw aria-hidden />
              </Button>
              <Switch checked={k.enabled} aria-label={t("keys.toggle", { name: k.name || k.prefix })} onCheckedChange={(v) => toggle.mutate({ id: k.id, enabled: v })} />
            </li>
          ))}
        </ul>
      )}
    </Section>
    {editing ? <AdminKeyLimitsDialog k={editing} userId={userId} onClose={() => setEditing(null)} /> : null}
    <ConfirmDialog
      open={!!resetting}
      onOpenChange={(o) => !o && setResetting(null)}
      title={t("keyLimits.resetTitle")}
      description={t("keyLimits.resetBody")}
      confirmLabel={t("keyLimits.reset")}
      onConfirm={() => reset.mutateAsync(resetting!.id)}
    />
    </>
  );
}

/** Admin editor of a key's limits and access controls (audited). */
function AdminKeyLimitsDialog({ k, userId, onClose }: { k: Schemas["ApiKey"]; userId: string; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [draft, setDraft] = useState<LimitDraft>(() => draftFromKey(k));
  const [errors, setErrors] = useState<LimitErrors>({});
  const save = useApiMutation({
    mutationFn: (body: Schemas["ApiKeyUpdate"]) => unwrap(api.PATCH("/admin/keys/{keyId}", { params: { path: { keyId: k.id } }, body })),
    success: t("keys.saved"),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.userKeys(userId) });
      onClose();
    },
  });
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const { body, errors: errs } = draftToBody(draft);
    setErrors(errs);
    if (Object.keys(errs).length === 0) save.mutate(body);
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={t("keyLimits.adminTitle", { name: k.name || k.prefix })} wide>
        <form onSubmit={submit} noValidate className="flex flex-col gap-5">
          <KeyLimitFields draft={draft} onChange={setDraft} errors={errors} idPrefix="akl" />
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

function UserSubscriptions({ userId }: { userId: string }) {
  const { t } = useTranslation();
  const q = useInfiniteQuery(subscriptionsInfinite({ user_id: userId, limit: 50 }));
  const rows = useMemo(() => q.data?.pages.flatMap((p) => p.items) ?? [], [q.data]);
  return (
    <Section
      n={5}
      title={t("users.subscriptions")}
      aside={
        <Link to="/admin/subscriptions" search={{ tab: "all", user: userId }} className="text-[13px] font-semibold underline underline-offset-4">
          {t("common.viewAll")}
        </Link>
      }
      bodyClassName="p-0"
    >
      {q.isPending ? (
        <Skeleton className="m-4 h-16" />
      ) : rows.length === 0 ? (
        <EmptyState className="m-4" title={t("users.noSubscriptions")} />
      ) : (
        <ul className="divide-y divide-rule">
          {rows.map((s) => (
            <li key={s.id} className="grid gap-1 px-4 py-3 text-[13px] sm:grid-cols-[1fr_auto_auto] sm:items-center sm:gap-4">
              <div>
                <span className="font-semibold">{s.plan_name}</span>
                <span className="num ml-2 text-muted">No.{shortId(s.id)}</span>
              </div>
              <span className="text-muted">{s.account_name || t("subs.unbound")}</span>
              <span className="label-mono border border-ink px-1.5 py-0.5">{t(`subStatus.${s.status}`)}</span>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}

function UserUsage({ userId }: { userId: string }) {
  const { t } = useTranslation();
  const range = useDayRange(14);
  const q = useQuery(adminUsageQuery({ granularity: "hour", from: range.from, to: range.to, user_id: userId }));
  const series = tierSeries(q.data?.items ?? [], "day", range.fromDate, range.toDate);
  const facts = seriesFacts(series, formatUSD);
  const upstream = (q.data?.items ?? []).reduce((a, b) => a + b.upstream_cost_micro, 0);
  return (
    <Section n={6} title={t("users.recentUsage")} aside={<span className="num text-[12px] text-muted">{t("users.usageTotals", { billed: facts.total, upstream: formatUSD(upstream) })}</span>}>
      {q.isPending ? <Skeleton className="h-56" /> : q.error ? <ErrorBox error={q.error} onRetry={() => q.refetch()} /> : <TierStackedBars data={series} description={t("charts.tierSeries", { title: t("users.recentUsage"), ...facts })} />}
    </Section>
  );
}


/** Safeguard counters of the user, with the admin's adjustments (audited). */
function UserSafeguard({ userId }: { userId: string }) {
  const { t } = useTranslation();
  const q = useQuery(userSafeguardQuery(userId));
  const [editing, setEditing] = useState(false);
  const s = q.data;
  return (
    <Section
      n={4}
      title={t("users.safeguard")}
      aside={
        <>
          <Link to="/admin/safeguard" search={{ user: userId }} className="text-[13px] font-semibold underline underline-offset-4">
            {t("users.safeguardEvents")}
          </Link>
          <Button size="sm" variant="primary" disabled={!s} onClick={() => setEditing(true)}>
            <ShieldAlert aria-hidden />
            {t("users.safeguardAdjust")}
          </Button>
        </>
      }
    >
      {q.isPending ? (
        <Skeleton className="h-24" />
      ) : q.error ? (
        <ErrorBox error={q.error} onRetry={() => q.refetch()} />
      ) : s ? (
        <StatStrip
          stats={[
            {
              label: t("users.sgUsed"),
              value: (
                <span data-testid="user-sg-used">
                  {s.used} / {s.allowance}
                </span>
              ),
              note: s.allowance_override != null ? t("users.sgOverride") : t("users.sgDefault", { n: s.default_allowance }),
              alert: s.locked,
            },
            { label: t("users.sgCards"), value: s.reset_cards },
            { label: t("users.sgEvents"), value: formatInt(s.events_count) },
            { label: t("users.sgBlocked"), value: formatInt(s.blocked_count), note: s.locked && s.locked_at ? t("users.sgLockedAt", { time: formatDateTime(s.locked_at) }) : undefined },
          ]}
        />
      ) : null}
      {editing && s ? <SafeguardDialog s={s} onClose={() => setEditing(false)} /> : null}
    </Section>
  );
}

function SafeguardDialog({ s, onClose }: { s: Schemas["AdminUserSafeguard"]; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const int = (min: number, max: number) =>
    z
      .string()
      .trim()
      .refine((v) => /^\d+$/.test(v) && Number(v) >= min && Number(v) <= max, t("validation.between", { min, max }));
  const schema = z.object({
    override: z
      .string()
      .trim()
      .refine((v) => v === "" || (/^\d+$/.test(v) && Number(v) >= 1 && Number(v) <= 1000), t("validation.between", { min: 1, max: 1000 })),
    used: int(0, 100000),
    cards: int(0, 1000),
  });
  type V = z.infer<typeof schema>;
  const form = useForm<V>({
    resolver: zodResolver(schema),
    defaultValues: { override: s.allowance_override != null ? String(s.allowance_override) : "", used: String(s.used), cards: String(s.reset_cards) },
  });
  const errs = form.formState.errors;
  const save = useApiMutation({
    mutationFn: (body: Schemas["AdminUserSafeguardUpdate"]) => unwrap(api.PATCH("/admin/users/{userId}/safeguard", { params: { path: { userId: s.user_id } }, body })),
    success: t("users.sgSaved"),
    onSuccess: (next) => {
      qc.setQueryData(keys.userSafeguard(s.user_id), next);
      void qc.invalidateQueries({ queryKey: keys.user(s.user_id) });
      void qc.invalidateQueries({ queryKey: ["admin", "users", "list"] });
      onClose();
    },
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={t("users.sgAdjustTitle", { name: s.username })} description={t("users.sgAdjustHint")}>
        <form
          noValidate
          className="flex flex-col gap-4"
          onSubmit={form.handleSubmit((v) => {
            const body: Schemas["AdminUserSafeguardUpdate"] = {};
            const override = v.override === "" ? null : Number(v.override);
            if (override !== (s.allowance_override ?? null)) body.allowance_override = override;
            if (Number(v.used) !== s.used) body.used = Number(v.used);
            if (Number(v.cards) !== s.reset_cards) body.reset_cards = Number(v.cards);
            if (Object.keys(body).length) save.mutate(body);
            else onClose();
          })}
        >
          <Field label={t("users.sgOverrideLabel")} htmlFor="sg-override" hint={t("users.sgOverrideHint", { n: s.default_allowance })} error={errs.override?.message}>
            <Input id="sg-override" inputMode="numeric" className="num" aria-invalid={!!errs.override} {...form.register("override")} />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("users.sgUsed")} htmlFor="sg-used" hint={t("users.sgUsedHint")} error={errs.used?.message}>
              <Input id="sg-used" inputMode="numeric" className="num" aria-invalid={!!errs.used} {...form.register("used")} />
            </Field>
            <Field label={t("users.sgCards")} htmlFor="sg-cards" error={errs.cards?.message}>
              <Input id="sg-cards" inputMode="numeric" className="num" aria-invalid={!!errs.cards} {...form.register("cards")} />
            </Field>
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
