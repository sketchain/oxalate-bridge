import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { formatDate } from "@/lib/format";
import { startOfDay } from "@/lib/usage";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

export type RangePreset = "today" | "7d" | "30d" | "custom";

export interface RangeValue {
  range: RangePreset;
  from?: string; // YYYY-MM-DD (custom)
  to?: string; // YYYY-MM-DD inclusive (custom)
}

/** [from, to) instants of a range in local days. */
export function resolveRange(v: RangeValue, now = new Date()): { from: Date; to: Date } {
  switch (v.range) {
    case "7d":
      return { from: startOfDay(now, -6), to: startOfDay(now, 1) };
    case "30d":
      return { from: startOfDay(now, -29), to: startOfDay(now, 1) };
    case "custom": {
      const from = v.from ? new Date(`${v.from}T00:00:00`) : startOfDay(now, -6);
      const toDay = v.to ? new Date(`${v.to}T00:00:00`) : startOfDay(now);
      const to = startOfDay(toDay, 1);
      return to > from ? { from, to } : { from, to: startOfDay(from, 1) };
    }
    default:
      return { from: startOfDay(now), to: startOfDay(now, 1) };
  }
}

/** Preset buttons (today / 7 days / 30 days / custom) plus date inputs. */
export function RangeFilter({ value, onChange }: { value: RangeValue; onChange: (v: RangeValue) => void }) {
  const { t } = useTranslation();
  const presets: RangePreset[] = ["today", "7d", "30d", "custom"];
  const today = formatDate(new Date());
  return (
    <div className="flex flex-col gap-1.5">
      <span className="label-mono" id="range-label">
        {t("usage.range")}
      </span>
      <div className="flex flex-wrap items-end gap-2">
        <div role="group" aria-labelledby="range-label" className="flex border-2 border-ink">
          {presets.map((p) => (
            <button
              key={p}
              type="button"
              aria-pressed={value.range === p}
              onClick={() => onChange(p === "custom" ? { range: p, from: value.from ?? formatDate(startOfDay(new Date(), -6)), to: value.to ?? today } : { range: p })}
              className={cn("h-9 border-l border-ink px-3 text-[13px] font-semibold first:border-l-0", value.range === p ? "bg-yellow text-on-yellow" : "hover:bg-rule")}
            >
              {t(`usage.preset.${p}`)}
            </button>
          ))}
        </div>
        {value.range === "custom" ? (
          <div className="flex items-end gap-2">
            <div>
              <Label htmlFor="range-from" className="sr-only">
                {t("usage.from")}
              </Label>
              <Input id="range-from" type="date" className="num h-10 w-[150px]" max={value.to ?? today} value={value.from ?? ""} onChange={(e) => onChange({ ...value, from: e.target.value })} />
            </div>
            <span className="pb-2">→</span>
            <div>
              <Label htmlFor="range-to" className="sr-only">
                {t("usage.to")}
              </Label>
              <Input id="range-to" type="date" className="num h-10 w-[150px]" min={value.from} max={today} value={value.to ?? ""} onChange={(e) => onChange({ ...value, to: e.target.value })} />
            </div>
          </div>
        ) : null}
      </div>
    </div>
  );
}
