import { useMemo, useState, type ReactNode } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Dialog } from "radix-ui";
import type { Schemas, Tier } from "@/api/client";
import { accountsQuery, modelsQuery, requestLogsInfinite } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { DataTable, RowCard, columnHelper } from "@/components/DataTable";
import { StatusCode, TierTag } from "@/components/Tags";
import { EmptyState } from "@/components/Feedback";
import { SheetContent } from "@/components/ui/dialog";
import { Input, NativeSelect } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { TIER_LIST } from "@/lib/constants";
import { formatDateTime, formatInt, formatTokens, formatUSD } from "@/lib/format";
import { useDebounced } from "@/lib/hooks";
import { cn, shortId } from "@/lib/utils";

interface Search {
  user?: string;
  key?: string;
  account?: string;
  tier?: Tier;
  model?: string;
  status?: number;
}

const uuid = (v: unknown) => (typeof v === "string" && /^[0-9a-f-]{36}$/i.test(v) ? v : undefined);

export const Route = createFileRoute("/_app/admin/logs")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    user: uuid(s.user),
    key: uuid(s.key),
    account: uuid(s.account),
    tier: TIER_LIST.includes(s.tier as Tier) ? (s.tier as Tier) : undefined,
    model: typeof s.model === "string" && s.model ? s.model : undefined,
    status: Number.isInteger(Number(s.status)) && Number(s.status) >= 100 && Number(s.status) <= 599 ? Number(s.status) : undefined,
  }),
  component: RequestLogs,
});

type Log = Schemas["RequestLog"];

