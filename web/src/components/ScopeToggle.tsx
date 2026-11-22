import { useRef, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import type { QuotaScope } from "@/lib/keyLimits";
import { cn } from "@/lib/utils";

const OPTIONS: QuotaScope[] = ["all", "balance"];

/**
 * Square two-cell toggle for the key quota scope: 2px ink frame, no radius,
 * the selected cell inverted (ink fill). A radiogroup with roving tabindex:
 * arrow keys / Home / End move and select, Space / Enter select.
 */
export function ScopeToggle({ value, onChange, labelledBy, id }: { value: QuotaScope; onChange: (v: QuotaScope) => void; labelledBy?: string; id?: string }) {
  const { t } = useTranslation();
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const select = (i: number) => {
    const v = OPTIONS[(i + OPTIONS.length) % OPTIONS.length] ?? "all";
    onChange(v);
    refs.current[OPTIONS.indexOf(v)]?.focus();
  };
  const onKey = (e: KeyboardEvent<HTMLButtonElement>, i: number) => {
    switch (e.key) {
      case "ArrowRight":
      case "ArrowDown":
        e.preventDefault();
        select(i + 1);
        break;
      case "ArrowLeft":
      case "ArrowUp":
        e.preventDefault();
        select(i - 1);
        break;
      case "Home":
        e.preventDefault();
        select(0);
        break;
      case "End":
        e.preventDefault();
        select(OPTIONS.length - 1);
        break;
      case " ":
      case "Enter":
        e.preventDefault();
        select(i);
        break;
    }
  };
  return (
    <div role="radiogroup" id={id} aria-labelledby={labelledBy} className="inline-grid grid-cols-2 border-2 border-ink" data-testid="scope-toggle">
      {OPTIONS.map((o, i) => {
        const on = value === o;
        return (
          <button
            key={o}
            ref={(el) => {
              refs.current[i] = el;
            }}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            onClick={() => onChange(o)}
            onKeyDown={(e) => onKey(e, i)}
            className={cn(
              "h-9 min-w-24 px-3 text-[13px] font-semibold outline-offset-2 focus-visible:outline-2 focus-visible:outline-ink",
              i > 0 && "border-l-2 border-ink",
              on ? "bg-ink text-bg" : "bg-bg text-ink hover:bg-rule",
            )}
          >
            {t(`keyLimits.scope.${o}`)}
          </button>
        );
      })}
    </div>
  );
}
