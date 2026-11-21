import { Link, useRouter, type ErrorComponentProps } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ShieldAlert } from "lucide-react";
import { Skeleton } from "./ui/skeleton";
import { ErrorBox } from "./Feedback";
import { Button } from "./ui/button";

/** Skeleton shown while a route's loader runs for the first time. */
export function RoutePending() {
  return (
    <div className="flex flex-col gap-4" aria-busy="true">
      <Skeleton className="h-4 w-48" />
      <Skeleton className="h-10 w-72" />
      <Skeleton className="h-24" />
      <Skeleton className="h-64" />
    </div>
  );
}

export function RouteError({ error, reset }: ErrorComponentProps) {
  const router = useRouter();
  return (
    <ErrorBox
      error={error}
      onRetry={() => {
        reset();
        void router.invalidate();
      }}
    />
  );
}

/** Full-page code card used by 403 and 404. */
function CodePage({ code, title, body }: { code: string; title: string; body: string }) {
  const { t } = useTranslation();
  return (
    <div className="mx-auto mt-10 max-w-lg border-2 border-ink">
      <div className="flex items-center justify-between bg-yellow px-4 py-3 text-on-yellow">
        <span className="label-mono">ERROR · {code}</span>
        <ShieldAlert className="size-5" aria-hidden />
      </div>
      <div className="p-5">
        <h1 className="font-display text-[40px]">{title}</h1>
        <p className="mt-2 text-muted">{body}</p>
        <Button asChild variant="primary" className="mt-5">
          <Link to="/">{t("errorsPage.home")}</Link>
        </Button>
      </div>
    </div>
  );
}

export function Forbidden() {
  const { t } = useTranslation();
  return <CodePage code="403" title={t("errorsPage.forbiddenTitle")} body={t("errorsPage.forbiddenBody")} />;
}

export function NotFound() {
  const { t } = useTranslation();
  return <CodePage code="404" title={t("errorsPage.notFoundTitle")} body={t("errorsPage.notFoundBody")} />;
}
