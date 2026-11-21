import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ChevronRight, LogOut, Shield } from "lucide-react";
import { api, unwrap } from "@/api/client";
import { meQuery } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Button } from "@/components/ui/button";
import { userNav } from "@/lib/nav";
import { formatUSD } from "@/lib/format";

export const Route = createFileRoute("/_app/me")({
  component: MePage,
});

/** Mobile "我的" tab: account summary and the pages without a bottom tab. */
function MePage() {
  const { t } = useTranslation();
  const { data: me } = useSuspenseQuery(meQuery);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const links = userNav.filter((i) => ["/plans", "/billing", "/settings"].includes(i.to));
  return (
    <>
      <PageHeader code="OB-00" title={t("nav.me")} />
      <div className="flex flex-col gap-6">
        <div className="border-2 border-ink">
          <div className="label-mono bg-yellow px-4 py-2 text-on-yellow">{t(`role.${me.role}`)}</div>
          <div className="px-4 py-4">
            <div className="font-display text-[28px]">{me.username}</div>
            <div className="num mt-1 text-[13px] text-muted">{t("me.balance", { amount: formatUSD(me.balance_micro) })}</div>
          </div>
        </div>
        <nav aria-label={t("nav.me")} className="border-2 border-ink">
          <ul className="divide-y divide-rule">
            {links.map((l) => (
              <li key={l.to}>
                <Link to={l.to} className="flex h-12 items-center gap-3 px-4 font-semibold hover:bg-rule">
                  <span className="num w-5 text-[11px] text-muted">{l.n}</span>
                  <l.icon className="size-4" aria-hidden />
                  <span className="flex-1">{t(l.label)}</span>
                  <ChevronRight className="size-4" aria-hidden />
                </Link>
              </li>
            ))}
            {me.role === "admin" ? (
              <li>
                <Link to="/admin" className="flex h-12 items-center gap-3 px-4 font-semibold hover:bg-rule">
                  <span className="num w-5 text-[11px] text-muted">07</span>
                  <Shield className="size-4" aria-hidden />
                  <span className="flex-1">{t("nav.groupAdmin")}</span>
                  <ChevronRight className="size-4" aria-hidden />
                </Link>
              </li>
            ) : null}
          </ul>
        </nav>
        <Button
          onClick={async () => {
            try {
              await unwrap(api.POST("/auth/logout"));
            } catch {
              // ignore
            }
            qc.clear();
            await navigate({ to: "/login", search: { redirect: undefined } });
          }}
        >
          <LogOut aria-hidden />
          {t("auth.logout")}
        </Button>
      </div>
    </>
  );
}
