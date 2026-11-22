import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { LogIn } from "lucide-react";
import { api, unwrap } from "@/api/client";
import { keys } from "@/api/queries";
import { Logo } from "@/components/Logo";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { ErrorBox } from "@/components/Feedback";
import { Toaster } from "@/components/ui/sonner";
import { pageCode } from "@/lib/format";
import { APP_VERSION } from "@/lib/version";

export const Route = createFileRoute("/login")({
  validateSearch: (s: Record<string, unknown>): { redirect?: string } => ({
    redirect: typeof s.redirect === "string" && s.redirect.startsWith("/") && !s.redirect.startsWith("//") ? s.redirect : undefined,
  }),
  component: LoginPage,
});

function LoginPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { redirect } = Route.useSearch();
  const [error, setError] = useState<unknown>(null);
  const schema = z.object({
    username: z.string().trim().min(1, t("validation.required")).max(64),
    password: z.string().min(1, t("validation.required")).max(256),
  });
  const form = useForm<z.infer<typeof schema>>({ resolver: zodResolver(schema), defaultValues: { username: "", password: "" } });
  const errs = form.formState.errors;

  const onSubmit = form.handleSubmit(async (values) => {
    setError(null);
    try {
      const me = await unwrap(api.POST("/auth/login", { body: values }));
      qc.clear();
      qc.setQueryData(keys.me, me);
      await navigate({ to: redirect ?? "/", replace: true });
    } catch (e) {
      setError(e);
    }
  });

  return (
    <div className="flex min-h-dvh items-center justify-center px-4 py-10">
      <div className="w-full max-w-[420px]">
        <div className="border-2 border-ink">
          <div className="flex items-center justify-between bg-yellow px-4 py-2 text-on-yellow">
            <span className="label-mono">{pageCode("OB-00", new Date())}</span>
            <span className="label-mono">v{APP_VERSION}</span>
          </div>
          <div className="border-b-2 border-ink px-5 py-5">
            <Logo sub="Claude relay panel" />
          </div>
          <form onSubmit={onSubmit} noValidate className="flex flex-col gap-4 px-5 py-5">
            <h1 className="font-display text-[32px]">{t("auth.title")}</h1>
            {error ? <ErrorBox error={error} /> : null}
            <Field label={t("auth.username")} htmlFor="username" error={errs.username?.message}>
              <Input id="username" autoComplete="username" autoFocus aria-invalid={!!errs.username} {...form.register("username")} />
            </Field>
            <Field label={t("auth.password")} htmlFor="password" error={errs.password?.message}>
              <Input id="password" type="password" autoComplete="current-password" aria-invalid={!!errs.password} {...form.register("password")} />
            </Field>
            <Button type="submit" variant="primary" disabled={form.formState.isSubmitting} className="mt-2">
              <LogIn aria-hidden />
              {form.formState.isSubmitting ? t("auth.signingIn") : t("auth.signIn")}
            </Button>
          </form>
        </div>
        <p className="mt-3 text-[12px] text-muted">{t("auth.hint")}</p>
        <Link to="/query" className="mt-4 flex items-center justify-between border-2 border-ink px-4 py-3 text-[14px] font-semibold hover:bg-rule" data-testid="query-link">
          <span>{t("auth.queryLink")}</span>
          <span aria-hidden>→</span>
        </Link>
      </div>
      <Toaster />
    </div>
  );
}
