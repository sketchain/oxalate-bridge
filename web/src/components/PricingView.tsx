import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Search } from "lucide-react";
import type { Schemas, Tier } from "@/api/client";
import { pricingQuery } from "@/api/queries";
import { DataTable, RowCard, columnHelper } from "./DataTable";
import { EmptyState } from "./Feedback";
import { PageHeader } from "./PageHeader";
import { Section } from "./Section";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { TIER_LIST } from "@/lib/constants";
import { cn } from "@/lib/utils";

type Row = Schemas["ModelPricing"];

/** Price per 1M tokens from micro-USD: up to 4 decimals below $1, 2 above, trailing zeros dropped. */
export function perM(micro?: number): string {
  if (micro == null) return "—";
  const usd = micro / 1_000_000;
  const fixed = usd.toFixed(usd < 1 ? 4 : 2);
  const trimmed = fixed.includes(".") ? fixed.replace(/0+$/, "").replace(/\.$/, "") : fixed;
  return `$${trimmed}`;
}

/** The model and price list (user and admin navigation). */
export function PricingView({ code }: { code: string }) {
  const { t } = useTranslation();
  const q = useQuery(pricingQuery);
  const [search, setSearch] = useState("");
  const [tier, setTier] = useState<Tier>("standard");
  const rows = useMemo(() => (q.data?.items ?? []).filter((r) => r.model.includes(search.trim().toLowerCase())), [q.data, search]);
  const mult = q.data?.tier_multipliers;
  const prices = (r: Row) => r[tier];

  const col = columnHelper<Row>();
  const columns = [
    col.accessor("model", {
      header: t("pricing.model"),
      cell: (c) => (
        <span className="flex flex-wrap items-center gap-2">
          <span className="num font-semibold">{c.getValue()}</span>
          {!c.row.original.priced ? <span className="label-mono border border-dashed border-ink px-1">{t("pricing.unpriced")}</span> : null}
        </span>
      ),
    }),
    col.display({ id: "in", header: t("pricing.input"), cell: (c) => <span className="num">{perM(prices(c.row.original)?.input)}</span> }),
    col.display({ id: "out", header: t("pricing.output"), cell: (c) => <span className="num">{perM(prices(c.row.original)?.output)}</span> }),
    col.display({ id: "cr", header: t("pricing.cacheRead"), cell: (c) => <span className="num">{perM(prices(c.row.original)?.cache_read)}</span> }),
    col.display({ id: "cw", header: t("pricing.cacheWrite"), cell: (c) => <span className="num">{perM(prices(c.row.original)?.cache_write)}</span> }),
    col.accessor("model_multiplier", { header: t("pricing.modelMult"), cell: (c) => <span className="num text-[12px]">×{c.getValue()}</span> }),
  ];

  return (
    <>
      <PageHeader code={code} title={t("pricing.title")} />
      <div className="flex flex-col gap-6">
        <div className="grid gap-4 border-2 border-ink p-4 md:grid-cols-[1fr_auto] md:items-end">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="pricing-search">{t("pricing.search")}</Label>
            <div className="relative">
              <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted" aria-hidden />
              <Input id="pricing-search" type="search" className="pl-9" placeholder="claude-…" value={search} onChange={(e) => setSearch(e.target.value)} />
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <span id="pricing-tier" className="text-[13px] font-semibold">
              {t("pricing.tier")}
            </span>
            <div role="group" aria-labelledby="pricing-tier" className="flex border-2 border-ink">
              {TIER_LIST.map((x) => (
                <button
                  key={x}
                  type="button"
                  aria-pressed={tier === x}
                  onClick={() => setTier(x)}
                  className={cn("h-9 flex-1 border-l border-ink px-3 text-[13px] font-semibold first:border-l-0", tier === x ? "bg-yellow text-on-yellow" : "hover:bg-rule")}
                >
                  {t(`tier.${x}`)}
                  {mult ? <span className="num ml-1 text-[11px]">×{mult[x]}</span> : null}
                </button>
              ))}
            </div>
          </div>
        </div>
        <Section n={1} title={t("pricing.list")} aside={<span className="text-[12px] text-muted">{t("pricing.unit")}</span>} bodyClassName="p-0 sm:p-4">
          <DataTable
            label={t("pricing.list")}
            columns={columns}
            data={rows}
            rowKey={(r) => r.model}
            loading={q.isPending}
            error={q.error}
            onRetry={() => q.refetch()}
            empty={<EmptyState className="m-4 sm:m-0" title={t("pricing.empty")} description={t("pricing.emptyHint")} />}
            className="max-sm:p-4"
            renderCard={(r) => (
              <RowCard
                title={<span className="num">{r.model}</span>}
                aside={!r.priced ? <span className="label-mono border border-dashed border-ink px-1">{t("pricing.unpriced")}</span> : <span className="num text-[12px]">×{r.model_multiplier}</span>}
                lines={[
                  [t("pricing.input"), <span className="num">{perM(prices(r)?.input)}</span>],
                  [t("pricing.output"), <span className="num">{perM(prices(r)?.output)}</span>],
                  [t("pricing.cacheRead"), <span className="num">{perM(prices(r)?.cache_read)}</span>],
                  [t("pricing.cacheWrite"), <span className="num">{perM(prices(r)?.cache_write)}</span>],
                ]}
              />
            )}
          />
          <p className="px-4 pt-3 pb-4 text-[12px] text-muted sm:px-0 sm:pb-0">{t("pricing.formula")}</p>
        </Section>
      </div>
    </>
  );
}
