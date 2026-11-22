import { useState, type FormEvent } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Eye, EyeOff, LogIn, Search } from "lucide-react";
import type { Schemas } from "@/api/client";
import { ApiError } from "@/api/client";
import { Logo } from "@/components/Logo";
import { Section } from "@/components/Section";
import { KeyLimitsPanel } from "@/components/KeyLimitsPanel";
import { DataTable, RowCard, columnHelper } from "@/components/DataTable";
import { StatusCode, TierTag } from "@/components/Tags";
import { ErrorBox } from "@/components/Feedback";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Toaster } from "@/components/ui/sonner";
import { KeyStatusTag } from "@/components/KeyStatusTag";
import { formatDateTime, formatTokens, formatUSD, pageCode } from "@/lib/format";
import { APP_VERSION } from "@/lib/version";

export const Route = createFileRoute("/query")({ component: QueryPage });

type Usage = Schemas["PublicKeyUsage"];
type Log = Schemas["MyRequestLog"];

/** Queries the key's usage. The key travels in a header only and is kept in memory (never stored, never in the URL). */
async function fetchUsage(key: string): Promise<Usage> {
  let res: Response;
  try {
    res = await fetch("/api/v1/public/key-usage", { headers: { "x-api-key": key }, credentials: "omit", cache: "no-store" });
  } catch (e) {
    throw new ApiError(0, "network", e instanceof Error ? e.message : String(e));
  }
  const body = await res.json().catch(() => undefined);
  if (!res.ok) throw new ApiError(res.status, body?.error?.code ?? "unknown", body?.error?.message ?? res.statusText);
  return body as Usage;
}

function QueryPage() {
  const { t } = useTranslation();
  const [key, setKey] = useState("");
  const [show, setShow] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [data, setData] = useState<Usage | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!key.trim()) return;
    setBusy(true);
    setError(null);
    try {
      setData(await fetchUsage(key.trim()));
    } catch (err) {
      setData(null);
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="min-h-dvh px-4 py-6 sm:px-6 lg:px-10">
      <div className="mx-auto flex max-w-[1100px] flex-col gap-6">
        <header className="flex flex-wrap items-center justify-between gap-3 border-b-2 border-ink pb-4">
          <Link to="/" aria-label={t("nav.home")}>
            <Logo sub={`Claude relay panel · v${APP_VERSION}`} />
          </Link>
          <Button asChild size="sm">
            <Link to="/login" search={{ redirect: undefined }}>
              <LogIn aria-hidden />
              {t("query.login")}
            </Link>
          </Button>
        </header>
        <div>
          <p className="label-mono text-muted">{pageCode("OB-Q1", new Date())}</p>
          <h1 className="font-display text-[34px] leading-tight sm:text-[44px]">{t("query.title")}</h1>
          <p className="mt-1 max-w-[640px] text-[13px] text-muted">{t("query.intro")}</p>
        </div>

        <form onSubmit={submit} className="flex flex-col gap-2 border-2 border-ink p-4" autoComplete="off">
          <Label htmlFor="query-key">{t("query.keyLabel")}</Label>
          <div className="flex flex-col gap-2 sm:flex-row">
            <div className="relative flex-1">
              <Input
                id="query-key"
                type={show ? "text" : "password"}
                className="num pr-11"
                placeholder="sk-…"
                autoComplete="off"
                spellCheck={false}
                value={key}
                onChange={(e) => setKey(e.target.value)}
              />
              <button
                type="button"
                className="absolute top-1/2 right-1 inline-flex size-8 -translate-y-1/2 items-center justify-center border-2 border-transparent hover:border-ink"
                aria-label={show ? t("query.hide") : t("query.show")}
                aria-pressed={show}
                onClick={() => setShow(!show)}
              >
                {show ? <EyeOff className="size-4" aria-hidden /> : <Eye className="size-4" aria-hidden />}
              </button>
            </div>
            <Button type="submit" variant="primary" disabled={busy || !key.trim()}>
              <Search aria-hidden />
              {busy ? t("query.querying") : t("query.submit")}
            </Button>
          </div>
          <p className="text-[12px] text-muted">{t("query.privacy")}</p>
        </form>

        {error ? <QueryError error={error} /> : null}
        {data ? <QueryResult data={data} /> : null}
      </div>
      <Toaster />
    </div>
  );
}

function QueryError({ error }: { error: unknown }) {
  const { t } = useTranslation();
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div role="alert" className="border-2 border-ink p-4 text-[14px] font-semibold">
        <span className="label-mono mr-2 inline-block bg-red px-1 text-on-red">401</span>
        {t("query.invalidKey")}
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 429) {
    return (
      <div role="alert" className="border-2 border-ink bg-yellow p-4 text-[14px] font-semibold text-on-yellow">
        {t("query.rateLimited")}
      </div>
    );
  }
  return <ErrorBox error={error} />;
}

