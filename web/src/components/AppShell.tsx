import { useState, type ReactNode } from "react";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { LogOut, Menu, Moon, Search, Sun } from "lucide-react";
import { Dialog as D } from "radix-ui";
import { api, unwrap, type Schemas } from "@/api/client";
import { overviewQuery } from "@/api/queries";
import { adminNav, mobileTabs, userNav, type NavItem } from "@/lib/nav";
import { getTheme, isDark, setTheme } from "@/lib/theme";
import { APP_VERSION } from "@/lib/version";
import { cn } from "@/lib/utils";
import { Logo, LogoMark } from "./Logo";
import { CountBadge } from "./Tags";
import { Button } from "./ui/button";
import { SheetContent } from "./ui/dialog";
import { CommandPalette, useCommandPaletteHotkey } from "./CommandPalette";

function isActive(item: NavItem, path: string) {
  return item.exact ? path === item.to : path === item.to || path.startsWith(item.to + "/");
}

function NavList({ items, title, pending, onNavigate }: { items: NavItem[]; title: string; pending: number; onNavigate?: () => void }) {
  const { t } = useTranslation();
  const path = useRouterState({ select: (s) => s.location.pathname });
  return (
    <nav aria-label={title}>
      <div className="label-mono border-b border-ink px-4 pt-4 pb-1.5 text-muted">{title}</div>
      <ul>
        {items.map((item) => {
          const active = isActive(item, path);
          return (
            <li key={item.to}>
              <Link
                to={item.to}
                onClick={onNavigate}
                aria-current={active ? "page" : undefined}
                className={cn(
                  "flex h-10 items-center gap-3 border-b border-rule px-4 text-[14px] font-semibold",
                  active ? "bg-yellow text-on-yellow" : "hover:bg-rule",
                )}
              >
                <span className={cn("num w-5 text-[11px]", active ? "text-on-yellow" : "text-muted")}>{item.n}</span>
                <item.icon className="size-4" aria-hidden />
                <span className="flex-1 truncate">{t(item.label)}</span>
                {item.badge === "pending" ? <CountBadge n={pending} /> : null}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

function ThemeButton({ className }: { className?: string }) {
  const { t } = useTranslation();
  const [dark, setDark] = useState(() => isDark(getTheme()));
  return (
    <Button
      variant="ghost"
      size="icon-sm"
      className={className}
      aria-label={dark ? t("settings.themeLight") : t("settings.themeDark")}
      title={dark ? t("settings.themeLight") : t("settings.themeDark")}
      onClick={() => {
        setTheme(dark ? "light" : "dark");
        setDark(!dark);
      }}
    >
      {dark ? <Sun aria-hidden /> : <Moon aria-hidden />}
    </Button>
  );
}

function useLogout() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return async () => {
    try {
      await unwrap(api.POST("/auth/logout"));
    } catch {
      // the session is gone either way
    }
    qc.clear();
    await navigate({ to: "/login", search: { redirect: undefined } });
  };
}

export function AppShell({ me, children }: { me: Schemas["Me"]; children: ReactNode }) {
  const { t } = useTranslation();
  const admin = me.role === "admin";
  const path = useRouterState({ select: (s) => s.location.pathname });
  const onAdmin = path === "/admin" || path.startsWith("/admin/");
  const [drawer, setDrawer] = useState(false);
  const [palette, setPalette] = useState(false);
  useCommandPaletteHotkey(() => setPalette(true));
  const logout = useLogout();
  const overview = useQuery({ ...overviewQuery, enabled: admin });
  const pending = overview.data?.pending_assignment ?? 0;

  const groups = (onNavigate?: () => void) => (
    <>
      <NavList items={userNav} title={t("nav.groupUser")} pending={0} onNavigate={onNavigate} />
      {admin ? <NavList items={adminNav} title={t("nav.groupAdmin")} pending={pending} onNavigate={onNavigate} /> : null}
    </>
  );

  return (
    <div className="min-h-dvh lg:grid lg:grid-cols-[264px_1fr]">
      <a href="#main" className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-[60] focus:bg-yellow focus:p-2 focus:text-on-yellow">
        {t("common.skipToContent")}
      </a>

      {/* Desktop sidebar */}
      <aside className="sticky top-0 hidden h-dvh flex-col border-r-2 border-ink lg:flex">
        <div className="border-b-2 border-ink px-4 py-4">
          <Link to="/" aria-label={t("nav.home")}>
            <Logo sub={`Claude relay panel · v${APP_VERSION}`} />
          </Link>
        </div>
        <button
          type="button"
          onClick={() => setPalette(true)}
          className="flex h-10 items-center gap-2 border-b border-ink px-4 text-left text-[13px] text-muted hover:text-ink"
        >
          <Search className="size-4" aria-hidden />
          <span className="flex-1">{t("palette.open")}</span>
          <kbd className="num border border-ink px-1 text-[11px] text-ink">⌘K</kbd>
        </button>
        <div className="flex-1 overflow-y-auto">{groups()}</div>
        <div className="flex items-center gap-2 border-t-2 border-ink px-4 py-3">
          <div className="min-w-0 flex-1">
            <div className="truncate font-semibold">{me.username}</div>
            <div className="label-mono text-muted">{t(`role.${me.role}`)}</div>
          </div>
          <ThemeButton />
          <Button variant="ghost" size="icon-sm" aria-label={t("auth.logout")} title={t("auth.logout")} onClick={logout}>
            <LogOut aria-hidden />
          </Button>
        </div>
      </aside>

      {/* Mobile top bar */}
      <header className="sticky top-0 z-40 flex h-14 items-center gap-2 border-b-2 border-ink bg-bg px-4 lg:hidden">
        <Link to="/" aria-label={t("nav.home")} className="flex items-center gap-2">
          <LogoMark size={30} />
          <span className="font-display text-[15px] uppercase">OXALATE&apos;S BRIDGE</span>
        </Link>
        <div className="ml-auto flex items-center gap-1">
          <Button variant="ghost" size="icon-sm" aria-label={t("palette.open")} onClick={() => setPalette(true)}>
            <Search aria-hidden />
          </Button>
          <ThemeButton />
          {admin ? (
            <D.Root open={drawer} onOpenChange={setDrawer}>
              <D.Trigger asChild>
                <Button variant="ghost" size="icon-sm" aria-label={t("nav.menu")} className="relative">
                  <Menu aria-hidden />
                  {pending > 0 ? <span className="absolute -top-0.5 -right-0.5 size-2.5 border border-ink bg-yellow" aria-hidden /> : null}
                </Button>
              </D.Trigger>
              <SheetContent side="left" title={t("nav.menu")}>
                {groups(() => setDrawer(false))}
                <div className="p-4">
                  <Button className="w-full" onClick={logout}>
                    <LogOut aria-hidden />
                    {t("auth.logout")}
                  </Button>
                </div>
              </SheetContent>
            </D.Root>
          ) : null}
        </div>
      </header>

      <main id="main" tabIndex={-1} className={cn("min-w-0 px-4 py-6 sm:px-6 lg:px-10 lg:py-8", !onAdmin && "pb-24 lg:pb-8")}>
        <div className="mx-auto max-w-[1400px]">{children}</div>
      </main>

      {/* Mobile bottom tabs (user side) */}
      {!onAdmin ? (
        <nav aria-label={t("nav.groupUser")} className="fixed inset-x-0 bottom-0 z-40 grid grid-cols-4 border-t-2 border-ink bg-bg pb-[env(safe-area-inset-bottom)] lg:hidden">
          {mobileTabs.map((item) => {
            const active = isActive(item, path) || (item.to === "/me" && ["/plans", "/billing", "/settings"].some((p) => path.startsWith(p)));
            return (
              <Link
                key={item.to}
                to={item.to}
                aria-current={active ? "page" : undefined}
                className={cn("flex h-14 flex-col items-center justify-center gap-0.5 border-l border-ink text-[11px] font-semibold first:border-l-0", active && "bg-yellow text-on-yellow")}
              >
                <item.icon className="size-5" aria-hidden />
                {t(item.label)}
              </Link>
            );
          })}
        </nav>
      ) : null}

      <CommandPalette open={palette} onOpenChange={setPalette} admin={admin} />
    </div>
  );
}
