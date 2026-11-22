import { useState } from "react";
import { useTranslation } from "react-i18next";
import { RefreshCw, Share, SquarePlus, X } from "lucide-react";
import { applyUpdate, dismissIosHint, iosHintDismissed, isIOS, isStandalone, useUpdateWaiting } from "@/lib/pwa";

/*
 * Bottom bars of the PWA: "new version, click to refresh" and, on iOS Safari
 * outside the installed app, a one-time hint to add the panel to the home
 * screen. They sit above the phone tab bar (html[data-tabbar], set by the
 * app shell).
 */

const bar =
  "pwa-bar fixed inset-x-4 z-50 mx-auto flex max-w-xl items-center gap-3 border-2 border-ink px-4 py-3 text-[13px] bottom-[calc(16px+env(safe-area-inset-bottom))]";

export function UpdateBanner() {
  const { t } = useTranslation();
  const waiting = useUpdateWaiting();
  if (!waiting) return null;
  return (
    <div role="status" data-testid="update-banner" className={`${bar} bg-yellow text-on-yellow`}>
      <span className="label-mono shrink-0">UPDATE</span>
      <button type="button" onClick={applyUpdate} className="flex min-w-0 flex-1 items-center justify-between gap-2 text-left font-semibold underline-offset-2 hover:underline">
        <span>{t("pwa.updateAvailable")}</span>
        <RefreshCw className="size-4" aria-hidden />
      </button>
    </div>
  );
}

export function IosInstallBanner() {
  const { t } = useTranslation();
  const [show, setShow] = useState(() => isIOS() && !isStandalone() && !iosHintDismissed());
  if (!show) return null;
  return (
    <div role="region" aria-label={t("pwa.iosTitle")} data-testid="ios-install-banner" className={`${bar} bg-bg`}>
      <SquarePlus className="size-5 shrink-0" aria-hidden />
      <div className="min-w-0 flex-1">
        <div className="font-semibold">{t("pwa.iosTitle")}</div>
        <div className="text-muted">
          {t("pwa.iosStepShare")} <Share className="inline size-3.5 align-[-2px]" aria-label={t("pwa.shareIcon")} /> → {t("pwa.iosStepAdd")}
        </div>
      </div>
      <button
        type="button"
        className="inline-flex size-8 shrink-0 items-center justify-center border-2 border-transparent hover:border-ink"
        aria-label={t("pwa.dismiss")}
        onClick={() => {
          dismissIosHint();
          setShow(false);
        }}
      >
        <X className="size-4" aria-hidden />
      </button>
    </div>
  );
}
