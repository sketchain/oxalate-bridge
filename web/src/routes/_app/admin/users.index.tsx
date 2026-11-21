import { useMemo, useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { keepPreviousData, useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Plus, Search } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { usersInfinite } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { DataTable, RowCard, columnHelper } from "@/components/DataTable";
import { EmptyState } from "@/components/Feedback";
import { UserStatusTag } from "@/components/UserTags";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog";
import { Input, NativeSelect, Textarea } from "@/components/ui/input";
import { Field, Label } from "@/components/ui/label";
import { useApiMutation } from "@/lib/mutations";
import { useDebounced } from "@/lib/hooks";
import { formatDateTime, formatUSD } from "@/lib/format";

export const Route = createFileRoute("/_app/admin/users/")({
  component: UsersPage,
});

type User = Schemas["AdminUser"];

function UsersPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [search, setSearch] = useState("");
  const debounced = useDebounced(search.trim(), 300);
  const q = useInfiniteQuery({ ...usersInfinite(debounced), placeholderData: keepPreviousData });
  const rows = useMemo(() => q.data?.pages.flatMap((p) => p.items) ?? [], [q.data]);
  const [creating, setCreating] = useState(false);
  const open = (u: User) => navigate({ to: "/admin/users/$userId", params: { userId: u.id } });

  const col = columnHelper<User>();
  const columns = [
    col.accessor("username", { header: t("users.username"), cell: (c) => <span className="font-semibold">{c.getValue()}</span> }),
    col.display({ id: "role", header: t("users.roleStatus"), cell: (c) => <UserStatusTag u={c.row.original} /> }),
    col.accessor("balance_micro", { header: t("users.balance"), cell: (c) => <span className="num">{formatUSD(c.getValue())}</span> }),
    col.display({ id: "limits", header: t("users.limits"), cell: (c) => <span className="num text-[12px]">{c.row.original.max_concurrency} / {c.row.original.rpm ?? "∞"}</span> }),
    col.accessor("note", { header: t("users.note"), cell: (c) => <span className="block max-w-[240px] truncate text-[12px] text-muted">{c.getValue() || "—"}</span> }),
    col.accessor("created_at", { header: t("users.createdAt"), cell: (c) => <span className="num text-[12px]">{formatDateTime(c.getValue())}</span> }),
  ];

  return (
    <>
      <PageHeader
        code="OB-08"
        title={t("users.title")}
        actions={
          <Button variant="primary" onClick={() => setCreating(true)}>
            <Plus aria-hidden />
            {t("users.new")}
          </Button>
        }
      />
      <Section
        n={1}
        title={t("users.list")}
        aside={
          <div className="relative">
            <Label htmlFor="user-search" className="sr-only">
              {t("users.search")}
            </Label>
            <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted" aria-hidden />
            <Input id="user-search" type="search" className="h-8 w-56 pl-8" placeholder={t("users.search")} value={search} onChange={(e) => setSearch(e.target.value)} />
          </div>
        }
        bodyClassName="p-0 sm:p-4"
      >
        <DataTable
          label={t("users.list")}
          columns={columns}
          data={rows}
          rowKey={(u) => u.id}
          loading={q.isPending}
          error={q.error}
          onRetry={() => q.refetch()}
          hasMore={q.hasNextPage}
          fetchingMore={q.isFetchingNextPage}
          onLoadMore={() => void q.fetchNextPage()}
          onRowClick={open}
          className="max-sm:p-4"
          empty={<EmptyState className="m-4 sm:m-0" title={debounced ? t("users.noMatch") : t("users.empty")} description={t("users.emptyHint")} />}
          renderCard={(u) => (
            <RowCard
              onClick={() => open(u)}
              title={u.username}
              aside={<UserStatusTag u={u} />}
              lines={[
                [t("users.balance"), <span className="num">{formatUSD(u.balance_micro)}</span>],
                [t("users.createdAt"), <span className="num">{formatDateTime(u.created_at)}</span>],
              ]}
            />
          )}
        />
      </Section>
      {creating ? <CreateUserDialog onClose={() => setCreating(false)} onCreated={open} /> : null}
    </>
  );
}

function CreateUserDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (u: User) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const optInt = z
    .string()
    .trim()
    .refine((v) => v === "" || /^\d+$/.test(v), t("validation.nonNegativeInt"));
  const schema = z.object({
    username: z
      .string()
      .trim()
      .min(1, t("validation.required"))
      .max(64)
      .regex(/^[A-Za-z0-9_.@-]+$/, t("validation.username")),
    password: z.string().min(8, t("validation.minPassword")).max(256),
    role: z.enum(["user", "admin"]),
    max_concurrency: optInt,
    rpm: optInt,
    note: z.string().max(500),
  });
  type Values = z.infer<typeof schema>;
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { username: "", password: "", role: "user", max_concurrency: "", rpm: "", note: "" } });
  const errs = form.formState.errors;
  const create = useApiMutation({
    mutationFn: (v: Values) =>
      unwrap(
        api.POST("/admin/users", {
          body: {
            username: v.username,
            password: v.password,
            role: v.role,
            max_concurrency: v.max_concurrency === "" ? undefined : Number(v.max_concurrency),
            rpm: v.rpm === "" ? null : Number(v.rpm),
            note: v.note,
          },
        }),
      ),
    success: t("users.created"),
    onSuccess: (u) => {
      void qc.invalidateQueries({ queryKey: ["admin", "users"] });
      onClose();
      onCreated(u);
    },
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={t("users.new")}>
        <form onSubmit={form.handleSubmit((v) => create.mutate(v))} noValidate className="flex flex-col gap-4">
          <Field label={t("users.username")} htmlFor="nu-name" error={errs.username?.message}>
            <Input id="nu-name" autoComplete="off" aria-invalid={!!errs.username} {...form.register("username")} />
          </Field>
          <Field label={t("users.password")} htmlFor="nu-pw" hint={t("validation.minPassword")} error={errs.password?.message}>
            <Input id="nu-pw" type="password" autoComplete="new-password" aria-invalid={!!errs.password} {...form.register("password")} />
          </Field>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label={t("users.role")} htmlFor="nu-role">
              <NativeSelect id="nu-role" {...form.register("role")}>
                <option value="user">{t("role.user")}</option>
                <option value="admin">{t("role.admin")}</option>
              </NativeSelect>
            </Field>
            <Field label={t("users.concurrency")} htmlFor="nu-conc" hint={t("users.defaultHint")} error={errs.max_concurrency?.message}>
              <Input id="nu-conc" inputMode="numeric" className="num" {...form.register("max_concurrency")} />
            </Field>
            <Field label={t("users.rpm")} htmlFor="nu-rpm" hint={t("keys.emptyUnlimited")} error={errs.rpm?.message}>
              <Input id="nu-rpm" inputMode="numeric" className="num" {...form.register("rpm")} />
            </Field>
          </div>
          <Field label={t("users.note")} htmlFor="nu-note">
            <Textarea id="nu-note" rows={2} {...form.register("note")} />
          </Field>
          <DialogFooter className="mt-0">
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {t("users.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
