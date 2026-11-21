import { useTranslation } from "react-i18next";
import type { Schemas } from "@/api/client";

/** Role tag (admin = ink, user = outlined) plus a "disabled" tag. */
export function UserStatusTag({ u }: { u: Pick<Schemas["AdminUser"], "status" | "role"> }) {
  const { t } = useTranslation();
  return (
    <span className="inline-flex gap-1">
      <span className={u.role === "admin" ? "label-mono bg-ink px-1.5 py-0.5 text-bg" : "label-mono border border-ink px-1.5 py-0.5"}>{t(`role.${u.role}`)}</span>
      {u.status === "disabled" ? <span className="label-mono border border-rule px-1.5 py-0.5 text-muted">{t("users.disabled")}</span> : null}
    </span>
  );
}
