import * as React from "react";
import { cn } from "@/lib/utils";

export function Input({ className, ...props }: React.InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      className={cn(
        "h-10 w-full min-w-0 border border-ink bg-bg px-3 text-sm text-ink placeholder:text-muted disabled:opacity-50 aria-invalid:border-2 aria-invalid:border-red",
        className,
      )}
      {...props}
    />
  );
}

export function Textarea({ className, ...props }: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      className={cn("min-h-20 w-full border border-ink bg-bg px-3 py-2 text-sm text-ink placeholder:text-muted aria-invalid:border-2 aria-invalid:border-red", className)}
      {...props}
    />
  );
}

export function NativeSelect({ className, children, ...props }: React.SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      className={cn(
        "h-10 w-full min-w-0 appearance-none border border-ink bg-bg bg-[length:10px_6px] bg-[right_12px_center] bg-no-repeat px-3 pr-8 text-sm text-ink",
        "bg-[url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 10 6'%3E%3Cpath d='M0 0h10L5 6z' fill='%235E5E5E'/%3E%3C/svg%3E\")]",
        className,
      )}
      {...props}
    >
      {children}
    </select>
  );
}
