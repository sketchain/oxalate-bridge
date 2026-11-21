import { useTranslation } from "react-i18next";
import type { Tier } from "@/api/client";
import { cn } from "@/lib/utils";

const base = "label-mono inline-flex h-[22px] items-center whitespace-nowrap px-1.5 leading-none";

export type StatusKind =
  | "normal"
  | "penalized"
  | "stale"
  | "cooldown"
  | "error"
  | "unavailable"
  | "disabled"
  | "pending"
  | "five_hour_full"
  | "week_full"
  | "paused"
  | "full";

const FULL_KINDS: ReadonlySet<StatusKind> = new Set(["five_hour_full", "week_full", "paused", "full"]);

/** Square status tag. The "full" kinds (hard exhausted) get a 1px ink frame with an 8px hazard stripe on the left. */
export function StatusTag({ kind, className }: { kind: StatusKind; className?: string }) {
  const { t } = useTranslation();
  if (FULL_KINDS.has(kind)) {
    return (
      <span data-status={kind} className={cn(base, "border border-ink bg-bg pr-1.5 pl-0 text-ink", className)}>
        {/* Fixed yellow / black in both themes, like a warning label. */}
        <span aria-hidden="true" className="hazard-sm mr-1.5 w-2 shrink-0 self-stretch" />
        {t(`status.${kind}`)}
      </span>
    );
  }
  const styles: Record<StatusKind, string> = {
    normal: "bg-ink text-bg",
    penalized: "border border-ink bg-yellow text-on-yellow",
    stale: "border border-dashed border-ink text-ink",
    cooldown: "border border-ink border-l-[4px] border-l-yellow bg-bg pl-1.5 text-ink",
    error: "bg-red text-on-red",
    unavailable: "bg-red text-on-red",
    disabled: "border border-rule text-muted",
    pending: "border border-dashed border-ink text-ink",
    five_hour_full: "",
    week_full: "",
    paused: "",
    full: "",
  };
  return (
    <span data-status={kind} className={cn(base, styles[kind], className)}>
      {t(`status.${kind}`)}
    </span>
  );
}

/** Tier tag: plan = ink, standard = outlined, force = yellow with ink border. */
export function TierTag({ tier, className }: { tier: Tier | string; className?: string }) {
  const { t } = useTranslation();
  const styles: Record<string, string> = {
    plan: "bg-ink text-bg",
    standard: "border border-ink text-ink",
    force: "border border-ink bg-yellow text-on-yellow",
  };
  if (!tier) return <span className={cn(base, "border border-rule text-muted", className)}>—</span>;
  return <span className={cn(base, styles[tier] ?? "border border-rule text-muted", className)}>{t(`tier.${tier}`, { defaultValue: tier })}</span>;
}

/** A key's tiers in order, joined by arrows. */
export function TierChain({ tiers, className }: { tiers: readonly (Tier | string)[]; className?: string }) {
  const { t } = useTranslation();
  return (
    <span className={cn("inline-flex flex-wrap items-center gap-1", className)} aria-label={t("keys.chainLabel", { chain: tiers.map((x) => t(`tier.${x}`)).join(" → ") })}>
      {tiers.map((tier, i) => (
        <span key={tier} className="inline-flex items-center gap-1" aria-hidden="true">
          {i > 0 ? <span className="num text-[12px] text-muted">→</span> : null}
          <TierTag tier={tier} />
        </span>
      ))}
    </span>
  );
}

/** Marks an estimated figure. */
export function EstimatedTag({ className }: { className?: string }) {
  const { t } = useTranslation();
  return <span className={cn(base, "h-[18px] border border-dashed border-ink px-1 text-[10px] text-ink", className)}>{t("common.estimated")}</span>;
}

/** Small yellow count badge (navigation). */
export function CountBadge({ n, className }: { n: number; className?: string }) {
  if (n <= 0) return null;
  return <span className={cn("num inline-flex h-5 min-w-5 items-center justify-center border border-ink bg-yellow px-1 text-[11px] font-bold text-on-yellow", className)}>{n > 99 ? "99+" : n}</span>;
}

/** HTTP status: plain for 2xx, red plate otherwise. */
export function StatusCode({ code }: { code: number }) {
  return code >= 200 && code < 300 ? <span className="num text-[12px]">{code}</span> : <span className="num bg-red px-1 text-[12px] text-on-red">{code}</span>;
}

/** "预留超出剩余": a negative headroom (oversold account). */
export function OversoldTag({ className }: { className?: string }) {
  const { t } = useTranslation();
  return <span className={cn(base, "h-[18px] border border-red px-1 text-[10px] text-red", className)}>{t("accounts.oversoldTag")}</span>;
}

/** "超卖账号 N 个，合计超出 $X" (or a quiet "none" line). */
export function OversoldLine({ count, amount, className }: { count: number; amount: string; className?: string }) {
  const { t } = useTranslation();
  return (
    <p data-testid="oversold-line" className={cn("num text-[12px]", count > 0 ? "font-semibold text-red" : "text-muted", className)}>
      {count > 0 ? t("accounts.oversoldLine", { n: count, amount }) : t("accounts.noOversold")}
    </p>
  );
}
