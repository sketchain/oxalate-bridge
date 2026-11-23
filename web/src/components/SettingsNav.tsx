import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Bell, SlidersHorizontal } from "lucide-react";

const tab =
  "-mb-[2px] inline-flex h-10 shrink-0 items-center gap-2 border-2 border-b-0 border-transparent px-4 text-sm font-semibold text-muted hover:text-ink";
const active = "border-ink bg-yellow text-on-yellow hover:text-on-yellow";

/** Tabs between the settings pages. */
export function SettingsNav() {
  const { t } = useTranslation();
  return (
    <nav aria-label={t("settings.title")} className="scroll-x mb-6 flex border-b-2 border-ink">
      <Link to="/settings" activeOptions={{ exact: true }} className={tab} activeProps={{ className: active, "aria-current": "page" }}>
        <SlidersHorizontal className="size-4" aria-hidden />
        {t("settings.general")}
      </Link>
      <Link to="/settings/notifications" className={tab} activeProps={{ className: active, "aria-current": "page" }}>
        <Bell className="size-4" aria-hidden />
        {t("notifications.title")}
      </Link>
    </nav>
  );
}
