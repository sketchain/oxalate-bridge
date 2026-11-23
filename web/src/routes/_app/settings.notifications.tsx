import { useCallback, useEffect, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useQuery, useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { BellOff, BellRing, Send, Smartphone, Trash2 } from "lucide-react";
import { ApiError, api, unwrap, type Schemas } from "@/api/client";
import { keys, meQuery, notificationPrefsQuery, pushConfigQuery, pushDevicesQuery } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { SettingsNav } from "@/components/SettingsNav";
import { ErrorBox } from "@/components/Feedback";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { errorText } from "@/lib/errors";
import { formatDateTime, formatUSD, microToInput, parseUSD } from "@/lib/format";
import { useApiMutation } from "@/lib/mutations";
import { isIOS, isStandalone, pushSupported, useSwRegistration, vapidKeyBytes } from "@/lib/pwa";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/settings/notifications")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([queryClient.ensureQueryData(notificationPrefsQuery), queryClient.ensureQueryData(pushDevicesQuery)]),
  component: NotificationsPage,
});

type DeviceState = "unsupported" | "needs-install" | "no-worker" | "denied" | "off" | "on";

/** Push state of this browser: support, permission and its subscription. */
function useThisDevice(devices: Schemas["PushDevice"][]) {
  const reg = useSwRegistration();
  const [endpoint, setEndpoint] = useState<string | null>(null);
  const [checked, setChecked] = useState(false);
  const refresh = useCallback(async () => {
    if (!reg || !("pushManager" in reg)) {
      setChecked(true);
      return;
    }
    try {
      const sub = await reg.pushManager.getSubscription();
      setEndpoint(sub?.endpoint ?? null);
    } catch {
      setEndpoint(null);
    }
    setChecked(true);
  }, [reg]);
  useEffect(() => {
    void refresh();
  }, [refresh]);

  let state: DeviceState;
  if (isIOS() && !isStandalone()) state = "needs-install";
  else if (!pushSupported()) state = "unsupported";
  else if (!reg) state = "no-worker";
  else if (Notification.permission === "denied") state = "denied";
  else if (endpoint && devices.some((d) => d.endpoint === endpoint)) state = "on";
  else state = "off";
  return { state, reg, endpoint, checked, refresh };
}

function ThisDevice({ devices }: { devices: Schemas["PushDevice"][] }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const dev = useThisDevice(devices);
  const config = useQuery({ ...pushConfigQuery, enabled: dev.state === "off" || dev.state === "on" });
  const [busy, setBusy] = useState(false);

  const enable = async () => {
    if (!dev.reg) return;
    setBusy(true);
    try {
      const perm = await Notification.requestPermission();
      if (perm !== "granted") {
        toast.error(t("notifications.permissionDenied"));
        return;
      }
      const key = config.data?.public_key ?? (await qc.fetchQuery(pushConfigQuery)).public_key;
      let sub = await dev.reg.pushManager.getSubscription();
      if (!sub) sub = await dev.reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: vapidKeyBytes(key) });
      const json = sub.toJSON();
      await unwrap(api.POST("/push/subscriptions", { body: { endpoint: sub.endpoint, keys: { p256dh: json.keys?.p256dh ?? "", auth: json.keys?.auth ?? "" } } }));
      await qc.invalidateQueries({ queryKey: keys.pushDevices });
      await dev.refresh();
      toast.success(t("notifications.enabled"));
    } catch (e) {
      toast.error(e instanceof ApiError ? errorText(t, e) : t("notifications.subscribeFailed"));
    } finally {
      setBusy(false);
    }
  };

  const disable = async () => {
    if (!dev.reg) return;
    setBusy(true);
    try {
      const sub = await dev.reg.pushManager.getSubscription();
      const mine = devices.find((d) => d.endpoint === sub?.endpoint);
      if (mine) await unwrap(api.DELETE("/push/subscriptions/{pushSubscriptionId}", { params: { path: { pushSubscriptionId: mine.id } } }));
      await sub?.unsubscribe();
      await qc.invalidateQueries({ queryKey: keys.pushDevices });
      await dev.refresh();
      toast.success(t("notifications.disabled"));
    } catch (e) {
      toast.error(errorText(t, e));
    } finally {
      setBusy(false);
    }
  };

  const test = useApiMutation({
    mutationFn: () => unwrap(api.POST("/push/test")),
    onSuccess: (r) => {
      if (r.devices === 0) toast.info(t("notifications.testNoDevices"));
      else toast.success(t("notifications.testSent", { count: r.devices }));
      void qc.invalidateQueries({ queryKey: keys.pushDevices });
    },
  });

  const on = dev.state === "on";
  return (
    <Section
      n={1}
      title={t("notifications.thisDevice")}
      aside={
        <Button size="sm" onClick={() => test.mutate()} disabled={test.isPending}>
          <Send aria-hidden />
          {t("notifications.sendTest")}
        </Button>
      }
    >
      <div className="flex flex-col gap-4">
        <div className={cn("flex items-start gap-3 border-2 border-ink px-4 py-3", on ? "bg-yellow text-on-yellow" : "bg-bg")} data-testid="device-state" data-state={dev.state}>
          {on ? <BellRing className="mt-0.5 size-5 shrink-0" aria-hidden /> : <BellOff className="mt-0.5 size-5 shrink-0" aria-hidden />}
          <div className="min-w-0 flex-1">
            <div className="label-mono">{t(`notifications.state.${dev.state}`)}</div>
            <p className={cn("mt-1 text-[13px]", on ? "" : "text-muted")}>{t(`notifications.stateHint.${dev.state}`)}</p>
          </div>
        </div>
        {dev.state === "off" || dev.state === "on" ? (
          <div>
            {on ? (
              <Button onClick={disable} disabled={busy}>
                <BellOff aria-hidden />
                {t("notifications.disable")}
              </Button>
            ) : (
              <Button variant="primary" onClick={enable} disabled={busy || !dev.checked}>
                <BellRing aria-hidden />
                {t("notifications.enable")}
              </Button>
            )}
          </div>
        ) : null}
      </div>
    </Section>
  );
}

