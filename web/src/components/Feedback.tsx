import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Copy, Inbox, RotateCw } from "lucide-react";
import { toast } from "sonner";
import { Button, type ButtonProps } from "./ui/button";
import { errorText } from "@/lib/errors";
import { copyText } from "@/lib/hooks";
import { cn } from "@/lib/utils";

/** Empty state: what is missing and the next step. */
export function EmptyState({
  title,
  description,
  action,
  icon,
  className,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
  icon?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col items-start gap-2 border border-dashed border-ink p-5", className)}>
      <div className="flex items-center gap-2 font-semibold">
        {icon ?? <Inbox className="size-4" aria-hidden />}
        {title}
      </div>
      {description ? <p className="text-[13px] text-muted">{description}</p> : null}
      {action ? <div className="mt-1">{action}</div> : null}
    </div>
  );
}

/** Inline error with a retry button; shows the mapped text, never the raw message. */
export function ErrorBox({ error, onRetry, className }: { error: unknown; onRetry?: () => void; className?: string }) {
  const { t } = useTranslation();
  return (
    <div role="alert" className={cn("flex flex-wrap items-center gap-3 border-2 border-red p-3", className)}>
      <span className="inline-flex size-6 items-center justify-center bg-red text-on-red">
        <AlertTriangle className="size-4" aria-hidden />
      </span>
      <span className="text-sm font-semibold">{errorText(t, error)}</span>
      {onRetry ? (
        <Button size="sm" className="ml-auto" onClick={onRetry}>
          <RotateCw aria-hidden />
          {t("common.retry")}
        </Button>
      ) : null}
    </div>
  );
}

/** Copies a value (or one fetched on click) and confirms with a toast. */
export function CopyButton({
  value,
  label,
  showLabel = false,
  ...props
}: { value: string | (() => Promise<string>); label: string; showLabel?: boolean } & Omit<ButtonProps, "value" | "onClick">) {
  const { t } = useTranslation();
  return (
    <Button
      size={showLabel ? "sm" : "icon-sm"}
      aria-label={label}
      title={label}
      onClick={async () => {
        try {
          const text = typeof value === "string" ? value : await value();
          await copyText(text);
          toast.success(t("common.copied"));
        } catch (e) {
          toast.error(errorText(t, e));
        }
      }}
      {...props}
    >
      <Copy aria-hidden />
      {showLabel ? label : null}
    </Button>
  );
}
