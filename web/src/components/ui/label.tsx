import * as React from "react";
import { cn } from "@/lib/utils";

export function Label({ className, ...props }: React.LabelHTMLAttributes<HTMLLabelElement>) {
  return <label className={cn("label-mono block text-ink", className)} {...props} />;
}

/** A form row: label, control, hint and inline error. */
export function Field({
  label,
  htmlFor,
  hint,
  error,
  children,
  className,
}: {
  label: React.ReactNode;
  htmlFor?: string;
  hint?: React.ReactNode;
  error?: string;
  children: React.ReactNode;
  className?: string;
}) {
  const hintId = htmlFor ? `${htmlFor}-hint` : undefined;
  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {error ? (
        <p id={hintId} role="alert" className="text-[12px] font-semibold text-ink">
          <span className="mr-1.5 inline-block bg-red px-1 label-mono text-on-red">!</span>
          {error}
        </p>
      ) : hint ? (
        <p id={hintId} className="text-[12px] text-muted">
          {hint}
        </p>
      ) : null}
    </div>
  );
}
