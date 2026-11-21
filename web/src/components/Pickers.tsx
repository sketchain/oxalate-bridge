import { useId, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { Schemas } from "@/api/client";
import { accountsQuery, usersInfinite } from "@/api/queries";
import { accountKind } from "@/lib/accounts";
import { formatUSD } from "@/lib/format";
import { useDebounced } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { Input } from "./ui/input";
import { StatusTag } from "./Tags";

/** Searchable single-choice list of users (radio group). */
export function UserPicker({ value, onChange, label }: { value: string; onChange: (id: string, u: Schemas["AdminUser"]) => void; label: string }) {
  const { t } = useTranslation();
  const id = useId();
  const [search, setSearch] = useState("");
  const debounced = useDebounced(search.trim(), 250);
  const q = useInfiniteQuery(usersInfinite(debounced));
  const users = q.data?.pages.flatMap((p) => p.items).slice(0, 8) ?? [];
  return (
    <fieldset className="flex flex-col gap-1.5">
      <legend className="label-mono mb-1.5">{label}</legend>
      <Input type="search" aria-label={t("users.search")} placeholder={t("users.search")} value={search} onChange={(e) => setSearch(e.target.value)} />
      <div className="max-h-48 overflow-y-auto border border-ink">
        {users.length === 0 ? <p className="px-3 py-2 text-[13px] text-muted">{q.isPending ? t("common.loading") : t("users.noMatch")}</p> : null}
        {users.map((u) => (
          <label key={u.id} className={cn("flex cursor-pointer items-center gap-2 border-b border-rule px-3 py-2 text-[13px] last:border-b-0 has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-ink", value === u.id && "bg-yellow text-on-yellow")}>
            <input type="radio" name={id} className="sr-only" checked={value === u.id} onChange={() => onChange(u.id, u)} />
            <span className="flex-1 font-semibold">{u.username}</span>
            <span className="num text-[12px]">{formatUSD(u.balance_micro)}</span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

/** Account choice with each account's reservation and capacity. */
export function AccountPicker({ value, onChange, label, exclude }: { value: string; onChange: (a: Schemas["Account"]) => void; label: string; exclude?: string | null }) {
  const { t } = useTranslation();
  const id = useId();
  const q = useQuery(accountsQuery);
  const items = (q.data?.items ?? []).filter((a) => a.id !== exclude);
  return (
    <fieldset className="flex flex-col gap-1.5">
      <legend className="label-mono mb-1.5">{label}</legend>
      <div className="max-h-64 overflow-y-auto border border-ink">
        {items.length === 0 ? <p className="px-3 py-2 text-[13px] text-muted">{q.isPending ? t("common.loading") : t("accounts.emptyTitle")}</p> : null}
        {items.map((a) => (
          <label
            key={a.id}
            className={cn(
              "flex cursor-pointer flex-col gap-1 border-b border-rule px-3 py-2 text-[13px] last:border-b-0 has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-ink",
              value === a.id && "bg-yellow text-on-yellow",
              !a.enabled && "opacity-60",
            )}
          >
            <input type="radio" name={id} className="sr-only" checked={value === a.id} onChange={() => onChange(a)} disabled={!a.enabled} />
            <span className="flex items-center gap-2">
              <span className="flex-1 font-semibold">{a.name}</span>
              <StatusTag kind={accountKind(a)} />
            </span>
            <span className="num text-[12px]">
              {t("subs.capacityLine", {
                reserved: formatUSD(a.runtime.reserved_micro),
                sold: formatUSD(a.sold_upstream_micro),
                total: a.week_total_micro != null ? formatUSD(a.week_total_micro) : "—",
              })}
            </span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}
