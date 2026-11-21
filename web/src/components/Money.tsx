import { formatUSD } from "@/lib/format";
import { cn } from "@/lib/utils";

/** An amount in USD (tabular mono); the title shows the exact value. */
export function Money({ micro, className }: { micro: number; className?: string }) {
  return (
    <span className={cn("num", className)} title={`$${(micro / 1_000_000).toFixed(6)}`}>
      {formatUSD(micro)}
    </span>
  );
}
