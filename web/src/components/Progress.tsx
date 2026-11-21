import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { formatPercent } from "@/lib/format";

/**
 * 12px bar, 1.5px ink frame, ink fill. Above the watermark the fill is
 * yellow; at or above 100 % it is the hazard stripe. With a watermark a small
 * downward ink triangle marks its position above the bar.
 */
export function ProgressBar({
  value,
  watermark,
  label,
  className,
}: {
  value: number;
  watermark?: number;
  label: string;
  className?: string;
}) {
  const { t } = useTranslation();
  const v = Number.isFinite(value) ? Math.max(0, value) : 0;
  const full = v >= 100;
  const over = watermark !== undefined && v >= watermark;
  const width = Math.min(100, v);
  const text = watermark !== undefined ? t("progress.withWatermark", { value: formatPercent(v), watermark: formatPercent(watermark, 0) }) : formatPercent(v);
  return (
    <div className={cn("relative", watermark !== undefined && "pt-[7px]", className)}>
      {watermark !== undefined ? (
        <span
          aria-hidden="true"
          className="absolute top-0 size-0 -translate-x-1/2 border-x-[5px] border-t-[6px] border-x-transparent border-t-ink"
          style={{ left: `${Math.min(100, Math.max(0, watermark))}%` }}
        />
      ) : null}
      <div
        role="progressbar"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(Math.min(v, 100))}
        aria-valuetext={text}
        className="h-3 border-[1.5px] border-ink bg-bg"
      >
        <div className={cn("h-full", full ? "hazard" : over ? "bg-yellow" : "bg-ink")} style={{ width: `${width}%` }} />
      </div>
    </div>
  );
}