function QueryResult({ data }: { data: Usage }) {
  const { t } = useTranslation();
  const k = data.key;
  const facts: [string, React.ReactNode][] = [
    [t("query.name"), <span className="font-semibold">{k.name || t("keys.unnamed")}</span>],
    [t("query.prefix"), <span className="num">{k.prefix}</span>],
    [t("keys.status"), <KeyStatusTag status={k.status} />],
    [t("keyLimits.expires"), k.expires_at ? formatDateTime(k.expires_at) : t("keyLimits.never")],
    [t("keyLimits.models"), k.model_allowlist?.length ? k.model_allowlist.join(", ") : t("keyLimits.any")],
  ];
  const col = columnHelper<Log>();
  const columns = [
    col.accessor("created_at", { header: t("usage.time"), cell: (c) => <span className="num text-[12px]">{formatDateTime(c.getValue())}</span> }),
    col.accessor("model", { header: t("usage.model"), cell: (c) => <span className="font-semibold">{c.getValue() || "—"}</span> }),
    col.accessor("tier", { header: t("usage.tier"), cell: (c) => (c.getValue() ? <TierTag tier={c.getValue()} /> : <span className="text-muted">—</span>) }),
    col.display({
      id: "tokens",
      header: t("usage.tokens"),
      cell: (c) => {
        const r = c.row.original;
        return <span className="num text-[12px]">{formatTokens(r.input_tokens + r.cache_read_tokens + r.cache_write_tokens)} / {formatTokens(r.output_tokens)}</span>;
      },
    }),
    col.accessor("billed_micro", { header: t("usage.billed"), cell: (c) => <span className="num">{formatUSD(c.getValue())}</span> }),
    col.accessor("status_code", { header: t("usage.status"), cell: (c) => <StatusCode code={c.getValue()} /> }),
  ];
  return (
    <div className="flex flex-col gap-6" data-testid="query-result">
      <div className="grid items-start gap-6 lg:grid-cols-2">
        <Section n={1} title={t("query.keyInfo")}>
          <dl className="divide-y divide-rule text-[13px]">
            {facts.map(([label, v]) => (
              <div key={label} className="grid grid-cols-[110px_1fr] gap-3 py-2">
                <dt className="label-mono self-center text-muted">{label}</dt>
                <dd className="min-w-0 break-words">{v}</dd>
              </div>
            ))}
          </dl>
        </Section>
        <Section n={2} title={t("keyLimits.usageTitle")}>
          <KeyLimitsPanel limits={data.limits} />
        </Section>
      </div>
      {data.account ? <OwnerBlock a={data.account} /> : null}
      <Section n={data.account ? 4 : 3} title={t("query.recent")} bodyClassName="p-0 sm:p-4">
        <DataTable
          label={t("query.recent")}
          columns={columns}
          data={data.recent}
          rowKey={(r) => r.request_id}
          empty={<p className="p-4 text-[13px] text-muted">{t("query.noRecent")}</p>}
          className="max-sm:p-4"
          renderCard={(r) => (
            <RowCard
              title={r.model || "—"}
              aside={<StatusCode code={r.status_code} />}
              lines={[
                [t("usage.time"), <span className="num">{formatDateTime(r.created_at)}</span>],
                [t("usage.billed"), <span className="num">{formatUSD(r.billed_micro)}</span>],
              ]}
            />
          )}
        />
      </Section>
    </div>
  );
}

function OwnerBlock({ a }: { a: Schemas["OwnerSummary"] }) {
  const { t } = useTranslation();
  const h5 = (list: Schemas["AvailabilityHorizon"][]) => list.find((h) => h.horizon === "5h")?.available_micro ?? 0;
  return (
    <Section n={3} title={t("query.account")}>
      <div className="grid gap-px border-2 border-ink bg-ink sm:grid-cols-3">
        {[
          [t("query.balance"), formatUSD(a.balance_available_micro)],
          [t("query.normal5h"), formatUSD(h5(a.normal))],
          [t("query.force5h"), formatUSD(h5(a.force))],
        ].map(([label, v]) => (
          <div key={label} className="bg-bg p-3">
            <div className="label-mono text-muted">{label}</div>
            <div className="num text-[24px]">{v}</div>
          </div>
        ))}
      </div>
      {a.plans.length ? (
        <ul className="mt-4 divide-y divide-rule border-y-2 border-ink">
          {a.plans.map((p) => (
            <li key={p.subscription_id} className="grid gap-1 py-2.5 text-[13px] sm:grid-cols-[1fr_auto_auto] sm:items-center sm:gap-6">
              <span className="font-semibold">
                {p.plan_name}
                {p.status === "pending_assignment" ? <span className="label-mono ml-2 border border-dashed border-ink px-1">{t("subStatus.pending_assignment")}</span> : null}
              </span>
              <span className="num">
                {t("query.plan5h")} {p.five_hour_available_micro == null ? "—" : formatUSD(p.five_hour_available_micro)}
              </span>
              <span className="num">
                {t("query.planPeriod")} {p.period_available_micro == null ? "—" : formatUSD(p.period_available_micro)}
              </span>
            </li>
          ))}
        </ul>
      ) : null}
    </Section>
  );
}