function Devices({ devices, current }: { devices: Schemas["PushDevice"][]; current: string | null }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const remove = useApiMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/push/subscriptions/{pushSubscriptionId}", { params: { path: { pushSubscriptionId: id } } })),
    success: t("notifications.deviceRemoved"),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.pushDevices }),
  });
  return (
    <Section n={2} title={t("notifications.devices")} aside={<span className="num text-[12px] text-muted">{devices.length}</span>}>
      {devices.length === 0 ? (
        <p className="text-[13px] text-muted">{t("notifications.noDevices")}</p>
      ) : (
        <ul className="divide-y divide-rule border-y-2 border-ink" data-testid="device-list">
          {devices.map((d) => (
            <li key={d.id} className="flex items-center gap-3 py-3" data-testid="device-row">
              <Smartphone className="size-5 shrink-0" aria-hidden />
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-semibold">{d.device_label}</span>
                  {d.endpoint === current ? <span className="label-mono bg-ink px-1.5 py-0.5 text-bg">{t("notifications.current")}</span> : null}
                  {d.failures > 0 ? <span className="label-mono border border-ink px-1.5 py-0.5">{t("notifications.failures", { count: d.failures })}</span> : null}
                </div>
                <div className="num mt-0.5 text-[12px] text-muted">
                  {t("notifications.added", { at: formatDateTime(d.created_at) })}
                  {" · "}
                  {d.last_success_at ? t("notifications.lastSuccess", { at: formatDateTime(d.last_success_at) }) : t("notifications.neverDelivered")}
                </div>
              </div>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t("notifications.removeDevice", { name: d.device_label })}
                onClick={() => remove.mutate(d.id)}
                disabled={remove.isPending}
              >
                <Trash2 aria-hidden />
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}

function Threshold({ value }: { value: number }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [text, setText] = useState(microToInput(value));
  useEffect(() => setText(microToInput(value)), [value]);
  const parsed = parseUSD(text);
  const valid = Number.isFinite(parsed) && parsed >= 0;
  const save = useApiMutation({
    mutationFn: (micro: number) => unwrap(api.PUT("/notifications/prefs", { body: { balance_threshold_micro: micro } })),
    success: t("notifications.thresholdSaved"),
    onSuccess: (p) => qc.setQueryData(keys.notificationPrefs, p),
  });
  return (
    <form
      noValidate
      className="mt-3 flex flex-wrap items-end gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        if (valid && parsed !== value) save.mutate(parsed);
      }}
    >
      <Field label={t("notifications.threshold")} htmlFor="balance-threshold" hint={t("notifications.thresholdHint", { value: formatUSD(value) })} error={valid ? undefined : t("validation.amountNonNegative")}>
        <div className="relative w-40">
          <span className="num pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-muted">$</span>
          <Input id="balance-threshold" inputMode="decimal" className="num pl-6" value={text} aria-invalid={!valid} onChange={(e) => setText(e.target.value)} />
        </div>
      </Field>
      <Button type="submit" variant="primary" disabled={!valid || parsed === value || save.isPending} className="mb-[22px]">
        {t("common.save")}
      </Button>
    </form>
  );
}

function EventGroup({ n, title, items, threshold }: { n: number; title: string; items: Schemas["NotificationPref"][]; threshold?: number }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toggle = useApiMutation({
    mutationFn: (v: { event: Schemas["NotificationEvent"]; enabled: boolean }) => unwrap(api.PUT("/notifications/prefs", { body: { items: [v] } })),
    onMutate: async (v) => {
      await qc.cancelQueries({ queryKey: keys.notificationPrefs });
      const prev = qc.getQueryData<Schemas["NotificationPrefs"]>(keys.notificationPrefs);
      qc.setQueryData<Schemas["NotificationPrefs"]>(keys.notificationPrefs, (old) =>
        old ? { ...old, items: old.items.map((i) => (i.event === v.event ? { ...i, enabled: v.enabled } : i)) } : old,
      );
      return { prev };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(keys.notificationPrefs, ctx.prev);
    },
    onSuccess: (p) => qc.setQueryData(keys.notificationPrefs, p),
  });
  return (
    <Section n={n} title={title}>
      <ul className="divide-y divide-rule border-y-2 border-ink">
        {items.map((p) => {
          const id = `ev-${p.event}`;
          return (
            <li key={p.event} className="py-3" data-testid={`pref-${p.event}`}>
              <div className="flex items-start gap-4">
                <div className="min-w-0 flex-1">
                  <label htmlFor={id} className="font-semibold">
                    {t(`notifications.event.${p.event}`)}
                  </label>
                  <p className="text-[13px] text-muted">{t(`notifications.eventHint.${p.event}`)}</p>
                  {p.default_enabled ? null : <p className="label-mono mt-1 text-muted">{t("notifications.defaultOff")}</p>}
                </div>
                <Switch id={id} checked={p.enabled} onCheckedChange={(v) => toggle.mutate({ event: p.event, enabled: v })} />
              </div>
              {p.event === "balance_low" && threshold !== undefined ? <Threshold value={threshold} /> : null}
            </li>
          );
        })}
      </ul>
    </Section>
  );
}

function NotificationsPage() {
  const { t } = useTranslation();
  const { data: me } = useSuspenseQuery(meQuery);
  const prefs = useQuery(notificationPrefsQuery);
  const devices = useQuery(pushDevicesQuery);
  const reg = useSwRegistration();
  const [current, setCurrent] = useState<string | null>(null);
  useEffect(() => {
    if (!reg || !("pushManager" in reg)) return;
    void reg.pushManager
      .getSubscription()
      .then((s) => setCurrent(s?.endpoint ?? null))
      .catch(() => {});
  }, [reg, devices.data]);

  const err = prefs.error ?? devices.error;
  const adminItems = prefs.data?.items.filter((i) => i.group === "admin") ?? [];
  const userItems = prefs.data?.items.filter((i) => i.group === "user") ?? [];
  return (
    <>
      <PageHeader code="OB-06" title={t("settings.title")} />
      <SettingsNav />
      {err ? <ErrorBox error={err} onRetry={() => void Promise.all([prefs.refetch(), devices.refetch()])} /> : null}
      {!prefs.data || !devices.data ? (
        <Skeleton className="h-96" />
      ) : (
        <div className="grid gap-6 xl:grid-cols-2">
          <div className="flex flex-col gap-6">
            <ThisDevice devices={devices.data.items} />
            <Devices devices={devices.data.items} current={current} />
          </div>
          <div className="flex flex-col gap-6">
            <EventGroup n={3} title={t("notifications.groupUser")} items={userItems} threshold={prefs.data.balance_threshold_micro} />
            {me.role === "admin" ? <EventGroup n={4} title={t("notifications.groupAdmin")} items={adminItems} /> : null}
          </div>
        </div>
      )}
    </>
  );
}
