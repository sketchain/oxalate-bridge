import { useEffect } from "react";
import { Command } from "cmdk";
import { Dialog as D } from "radix-ui";
import { useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Copy, CornerDownLeft, Plus, Server } from "lucide-react";
import { toast } from "sonner";
import { accountsQuery, keySecretQuery, keysQuery } from "@/api/queries";
import { adminNav, userNav } from "@/lib/nav";
import { copyText } from "@/lib/hooks";
import { errorText } from "@/lib/errors";

/** Opens the palette on ⌘K / Ctrl+K. */
export function useCommandPaletteHotkey(open: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        open();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);
}

const itemClass =
  "flex cursor-pointer items-center gap-3 px-3 py-2.5 text-sm data-[selected=true]:bg-yellow data-[selected=true]:text-on-yellow";

export function CommandPalette({ open, onOpenChange, admin }: { open: boolean; onOpenChange: (o: boolean) => void; admin: boolean }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const myKeys = useQuery({ ...keysQuery, enabled: open });
  const accounts = useQuery({ ...accountsQuery, enabled: open && admin });

  const run = (fn: () => void | Promise<void>) => {
    onOpenChange(false);
    void fn();
  };
  const nav = admin ? [...userNav, ...adminNav] : userNav;

  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-50 bg-[color-mix(in_srgb,var(--ink)_45%,transparent)]" />
        <D.Content
          aria-describedby={undefined}
          className="fixed top-[12vh] left-1/2 z-50 w-[calc(100vw-32px)] max-w-xl -translate-x-1/2 border-2 border-ink bg-bg"
        >
          <D.Title className="sr-only">{t("palette.title")}</D.Title>
          <Command label={t("palette.title")} loop>
            <div className="flex items-center gap-2 border-b-2 border-ink px-3">
              <span className="label-mono bg-ink px-1.5 py-0.5 text-bg">⌘K</span>
              <Command.Input autoFocus placeholder={t("palette.placeholder")} className="h-12 flex-1 bg-transparent text-[15px] outline-none placeholder:text-muted" />
            </div>
            <Command.List className="max-h-[60vh] overflow-y-auto py-1">
              <Command.Empty className="px-3 py-6 text-center text-sm text-muted">{t("palette.empty")}</Command.Empty>
              <Command.Group heading={t("palette.actions")} className="[&_[cmdk-group-heading]]:label-mono [&_[cmdk-group-heading]]:px-3 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-muted">
                <Command.Item className={itemClass} value={`new key ${t("palette.newKey")}`} onSelect={() => run(() => navigate({ to: "/keys", search: { new: true } }))}>
                  <Plus className="size-4" aria-hidden />
                  {t("palette.newKey")}
                </Command.Item>
                {myKeys.data?.items.map((k) => (
                  <Command.Item
                    key={k.id}
                    className={itemClass}
                    value={`copy key ${k.name} ${k.prefix}`}
                    onSelect={() =>
                      run(async () => {
                        try {
                          const s = await qc.fetchQuery(keySecretQuery(k.id));
                          await copyText(s.key);
                          toast.success(t("common.copied"));
                        } catch (e) {
                          toast.error(errorText(t, e));
                        }
                      })
                    }
                  >
                    <Copy className="size-4" aria-hidden />
                    <span className="flex-1 truncate">{t("palette.copyKey", { name: k.name || k.prefix })}</span>
                    <span className="num text-[11px] opacity-70">{k.prefix}</span>
                  </Command.Item>
                ))}
                {admin
                  ? accounts.data?.items.map((a) => (
                      <Command.Item
                        key={a.id}
                        className={itemClass}
                        value={`open account ${a.name}`}
                        onSelect={() => run(() => navigate({ to: "/admin/accounts/$accountId", params: { accountId: a.id } }))}
                      >
                        <Server className="size-4" aria-hidden />
                        {t("palette.openAccount", { name: a.name })}
                      </Command.Item>
                    ))
                  : null}
              </Command.Group>
              <Command.Group heading={t("palette.pages")} className="[&_[cmdk-group-heading]]:label-mono [&_[cmdk-group-heading]]:px-3 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-muted">
                {nav.map((item) => (
                  <Command.Item key={item.to} className={itemClass} value={`${item.n} ${t(item.label)} ${item.to}`} onSelect={() => run(() => navigate({ to: item.to }))}>
                    <span className="num w-5 text-[11px]">{item.n}</span>
                    <item.icon className="size-4" aria-hidden />
                    <span className="flex-1">{t(item.label)}</span>
                    <CornerDownLeft className="size-3.5 opacity-60" aria-hidden />
                  </Command.Item>
                ))}
              </Command.Group>
            </Command.List>
          </Command>
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}
