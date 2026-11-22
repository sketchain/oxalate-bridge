import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { api, unwrap } from "@/api/client";
import { PageHeader } from "@/components/PageHeader";
import { SettingsNav } from "@/components/SettingsNav";
import { Section } from "@/components/Section";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { useApiMutation } from "@/lib/mutations";
import { setLanguage, LANGUAGES, type Language } from "@/i18n";
import { getTheme, setTheme, type ThemeChoice } from "@/lib/theme";
import { cn } from "@/lib/utils";
import { isIOS, isStandalone, promptInstall, useInstallPrompt } from "@/lib/pwa";
import { Download } from "lucide-react";

export const Route = createFileRoute("/_app/settings/")({
  component: SettingsPage,
});

function Choice<T extends string>({ name, value, options, onChange, label }: { name: string; value: T; options: { v: T; label: string }[]; onChange: (v: T) => void; label: string }) {
  return (
    <fieldset>
      <legend className="label-mono mb-1.5">{label}</legend>
      <div className="flex flex-wrap border-2 border-ink">
        {options.map((o) => (
          <label
            key={o.v}
            className={cn(
              "flex h-10 flex-1 cursor-pointer items-center justify-center border-l border-ink px-4 text-[13px] font-semibold first:border-l-0 has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-ink",
              value === o.v ? "bg-yellow text-on-yellow" : "hover:bg-rule",
            )}
          >
            <input type="radio" name={name} value={o.v} checked={value === o.v} onChange={() => onChange(o.v)} className="sr-only" />
            {o.label}
          </label>
        ))}
      </div>
    </fieldset>
  );
}

function SettingsPage() {
  const { t, i18n } = useTranslation();
  const [theme, setThemeState] = useState<ThemeChoice>(getTheme);
  const schema = z
    .object({
      old_password: z.string().min(1, t("validation.required")),
      new_password: z.string().min(8, t("validation.minPassword")).max(256),
      confirm: z.string(),
    })
    .refine((v) => v.new_password === v.confirm, { path: ["confirm"], message: t("validation.passwordMismatch") });
  type Values = z.infer<typeof schema>;
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { old_password: "", new_password: "", confirm: "" } });
  const errs = form.formState.errors;
  const change = useApiMutation({
    mutationFn: (v: Values) => unwrap(api.POST("/me/password", { body: { old_password: v.old_password, new_password: v.new_password } })),
    success: t("settings.passwordChanged"),
    onSuccess: () => form.reset(),
  });

  return (
    <>
      <PageHeader code="OB-06" title={t("settings.title")} />
      <SettingsNav />
      <div className="grid gap-6 lg:grid-cols-2">
        <Section n={1} title={t("settings.password")}>
          <form onSubmit={form.handleSubmit((v) => change.mutate(v))} noValidate className="flex flex-col gap-4">
            <Field label={t("settings.oldPassword")} htmlFor="old-pw" error={errs.old_password?.message}>
              <Input id="old-pw" type="password" autoComplete="current-password" aria-invalid={!!errs.old_password} {...form.register("old_password")} />
            </Field>
            <Field label={t("settings.newPassword")} htmlFor="new-pw" hint={t("validation.minPassword")} error={errs.new_password?.message}>
              <Input id="new-pw" type="password" autoComplete="new-password" aria-invalid={!!errs.new_password} {...form.register("new_password")} />
            </Field>
            <Field label={t("settings.confirmPassword")} htmlFor="confirm-pw" error={errs.confirm?.message}>
              <Input id="confirm-pw" type="password" autoComplete="new-password" aria-invalid={!!errs.confirm} {...form.register("confirm")} />
            </Field>
            <p className="text-[12px] text-muted">{t("settings.passwordNote")}</p>
            <div>
              <Button type="submit" variant="primary" disabled={change.isPending}>
                {t("settings.changePassword")}
              </Button>
            </div>
          </form>
        </Section>
        <Section n={2} title={t("settings.appearance")}>
          <div className="flex flex-col gap-5">
            <Choice<Language>
              name="language"
              label={t("settings.language")}
              value={(LANGUAGES as readonly string[]).includes(i18n.language) ? (i18n.language as Language) : "zh-CN"}
              options={[
                { v: "zh-CN", label: "简体中文" },
                { v: "en", label: "English" },
              ]}
              onChange={setLanguage}
            />
            <Choice<ThemeChoice>
              name="theme"
              label={t("settings.theme")}
              value={theme}
              options={[
                { v: "system", label: t("settings.themeSystem") },
                { v: "light", label: t("settings.themeLight") },
                { v: "dark", label: t("settings.themeDark") },
              ]}
              onChange={(v) => {
                setTheme(v);
                setThemeState(v);
              }}
            />
            <p className="text-[12px] text-muted">{t("settings.rememberNote")}</p>
          </div>
        </Section>
        <InstallSection />
      </div>
    </>
  );
}

/** Install as an app: the browser's prompt (Android, desktop Chrome / Edge) or the iOS steps. */
function InstallSection() {
  const { t } = useTranslation();
  const prompt = useInstallPrompt();
  const installed = isStandalone();
  return (
    <Section n={3} title={t("pwa.installTitle")}>
      <div className="flex flex-col gap-3">
        <p className="text-[13px] text-muted">{t("pwa.installBody")}</p>
        {installed ? (
          <p className="label-mono" data-testid="install-state">
            {t("pwa.installed")}
          </p>
        ) : prompt ? (
          <div>
            <Button variant="primary" onClick={() => void promptInstall()}>
              <Download aria-hidden />
              {t("pwa.installButton")}
            </Button>
          </div>
        ) : (
          <p className="text-[13px]" data-testid="install-state">
            {isIOS() ? t("pwa.installIOS") : t("pwa.installUnavailable")}
          </p>
        )}
      </div>
    </Section>
  );
}
