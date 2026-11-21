import * as React from "react";
import { Dialog as D } from "radix-ui";
import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";

export const Dialog = D.Root;
export const DialogTrigger = D.Trigger;
export const DialogClose = D.Close;

export function DialogContent({
  className,
  children,
  title,
  description,
  wide,
  ...props
}: React.ComponentProps<typeof D.Content> & { title: React.ReactNode; description?: React.ReactNode; wide?: boolean }) {
  const { t } = useTranslation();
  return (
    <D.Portal>
      <D.Overlay className="fixed inset-0 z-50 bg-[color-mix(in_srgb,var(--ink)_45%,transparent)]" />
      <D.Content
        className={cn(
          "fixed top-1/2 left-1/2 z-50 flex max-h-[calc(100dvh-32px)] w-[calc(100vw-32px)] -translate-x-1/2 -translate-y-1/2 flex-col border-2 border-ink bg-bg",
          wide ? "max-w-2xl" : "max-w-lg",
          className,
        )}
        {...(description ? {} : { "aria-describedby": undefined })}
        {...props}
      >
        <div className="flex items-start justify-between gap-4 border-b-2 border-ink px-5 py-4">
          <div className="min-w-0">
            <D.Title className="font-display text-[22px]">{title}</D.Title>
            {description ? <D.Description className="mt-1 text-[13px] text-muted">{description}</D.Description> : null}
          </div>
          <D.Close className="-mr-1 inline-flex size-8 items-center justify-center border-2 border-transparent hover:border-ink" aria-label={t("common.close")}>
            <X className="size-4" />
          </D.Close>
        </div>
        <div className="overflow-y-auto px-5 py-5">{children}</div>
      </D.Content>
    </D.Portal>
  );
}

export function DialogFooter({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("mt-6 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end", className)} {...props} />;
}

/** Side drawer (sheet) for details and the mobile admin menu. */
export function SheetContent({
  className,
  children,
  title,
  side = "right",
  ...props
}: React.ComponentProps<typeof D.Content> & { title: React.ReactNode; side?: "right" | "left" }) {
  const { t } = useTranslation();
  return (
    <D.Portal>
      <D.Overlay className="fixed inset-0 z-50 bg-[color-mix(in_srgb,var(--ink)_45%,transparent)]" />
      <D.Content
        aria-describedby={undefined}
        className={cn(
          "fixed inset-y-0 z-50 flex w-full flex-col bg-bg sm:w-[480px]",
          side === "right" ? "right-0 border-l-2 border-ink" : "left-0 max-w-[300px] border-r-2 border-ink",
          className,
        )}
        {...props}
      >
        <div className="flex items-center justify-between gap-4 border-b-2 border-ink px-5 py-4">
          <D.Title className="font-display min-w-0 truncate text-[22px]">{title}</D.Title>
          <D.Close className="inline-flex size-8 items-center justify-center border-2 border-transparent hover:border-ink" aria-label={t("common.close")}>
            <X className="size-4" />
          </D.Close>
        </div>
        <div className="flex-1 overflow-y-auto">{children}</div>
      </D.Content>
    </D.Portal>
  );
}
