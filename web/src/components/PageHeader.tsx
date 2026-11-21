import type { ReactNode } from "react";
import { pageCode } from "@/lib/format";
import { useNow } from "@/lib/hooks";

/** Page top: mono code line, 40px condensed title, 2px rule. */
export function PageHeader({ code, title, actions }: { code: string; title: ReactNode; actions?: ReactNode }) {
  const now = useNow(60_000);
  return (
    <header className="mb-6 border-b-2 border-ink pb-4">
      <div className="num text-[12px] text-muted">{pageCode(code, now)}</div>
      <div className="mt-1 flex flex-wrap items-end justify-between gap-3">
        <h1 className="font-display text-[32px] sm:text-[40px]">{title}</h1>
        {actions ? <div className="flex flex-wrap gap-2">{actions}</div> : null}
      </div>
    </header>
  );
}
