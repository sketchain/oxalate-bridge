import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { adminAnnouncementsQuery, keys } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { DataTable, RowCard, columnHelper } from "@/components/DataTable";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { EmptyState } from "@/components/Feedback";
import { AnnouncementText } from "@/components/AnnouncementText";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog";
import { Input, NativeSelect, Textarea } from "@/components/ui/input";
import { Field, Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useApiMutation } from "@/lib/mutations";
import { formatDateTime } from "@/lib/format";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/admin/announcements")({ component: AnnouncementsAdminPage });

type Ann = Schemas["Announcement"];

function toLocal(iso?: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

function AnnouncementsAdminPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery(adminAnnouncementsQuery);
  const [editing, setEditing] = useState<Ann | "new" | null>(null);
  const [deleting, setDeleting] = useState<Ann | null>(null);
  const remove = useApiMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/admin/announcements/{announcementId}", { params: { path: { announcementId: id } } })),
    success: t("announcementAdmin.deleted"),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.adminAnnouncements });
      void qc.invalidateQueries({ queryKey: keys.announcements });
    },
  });
  const span = (a: Ann) => `${a.starts_at ? formatDateTime(a.starts_at) : t("announcementAdmin.now")} → ${a.ends_at ? formatDateTime(a.ends_at) : t("announcementAdmin.forever")}`;

  const col = columnHelper<Ann>();
  const columns = [
    col.accessor("title", { header: t("announcementAdmin.titleField"), cell: (c) => <span className="font-semibold">{c.getValue()}</span> }),
    col.accessor("level", { header: t("announcementAdmin.level"), cell: (c) => <LevelTag level={c.getValue()} /> }),
    col.display({ id: "window", header: t("announcementAdmin.window"), cell: (c) => <span className="num text-[12px]">{span(c.row.original)}</span> }),
    col.display({ id: "state", header: t("announcementAdmin.state"), cell: (c) => <StateTag a={c.row.original} /> }),
    col.display({
      id: "actions",
      header: () => <span className="sr-only">{t("common.actions")}</span>,
      cell: (c) => (
        <div className="flex justify-end gap-1">
          <Button variant="ghost" size="icon-sm" aria-label={t("announcementAdmin.edit", { title: c.row.original.title })} onClick={() => setEditing(c.row.original)}>
            <Pencil aria-hidden />
          </Button>
          <Button variant="ghost" size="icon-sm" aria-label={t("announcementAdmin.delete", { title: c.row.original.title })} onClick={() => setDeleting(c.row.original)}>
            <Trash2 aria-hidden />
          </Button>
        </div>
      ),
    }),
  ];

  return (
    <>
      <PageHeader
        code="OB-19"
        title={t("announcementAdmin.title")}
        actions={
          <Button variant="primary" onClick={() => setEditing("new")}>
            <Plus aria-hidden />
            {t("announcementAdmin.new")}
          </Button>
        }
      />
      <Section n={1} title={t("announcementAdmin.list")} bodyClassName="p-0 sm:p-4">
        <DataTable
          label={t("announcementAdmin.list")}
          columns={columns}
          data={q.data?.items ?? []}
          rowKey={(a) => a.id}
          loading={q.isPending}
          error={q.error}
          onRetry={() => q.refetch()}
          empty={<EmptyState className="m-4 sm:m-0" title={t("announcementAdmin.empty")} description={t("announcementAdmin.emptyHint")} />}
          className="max-sm:p-4"
          renderCard={(a) => (
            <RowCard
              title={a.title}
              aside={<StateTag a={a} />}
              lines={[
                [t("announcementAdmin.level"), <LevelTag level={a.level} />],
                [t("announcementAdmin.window"), <span className="num">{span(a)}</span>],
              ]}
              onClick={() => setEditing(a)}
            />
          )}
        />
      </Section>
      {editing ? <AnnouncementDialog initial={editing === "new" ? null : editing} onClose={() => setEditing(null)} /> : null}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t("announcementAdmin.deleteTitle")}
        description={t("announcementAdmin.deleteBody", { title: deleting?.title })}
        confirmLabel={t("common.delete")}
        onConfirm={() => remove.mutateAsync(deleting!.id)}
      />
    </>
  );
}

