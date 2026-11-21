import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/** Mono number plate: ink fill, inverted text, 11px. */
export function NumberPlate({ n, className }: { n: number | string; className?: string }) {
  return <span className={cn("label-mono inline-flex h-5 items-center bg-ink px-1.5 text-bg", className)}>{typeof n === "number" ? String(n).padStart(2, "0") : n}</span>;
}

/** Main block: 2px ink frame with a numbered title bar. */
export function Section({
  n,
  title,
  aside,
  children,
  className,
  bodyClassName,
  id,
}: {
  n: number;
  title: ReactNode;
  aside?: ReactNode;
  children: ReactNode;
  className?: string;
  bodyClassName?: string;
  id?: string;
}) {
  const headingId = id ?? `section-${n}`;
  return (
    <section aria-labelledby={headingId} className={cn("border-2 border-ink", className)}>
      <div className="flex min-h-11 flex-wrap items-center justify-between gap-2 border-b border-ink px-4 py-2">
        <h2 id={headingId} className="flex items-center gap-2.5 text-[15px] font-bold">
          <NumberPlate n={n} />
          {title}
        </h2>
        {aside ? <div className="flex flex-wrap items-center gap-2">{aside}</div> : null}
      </div>
      <div className={cn("p-4", bodyClassName)}>{children}</div>
    </section>
  );
}
