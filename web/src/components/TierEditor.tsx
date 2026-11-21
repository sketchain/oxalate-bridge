import { useTranslation } from "react-i18next";
import { ArrowDown, ArrowUp, GripVertical } from "lucide-react";
import { DndContext, KeyboardSensor, PointerSensor, closestCenter, useSensor, useSensors, type DragEndEvent } from "@dnd-kit/core";
import { SortableContext, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { moveTier, reorder, toggleTier, type TierRow } from "@/lib/tiers";
import { cn } from "@/lib/utils";
import { Switch } from "./ui/switch";
import { Button } from "./ui/button";
import { TierTag } from "./Tags";

function Row({ row, index, count, onChange, rows }: { row: TierRow; index: number; count: number; rows: TierRow[]; onChange: (rows: TierRow[]) => void }) {
  const { t } = useTranslation();
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: row.tier });
  const name = t(`tier.${row.tier}`);
  return (
    <li
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={cn("flex items-center gap-2 border-b border-rule bg-bg px-2 py-2 last:border-b-0", isDragging && "relative z-10 border-2 border-ink")}
    >
      <button
        type="button"
        className="inline-flex size-8 cursor-grab items-center justify-center border-2 border-transparent hover:border-ink active:cursor-grabbing"
        aria-label={t("keys.dragHandle", { tier: name })}
        {...attributes}
        {...listeners}
      >
        <GripVertical className="size-4" aria-hidden />
      </button>
      <span className="num w-5 text-[11px] text-muted">{row.enabled ? String(rows.filter((r) => r.enabled).indexOf(row) + 1).padStart(2, "0") : "--"}</span>
      <span className={cn("flex-1", !row.enabled && "opacity-50")}>
        <TierTag tier={row.tier} />
        <span className="ml-2 text-[12px] text-muted">{t(`keys.tierHint.${row.tier}`)}</span>
      </span>
      <Button variant="ghost" size="icon-sm" aria-label={t("keys.moveUp", { tier: name })} disabled={index === 0} onClick={() => onChange(moveTier(rows, row.tier, -1))}>
        <ArrowUp aria-hidden />
      </Button>
      <Button variant="ghost" size="icon-sm" aria-label={t("keys.moveDown", { tier: name })} disabled={index === count - 1} onClick={() => onChange(moveTier(rows, row.tier, 1))}>
        <ArrowDown aria-hidden />
      </Button>
      <Switch checked={row.enabled} onCheckedChange={() => onChange(toggleTier(rows, row.tier))} aria-label={t("keys.enableTier", { tier: name })} />
    </li>
  );
}

/** Ordered tier switches: drag to reorder (pointer or keyboard) or use the arrow buttons. */
export function TierEditor({ rows, onChange, labelledBy }: { rows: TierRow[]; onChange: (rows: TierRow[]) => void; labelledBy?: string }) {
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 4 } }), useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }));
  const onDragEnd = (e: DragEndEvent) => {
    if (!e.over || e.active.id === e.over.id) return;
    const from = rows.findIndex((r) => r.tier === e.active.id);
    const to = rows.findIndex((r) => r.tier === e.over!.id);
    onChange(reorder(rows, from, to));
  };
  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
      <SortableContext items={rows.map((r) => r.tier)} strategy={verticalListSortingStrategy}>
        <ul className="border border-ink" aria-labelledby={labelledBy}>
          {rows.map((row, i) => (
            <Row key={row.tier} row={row} index={i} count={rows.length} rows={rows} onChange={onChange} />
          ))}
        </ul>
      </SortableContext>
    </DndContext>
  );
}