function RequestLogs() {
  const { t } = useTranslation();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: "/admin/logs" });
  const set = (patch: Partial<Search>) => navigate({ search: (s) => ({ ...s, ...patch }), replace: true });
  const accounts = useQuery(accountsQuery);
  const models = useQuery(modelsQuery);
  const [status, setStatus] = useState(search.status ? String(search.status) : "");
  const [user, setUser] = useState(search.user ?? "");
  const [key, setKey] = useState(search.key ?? "");
  const debouncedStatus = useDebounced(status, 400);
  const debouncedUser = useDebounced(user.trim(), 400);
  const debouncedKey = useDebounced(key.trim(), 400);
  const statusNum = /^\d{3}$/.test(debouncedStatus) ? Number(debouncedStatus) : undefined;
  const filter = {
    user_id: uuid(debouncedUser),
    key_id: uuid(debouncedKey),
    account_id: search.account,
    tier: search.tier,
    model: search.model,
    status_code: statusNum,
    limit: 100,
  };
  const q = useInfiniteQuery({ ...requestLogsInfinite(filter), placeholderData: keepPreviousData });
  const rows = useMemo(() => q.data?.pages.flatMap((p) => p.items) ?? [], [q.data]);
  const [open, setOpen] = useState<Log | null>(null);
  const accName = (id?: string | null) => accounts.data?.items.find((a) => a.id === id)?.name ?? (id ? shortId(id) : "—");

  const col = columnHelper<Log>();
  const columns = [
    col.accessor("created_at", { header: t("usage.time"), cell: (c) => <span className="num text-[12px]">{formatDateTime(c.getValue())}</span> }),
    col.display({
      id: "user",
      header: t("adminOverview.user"),
      cell: (c) =>
        c.row.original.user_id ? (
          <button type="button" className="num text-[12px] underline underline-offset-4" onClick={(e) => (e.stopPropagation(), setUser(c.row.original.user_id!))}>
            {shortId(c.row.original.user_id)}
          </button>
        ) : (
          "—"
        ),
    }),
    col.accessor("model", { header: t("usage.model"), cell: (c) => <span className="font-semibold">{c.getValue()}</span> }),
    col.accessor("tier", { header: t("usage.tier"), cell: (c) => <TierTag tier={c.getValue()} /> }),
    col.display({ id: "account", header: t("subs.account"), cell: (c) => <span className="text-[12px]">{accName(c.row.original.account_id)}</span> }),
    col.accessor("status_code", { header: t("usage.status"), cell: (c) => <StatusCode code={c.getValue()} /> }),
    col.accessor("billed_micro", { header: t("usage.billed"), cell: (c) => <span className="num">{formatUSD(c.getValue())}</span> }),
    col.accessor("upstream_cost_micro", { header: t("adminOverview.upstreamCol"), cell: (c) => <span className="num text-muted">{formatUSD(c.getValue())}</span> }),
    col.accessor("first_byte_ms", { header: t("usage.firstByte"), cell: (c) => <span className="num text-[12px]">{(c.getValue() / 1000).toFixed(2)}s</span> }),
    col.accessor("retries", { header: t("logs.retries"), cell: (c) => <span className={cn("num text-[12px]", c.getValue() > 0 && "bg-yellow px-1 text-on-yellow")}>{c.getValue()}</span> }),
  ];

  return (
    <>
      <PageHeader code="OB-12" title={t("logs.title")} />
      <div className="flex flex-col gap-6">
        <div className="grid gap-3 border-2 border-ink p-4 sm:grid-cols-3 xl:grid-cols-6">
          <Field label={t("logs.userId")} htmlFor="lg-user">
            <Input id="lg-user" className="num" placeholder="uuid" value={user} onChange={(e) => setUser(e.target.value)} />
          </Field>
          <Field label={t("logs.keyId")} htmlFor="lg-key">
            <Input id="lg-key" className="num" placeholder="uuid" value={key} onChange={(e) => setKey(e.target.value)} />
          </Field>
          <Field label={t("subs.account")} htmlFor="lg-acc">
            <NativeSelect id="lg-acc" value={search.account ?? ""} onChange={(e) => set({ account: e.target.value || undefined })}>
              <option value="">{t("common.all")}</option>
              {accounts.data?.items.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </NativeSelect>
          </Field>
          <Field label={t("usage.tier")} htmlFor="lg-tier">
            <NativeSelect id="lg-tier" value={search.tier ?? ""} onChange={(e) => set({ tier: (e.target.value || undefined) as Tier | undefined })}>
              <option value="">{t("common.all")}</option>
              {TIER_LIST.map((x) => (
                <option key={x} value={x}>
                  {t(`tier.${x}`)}
                </option>
              ))}
            </NativeSelect>
          </Field>
          <Field label={t("usage.model")} htmlFor="lg-model">
            <NativeSelect id="lg-model" value={search.model ?? ""} onChange={(e) => set({ model: e.target.value || undefined })}>
              <option value="">{t("common.all")}</option>
              {models.data?.items.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </NativeSelect>
          </Field>
          <Field label={t("usage.status")} htmlFor="lg-status" error={status && !/^\d{3}$/.test(status) ? t("logs.statusFormat") : undefined}>
            <Input id="lg-status" inputMode="numeric" className="num" placeholder="200" value={status} onChange={(e) => setStatus(e.target.value)} />
          </Field>
        </div>
        {(user || key || status || search.account || search.tier || search.model) ? (
          <div>
            <Button
              size="sm"
              onClick={() => {
                setUser("");
                setKey("");
                setStatus("");
                void navigate({ search: {}, replace: true });
              }}
            >
              {t("common.clearFilters")}
            </Button>
          </div>
        ) : null}
        <Section n={1} title={t("logs.list")} bodyClassName="p-0 sm:p-4">
          <DataTable
            label={t("logs.list")}
            columns={columns}
            data={rows}
            rowKey={(r) => r.request_id}
            loading={q.isPending}
            error={q.error}
            onRetry={() => q.refetch()}
            hasMore={q.hasNextPage}
            fetchingMore={q.isFetchingNextPage}
            onLoadMore={() => void q.fetchNextPage()}
            onRowClick={setOpen}
            virtualizeFrom={40}
            className="max-sm:p-4"
            empty={<EmptyState className="m-4 sm:m-0" title={t("usage.noData")} description={t("logs.emptyHint")} />}
            renderCard={(r) => (
              <RowCard
                onClick={() => setOpen(r)}
                title={r.model}
                aside={<TierTag tier={r.tier} />}
                lines={[
                  [t("usage.time"), <span className="num">{formatDateTime(r.created_at)}</span>],
                  [t("usage.billed"), <span className="num">{formatUSD(r.billed_micro)}</span>],
                  [t("usage.status"), <StatusCode code={r.status_code} />],
                ]}
              />
            )}
          />
        </Section>
      </div>
      <Dialog.Root open={!!open} onOpenChange={(o) => !o && setOpen(null)}>
        {open ? (
          <SheetContent title={t("usage.detailTitle")}>
            <AdminLogDetail log={open} accountName={accName(open.account_id)} />
          </SheetContent>
        ) : null}
      </Dialog.Root>
    </>
  );
}

function AdminLogDetail({ log, accountName }: { log: Log; accountName: string }) {
  const { t } = useTranslation();
  const rows: [string, ReactNode][] = [
    [t("usage.time"), formatDateTime(log.created_at)],
    [t("usage.requestId"), <span className="break-all">{log.request_id}</span>],
    [
      t("adminOverview.user"),
      log.user_id ? (
        <Link to="/admin/users/$userId" params={{ userId: log.user_id }} className="underline underline-offset-4">
          {log.user_id}
        </Link>
      ) : (
        "—"
      ),
    ],
    [t("usage.key"), log.key_id ?? "—"],
    [
      t("subs.account"),
      log.account_id ? (
        <Link to="/admin/accounts/$accountId" params={{ accountId: log.account_id }} className="underline underline-offset-4">
          {accountName}
        </Link>
      ) : (
        "—"
      ),
    ],
    [t("usage.model"), log.model_raw === log.model ? log.model : `${log.model} (${log.model_raw})`],
    [t("usage.tier"), <TierTag tier={log.tier} />],
    [t("logs.subscription"), log.subscription_id ? `No.${shortId(log.subscription_id)}` : "—"],
    [t("usage.endpoint"), log.endpoint],
    [t("usage.stream"), log.stream ? t("common.yes") : t("common.no")],
    [t("usage.status"), <StatusCode code={log.status_code} />],
    [t("usage.errorType"), log.error_type || "—"],
    [t("logs.retries"), log.retries],
    [t("logs.multipliers"), `${log.tier_multiplier} × ${log.model_multiplier}`],
    [t("logs.sessionHash"), <span className="break-all">{log.session_hash || "—"}</span>],
    [t("logs.clientIp"), log.client_ip || "—"],
  ];
  return (
    <div className="flex flex-col gap-5 p-5">
      <div className="grid grid-cols-2 border-2 border-ink">
        <div className="p-3">
          <div className="label-mono text-muted">{t("usage.billed")}</div>
          <div className="num text-[26px]">{formatUSD(log.billed_micro)}</div>
        </div>
        <div className="border-l-2 border-ink p-3">
          <div className="label-mono text-muted">{t("adminOverview.upstreamCol")}</div>
          <div className="num text-[26px]">{formatUSD(log.upstream_cost_micro)}</div>
        </div>
        {log.unpriced || log.usage_missing ? <p className="col-span-2 border-t border-ink px-3 py-2 text-[12px]">{log.unpriced ? t("usage.unpriced") : t("usage.usageMissing")}</p> : null}
      </div>
      <dl className="grid grid-cols-3 border border-ink text-[13px]">
        {(
          [
            [t("usage.latency"), `${(log.latency_ms / 1000).toFixed(2)}s`],
            [t("usage.firstByte"), `${(log.first_byte_ms / 1000).toFixed(2)}s`],
            [t("logs.retries"), String(log.retries)],
          ] as const
        ).map(([k, v], i) => (
          <div key={k} className={i ? "border-l border-ink p-2" : "p-2"}>
            <dt className="label-mono text-muted">{k}</dt>
            <dd className="num text-[16px]">{v}</dd>
          </div>
        ))}
      </dl>
      <dl className="grid grid-cols-2 border border-ink text-[13px]">
        {(
          [
            [t("usage.inputTokens"), log.input_tokens],
            [t("usage.cacheRead"), log.cache_read_tokens],
            [t("usage.cacheWrite"), log.cache_write_tokens],
            [t("usage.outputTokens"), log.output_tokens],
          ] as const
        ).map(([k, v], i) => (
          <div key={k} className={cn("p-2", i % 2 && "border-l border-ink", i >= 2 && "border-t border-ink")}>
            <dt className="label-mono text-muted">{k}</dt>
            <dd className="num text-[16px]" title={formatInt(v)}>
              {formatTokens(v)}
            </dd>
          </div>
        ))}
      </dl>
      <dl className="divide-y divide-rule border-y-2 border-ink text-[13px]">
        {rows.map(([k, v]) => (
          <div key={k} className="grid grid-cols-[110px_1fr] gap-3 py-2">
            <dt className="label-mono self-center text-muted">{k}</dt>
            <dd className="num min-w-0">{v}</dd>
          </div>
        ))}
      </dl>
      {log.user_agent ? <p className="num text-[11px] break-all text-muted">{log.user_agent}</p> : null}
    </div>
  );
}