function LevelTag({ level }: { level: Schemas["AnnouncementLevel"] }) {
  const { t } = useTranslation();
  return (
    <span className={cn("label-mono inline-block border border-ink px-1.5 py-0.5", level === "warning" && "bg-yellow text-on-yellow")}>{t(`announcement.level.${level}`)}</span>
  );
}

function StateTag({ a }: { a: Ann }) {
  const { t } = useTranslation();
  const state = a.active ? "active" : a.enabled ? "scheduled" : "off";
  return (
    <span className={cn("label-mono inline-block px-1.5 py-0.5", state === "active" ? "bg-ink text-bg" : "border border-dashed border-ink text-muted")}>
      {t(`announcementAdmin.states.${state}`)}
    </span>
  );
}

function AnnouncementDialog({ initial, onClose }: { initial: Ann | null; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [title, setTitle] = useState(initial?.title ?? "");
  const [body, setBody] = useState(initial?.body ?? "");
  const [level, setLevel] = useState<Schemas["AnnouncementLevel"]>(initial?.level ?? "info");
  const [starts, setStarts] = useState(toLocal(initial?.starts_at));
  const [ends, setEnds] = useState(toLocal(initial?.ends_at));
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const save = useApiMutation({
    mutationFn: (input: Schemas["AnnouncementInput"]) =>
      initial
        ? unwrap(api.PATCH("/admin/announcements/{announcementId}", { params: { path: { announcementId: initial.id } }, body: input }))
        : unwrap(api.POST("/admin/announcements", { body: input })),
    success: t("announcementAdmin.saved"),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.adminAnnouncements });
      void qc.invalidateQueries({ queryKey: keys.announcements });
      onClose();
    },
  });
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!title.trim()) errs.title = t("validation.required");
    if (title.length > 200) errs.title = t("validation.max", { n: 200 });
    if (body.length > 5000) errs.body = t("validation.max", { n: 5000 });
    const s = starts ? new Date(starts) : null;
    const en = ends ? new Date(ends) : null;
    if (s && en && en <= s) errs.ends = t("announcementAdmin.endsAfterStarts");
    setErrors(errs);
    if (Object.keys(errs).length) return;
    save.mutate({ title: title.trim(), body, level, enabled, starts_at: s ? s.toISOString() : null, ends_at: en ? en.toISOString() : null });
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={initial ? t("announcementAdmin.editTitle") : t("announcementAdmin.new")} wide>
        <form onSubmit={submit} noValidate className="flex flex-col gap-4">
          <Field label={t("announcementAdmin.titleField")} htmlFor="an-title" error={errors.title}>
            <Input id="an-title" value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} aria-invalid={!!errors.title} />
          </Field>
          <Field label={t("announcementAdmin.body")} htmlFor="an-body" hint={t("announcementAdmin.bodyHint")} error={errors.body}>
            <Textarea id="an-body" rows={4} value={body} onChange={(e) => setBody(e.target.value)} aria-invalid={!!errors.body} />
          </Field>
          {body ? (
            <div className="border border-dashed border-ink p-3">
              <div className="label-mono mb-1 text-muted">{t("announcementAdmin.preview")}</div>
              <AnnouncementText text={body} className="text-[13px]" />
            </div>
          ) : null}
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label={t("announcementAdmin.level")} htmlFor="an-level">
              <NativeSelect id="an-level" value={level} onChange={(e) => setLevel(e.target.value as Schemas["AnnouncementLevel"])}>
                <option value="info">{t("announcement.level.info")}</option>
                <option value="warning">{t("announcement.level.warning")}</option>
              </NativeSelect>
            </Field>
            <Field label={t("announcementAdmin.starts")} htmlFor="an-starts" hint={t("announcementAdmin.startsHint")}>
              <Input id="an-starts" type="datetime-local" className="num" value={starts} onChange={(e) => setStarts(e.target.value)} />
            </Field>
            <Field label={t("announcementAdmin.ends")} htmlFor="an-ends" hint={t("announcementAdmin.endsHint")} error={errors.ends}>
              <Input id="an-ends" type="datetime-local" className="num" value={ends} onChange={(e) => setEnds(e.target.value)} />
            </Field>
          </div>
          <div className="flex items-center justify-between border border-ink px-3 py-2.5">
            <Label htmlFor="an-enabled">{t("announcementAdmin.enabled")}</Label>
            <Switch id="an-enabled" checked={enabled} onCheckedChange={setEnabled} />
          </div>
          <DialogFooter className="mt-0">
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {t("common.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
