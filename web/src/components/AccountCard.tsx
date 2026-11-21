import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import type { Schemas } from "@/api/client";
import { accountKind, accountNote } from "@/lib/accounts";
import { formatPercent, formatUSD } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { ProgressBar } from "./Progress";
import { OversoldTag, StatusTag } from "./Tags";

/** Account card: name, tier and k, status, 5h (with watermark) and 7d bars, three figures and a note. */
export function AccountCard({ a }: { a: Schemas["Account"] }) {
  const { t, i18n } = useTranslation();
  const now = useNow(30_000);
  const rt = a.runtime;
  const five = rt.five_hour?.used_percent;
  const week = rt.week?.used_percent;
  const kind = accountKind(a);
  return (
    <article className="flex flex-col border-2 border-ink" aria-label={a.name}>
      <header className="flex items-start justify-between gap-2 border-b border-ink px-4 py-3">
        <div className="min-w-0">
          <h3 className="truncate">
            <Link to="/admin/accounts/$accountId" params={{ accountId: a.id }} className="font-display text-[20px] hover:underline hover:underline-offset-4">
              {a.name}
            </Link>
          </h3>
          <div className="num mt-0.5 text-[11px] text-muted">
            {a.tier || "—"} · k {a.k != null ? a.k.toFixed(2) : "—"} · {a.instance || "—"}
          </div>
        </div>
        <StatusTag kind={kind} />
      </header>
      <div className="flex flex-col gap-3 px-4 py-3">
        <div>
          <div className="num mb-1 flex justify-between text-[11px]">
            <span className="label-mono">5H</span>
            <span>{five != null ? formatPercent(five) : "—"}</span>
          </div>
          <ProgressBar value={five ?? 0} watermark={a.watermark_5h_percent_effective} label={t("accounts.fiveHourUsage", { name: a.name })} />
        </div>
        <div>
          <div className="num mb-1 flex justify-between text-[11px]">
            <span className="label-mono">7D</span>
            <span>{week != null ? formatPercent(week) : "—"}</span>
          </div>
          <ProgressBar value={week ?? 0} label={t("accounts.weekUsage", { name: a.name })} />
        </div>
      </div>
      <dl className="grid grid-cols-3 border-t border-ink text-[12px]">
        {(
          [
            [t("accounts.headroom"), <HeadroomValue key="h" micro={rt.headroom_micro} />],
            [t("accounts.reserved"), formatUSD(rt.reserved_micro)],
            [t("accounts.concurrency"), `${rt.current_concurrency}/${a.max_concurrency ?? "∞"}`],
          ] as const
        ).map(([k, v], i) => (
          <div key={k} className={i > 0 ? "border-l border-ink px-3 py-2" : "px-3 py-2"}>
            <dt className="label-mono text-muted">{k}</dt>
            <dd className="num mt-0.5 min-w-0 text-[14px]">{v}</dd>
          </div>
        ))}
      </dl>
      <p className="min-h-9 truncate border-t border-rule px-4 py-2 text-[12px] text-muted" title={accountNote(a, now, i18n.language, t)}>
        {accountNote(a, now, i18n.language, t) || "—"}
      </p>
    </article>
  );
}

/** Headroom H_A: negative (oversold) is shown as is, in danger red, with a "预留超出剩余" tag. */
export function HeadroomValue({ micro }: { micro: number }) {
  if (micro >= 0) return <span className="block truncate">{formatUSD(micro)}</span>;
  return (
    <span className="flex flex-col items-start gap-0.5" data-testid="headroom-oversold">
      <span className="truncate font-semibold text-red">{formatUSD(micro)}</span>
      <OversoldTag />
    </span>
  );
}
