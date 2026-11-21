import { useEffect, useRef, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ChevronRight } from "lucide-react";
import { createColumnHelper, flexRender, tableFeatures, useTable, type ColumnDef, type RowData } from "@tanstack/react-table";
import { useVirtualizer } from "@tanstack/react-virtual";
import { cn } from "@/lib/utils";
import { useMediaQuery } from "@/lib/hooks";
import { Button } from "./ui/button";
import { Skeleton } from "./ui/skeleton";
import { EmptyState, ErrorBox } from "./Feedback";

export const features = tableFeatures({});
export type Features = typeof features;
export type Column<T extends RowData> = ColumnDef<Features, T, any>; // eslint-disable-line @typescript-eslint/no-explicit-any
export function columnHelper<T extends RowData>() {
  return createColumnHelper<Features, T>();
}

interface Props<T extends RowData> {
  columns: Column<T>[];
  data: T[];
  rowKey: (row: T) => string;
  label: string;
  loading?: boolean;
  error?: unknown;
  onRetry?: () => void;
  hasMore?: boolean;
  fetchingMore?: boolean;
  onLoadMore?: () => void;
  onRowClick?: (row: T) => void;
  /** Mobile (< 640px) renders cards instead of the table when given. */
  renderCard?: (row: T) => ReactNode;
  empty?: ReactNode;
  /** Height of the virtual scroll area (CSS length). */
  height?: string;
  /** Rows above which virtualization is used. */
  virtualizeFrom?: number;
  className?: string;
}

const ROW_HEIGHT = 44;

/**
 * Table with mono headers (2px rule above, 1px below) and thin row rules.
 * Long lists are virtualized and load the next cursor page near the end.
 */
export function DataTable<T extends RowData>({
  columns,
  data,
  rowKey,
  label,
  loading,
  error,
  onRetry,
  hasMore,
  fetchingMore,
  onLoadMore,
  onRowClick,
  renderCard,
  empty,
  height = "min(70dvh, 640px)",
  virtualizeFrom = 60,
  className,
}: Props<T>) {
  const { t } = useTranslation();
  const mobile = useMediaQuery("(max-width: 639px)");
  const table = useTable({ features, columns, data, getRowId: (row: T) => rowKey(row) });
  const rows = table.getRowModel().rows;
  const scrollRef = useRef<HTMLDivElement>(null);
  const virtual = rows.length >= virtualizeFrom;
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 12,
    enabled: virtual && !(mobile && renderCard),
  });
  const items = virtualizer.getVirtualItems();
  const lastIndex = items.length ? items[items.length - 1]!.index : -1;

  useEffect(() => {
    if (virtual && hasMore && !fetchingMore && onLoadMore && lastIndex >= rows.length - 10) onLoadMore();
  }, [virtual, lastIndex, rows.length, hasMore, fetchingMore, onLoadMore]);

  if (error) return <ErrorBox error={error} onRetry={onRetry} />;
  if (loading) {
    return (
      <div className="flex flex-col gap-2" aria-busy="true" aria-label={t("common.loading")}>
        {Array.from({ length: 6 }, (_, i) => (
          <Skeleton key={i} className="h-9" />
        ))}
      </div>
    );
  }
  if (data.length === 0) return <>{empty ?? <EmptyState title={t("common.empty")} />}</>;

  const more = hasMore ? (
    <div className="flex justify-center border-t border-rule p-3">
      <Button size="sm" onClick={onLoadMore} disabled={fetchingMore}>
        {fetchingMore ? t("common.loading") : t("common.loadMore")}
      </Button>
    </div>
  ) : null;

  if (mobile && renderCard) {
    return (
      <div className={className}>
        <ul aria-label={label} className="flex flex-col gap-2">
          {data.map((row) => (
            <li key={rowKey(row)}>{renderCard(row)}</li>
          ))}
        </ul>
        {more}
      </div>
    );
  }

  const colCount = columns.length + (onRowClick ? 1 : 0);
  const visible = virtual ? items.map((v) => ({ row: rows[v.index]!, key: v.key })) : rows.map((row) => ({ row, key: row.id }));
  const padTop = virtual && items.length ? items[0]!.start : 0;
  const padBottom = virtual && items.length ? virtualizer.getTotalSize() - items[items.length - 1]!.end : 0;

  return (
    <div className={className}>
      <div ref={scrollRef} className="scroll-x" style={virtual ? { maxHeight: height, overflowY: "auto" } : undefined}>
        <table className="w-full min-w-[640px] border-collapse text-left text-[13px]" aria-label={label}>
          <thead className="sticky top-0 z-[1] bg-bg">
            {table.getHeaderGroups().map((hg) => (
              <tr key={hg.id} className="border-t-2 border-b border-ink">
                {hg.headers.map((h) => (
                  <th key={h.id} scope="col" className="label-mono px-3 py-2.5 whitespace-nowrap">
                    {h.isPlaceholder ? null : flexRender(h.column.columnDef.header, h.getContext())}
                  </th>
                ))}
                {onRowClick ? (
                  <th scope="col" className="w-10 px-2">
                    <span className="sr-only">{t("common.details")}</span>
                  </th>
                ) : null}
              </tr>
            ))}
          </thead>
          <tbody>
            {padTop > 0 ? (
              <tr aria-hidden>
                <td colSpan={colCount} style={{ height: padTop }} />
              </tr>
            ) : null}
            {visible.map(({ row, key }) => (
              <tr
                key={key}
                className={cn("border-b border-rule", onRowClick && "cursor-pointer hover:bg-rule")}
                style={{ height: ROW_HEIGHT }}
                onClick={onRowClick ? () => onRowClick(row.original) : undefined}
              >
                {row.getAllCells().map((cell) => (
                  <td key={cell.id} className="px-3 py-2 align-middle whitespace-nowrap">
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </td>
                ))}
                {onRowClick ? (
                  <td className="px-2 text-right">
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label={t("common.details")}
                      onClick={(e) => {
                        e.stopPropagation();
                        onRowClick(row.original);
                      }}
                    >
                      <ChevronRight aria-hidden />
                    </Button>
                  </td>
                ) : null}
              </tr>
            ))}
            {padBottom > 0 ? (
              <tr aria-hidden>
                <td colSpan={colCount} style={{ height: padBottom }} />
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
      {virtual && hasMore ? (fetchingMore ? <p className="p-3 text-center text-[12px] text-muted">{t("common.loading")}</p> : null) : more}
    </div>
  );
}

/** Mobile card for a table row: a 1px frame with label/value lines. */
export function RowCard({ title, aside, lines, onClick }: { title: ReactNode; aside?: ReactNode; lines: [string, ReactNode][]; onClick?: () => void }) {
  const body = (
    <>
      <div className="flex items-center justify-between gap-2 border-b border-rule pb-2">
        <div className="min-w-0 truncate font-semibold">{title}</div>
        {aside}
      </div>
      <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[13px]">
        {lines.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="label-mono self-center text-muted">{k}</dt>
            <dd className="min-w-0 text-right">{v}</dd>
          </div>
        ))}
      </dl>
    </>
  );
  if (!onClick) return <div className="border border-ink p-3">{body}</div>;
  return (
    <button type="button" onClick={onClick} className="block w-full border border-ink p-3 text-left">
      {body}
    </button>
  );
}
