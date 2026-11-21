import type { ReactNode } from "react";
import { cn } from "@/lib/utils";
import { Skeleton } from "./ui/skeleton";

export interface Stat {
  label: string;
  value: ReactNode;
  note?: ReactNode;
  /** Needs attention: the whole cell is filled yellow. */
  alert?: boolean;
  icon?: ReactNode;
}

/** 2px frame split into N cells: mono label, 30px mono number, 12px note. */
export function StatStrip({ stats, loading, className }: { stats: Stat[]; loading?: boolean; className?: string }) {
  const n = stats.length;
  return (
    <dl
      className={cn(
        "grid grid-cols-2 border-2 border-ink",
        n === 3 ? "lg:grid-cols-3" : n >= 5 ? "lg:grid-cols-5" : "lg:grid-cols-4",
        "[&>div]:border-ink max-lg:[&>div:nth-child(2n)]:border-l max-lg:[&>div:nth-child(n+3)]:border-t lg:[&>div+div]:border-l",
        n % 2 === 1 && "max-lg:[&>div:last-child]:col-span-2",
        className,
      )}
    >
      {stats.map((s, i) => (
        <div key={i} className={cn("flex min-w-0 flex-col gap-1.5 p-3 sm:p-4", s.alert && "bg-yellow text-on-yellow")}>
          <dt className="label-mono flex items-center gap-1.5">
            {s.icon}
            <span className="truncate">
              {String(i + 1).padStart(2, "0")} · {s.label}
            </span>
          </dt>
          <dd className="num truncate text-[24px] leading-tight font-medium sm:text-[30px]">{loading ? <Skeleton className="h-8 w-28" /> : s.value}</dd>
          {s.note ? <dd className={cn("min-h-[18px] text-[12px]", s.alert ? "text-on-yellow" : "text-muted")}>{loading ? <Skeleton className="h-3 w-20" /> : s.note}</dd> : null}
        </div>
      ))}
    </dl>
  );
}
