import { useTranslation } from "react-i18next";
import type { Schemas } from "@/api/client";

/** active / disabled / expired. */
export function KeyStatusTag({ status }: { status: Schemas["KeyStatusValue"] }) {
  const { t } = useTranslation();
  return (
    <span
      className={
        status === "active"
          ? "label-mono inline-block border border-ink px-1.5 py-0.5"
          : status === "expired"
            ? "label-mono inline-block border border-ink bg-red px-1.5 py-0.5 text-on-red"
            : "label-mono inline-block border border-dashed border-ink px-1.5 py-0.5 text-muted"
      }
    >
      {t(`keyStatus.${status}`)}
    </span>
  );
}

