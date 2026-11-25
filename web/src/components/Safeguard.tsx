import { Link, useLocation } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Eye, ShieldAlert } from "lucide-react";
import type { Schemas } from "@/api/client";
import { mySafeguardQuery } from "@/api/queries";
import { cn } from "@/lib/utils";

/** "Administrators can view the requests that trigger the safeguard." Shown wherever a user meets the safeguard. */
export function SafeguardViewNotice({ className }: { className?: string }) {
  const { t } = useTranslation();
  return (
    <p data-testid="safeguard-notice" className={cn("flex items-start gap-2 border border-ink px-3 py-2 text-[12px]", className)}>
      <Eye className="mt-0.5 size-3.5 shrink-0" aria-hidden />
      <span>{t("mySafeguard.adminCanView")}</span>
    </p>
  );
}

/** Lock warning above every page while the user's keys are locked. */
export function SafeguardLockBanner() {
  const path = useLocation({ select: (l) => l.pathname });
  const q = useQuery({ ...mySafeguardQuery, staleTime: 60_000 });
  // The safeguard page shows the panel itself.
  if (!q.data?.locked || path === "/safeguard") return null;
  return <LockPanel s={q.data} link className="mb-6" />;
}

/** The lock panel: hazard stripe, what it means and where to go. */
export function LockPanel({ s, link, className }: { s: Schemas["MySafeguard"]; link?: boolean; className?: string }) {
  const { t } = useTranslation();
  const pending = s.appeal?.status === "pending";
  return (
    <section role="alert" data-testid="safeguard-locked" aria-labelledby="sg-lock-title" className={cn("flex border-2 border-ink", className)}>
      {/* Fixed yellow / black in both themes, like a warning label. */}
      <span aria-hidden="true" className="hazard w-3 shrink-0" />
      <div className="flex min-w-0 flex-1 flex-col gap-2 px-4 py-3">
        <h2 id="sg-lock-title" className="flex items-center gap-2 font-bold">
          <ShieldAlert className="size-4" aria-hidden />
          {t("mySafeguard.lockedTitle")}
        </h2>
        <p className="text-[13px]">{pending ? t("mySafeguard.lockedPending") : t("mySafeguard.lockedBody")}</p>
        <p className="text-[12px] text-muted">{t("mySafeguard.adminCanView")}</p>
        {link ? (
          <Link to="/safeguard" className="self-start text-[13px] font-semibold underline underline-offset-4">
            {pending ? t("mySafeguard.viewAppeal") : t("mySafeguard.goAppeal")}
          </Link>
        ) : null}
      </div>
    </section>
  );
}

/** Appeal status plate: pending = yellow, approved = ink, rejected = red. */
export function AppealStatusTag({ status }: { status: Schemas["SafeguardAppealStatus"] }) {
  const { t } = useTranslation();
  const cls = status === "pending" ? "border border-ink bg-yellow text-on-yellow" : status === "approved" ? "bg-ink text-bg" : "bg-red text-on-red";
  return <span className={cn("label-mono inline-flex h-[22px] items-center px-1.5 whitespace-nowrap", cls)}>{t(`appealStatus.${status}`)}</span>;
}

/** Remaining triggers as a row of squares: filled = used. */
export function AllowanceMeter({ used, allowance, className }: { used: number; allowance: number; className?: string }) {
  const { t } = useTranslation();
  const n = Math.min(allowance, 20);
  return (
    <span role="img" aria-label={t("mySafeguard.meter", { used, allowance })} className={cn("inline-flex flex-wrap gap-1", className)}>
      {Array.from({ length: n }, (_, i) => (
        <span key={i} aria-hidden className={cn("size-4 border-2 border-ink", i < used ? "bg-red" : "bg-bg")} />
      ))}
      {allowance > n ? <span className="num text-[11px] text-muted">+{allowance - n}</span> : null}
    </span>
  );
}
