import { useMemo, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { keepPreviousData, useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Dialog as D } from "radix-ui";
import { Check, Download, FileJson, Undo2, X } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { keys, safeguardAppealsInfinite, safeguardBlockQuery, safeguardBlocksInfinite, safeguardEventQuery, safeguardInfinite } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { DataTable, RowCard, columnHelper } from "@/components/DataTable";
import { EmptyState, ErrorBox } from "@/components/Feedback";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { AppealStatusTag } from "@/components/Safeguard";
import { CountBadge } from "@/components/Tags";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, SheetContent } from "@/components/ui/dialog";
import { Input, NativeSelect, Textarea } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useApiMutation } from "@/lib/mutations";
import { formatDateTime, formatInt } from "@/lib/format";
import { useDebounced } from "@/lib/hooks";
import { shortId } from "@/lib/utils";

type Tab = "events" | "blocks" | "appeals";
type BlockStatus = "all" | "active" | "revoked";
type AppealFilter = "all" | Schemas["SafeguardAppealStatus"];

interface Search {
  tab?: Tab;
  user?: string;
  hash?: string;
  status?: AppealFilter;
}

const APPEAL_FILTERS: AppealFilter[] = ["all", "pending", "approved", "rejected"];

export const Route = createFileRoute("/_app/admin/safeguard")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    tab: s.tab === "blocks" || s.tab === "appeals" ? s.tab : undefined,
    user: typeof s.user === "string" && /^[0-9a-f-]{36}$/i.test(s.user) ? s.user : undefined,
    hash: typeof s.hash === "string" && /^[0-9a-f]{64}$/.test(s.hash) ? s.hash : undefined,
    status: APPEAL_FILTERS.includes(s.status as AppealFilter) ? (s.status as AppealFilter) : undefined,
  }),
  component: Safeguard,
});

type Ev = Schemas["SafeguardEvent"];
type Block = Schemas["SafeguardBlock"];
type Appeal = Schemas["SafeguardAppeal"];

function CategoryPlate({ c }: { c: string }) {
  return <span className="label-mono bg-red px-1.5 py-0.5 text-on-red">{c || "—"}</span>;
}

function Hash({ h }: { h: string }) {
  return h ? (
    <span className="num text-[12px]" title={h}>
      {h.slice(0, 12)}…
    </span>
  ) : (
    <span className="text-muted">—</span>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${(n / 1024 / 1024).toFixed(2)} MiB`;
}

/** Safeguard administration: triggering requests, the prompt blocklist and appeals. */
function Safeguard() {
  const { t } = useTranslation();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: "/admin/safeguard" });
  const set = (patch: Partial<Search>) => navigate({ search: (s) => ({ ...s, ...patch }), replace: true });
  const tab = search.tab ?? "events";
  const pending = useQuery({
    queryKey: keys.safeguardAppeals({ status: "pending", limit: 1 }),
    queryFn: () => unwrap(api.GET("/admin/safeguard-appeals", { params: { query: { status: "pending", limit: 1 } } })),
  });
  return (
    <>
      <PageHeader code="OB-15" title={t("safeguard.title")} />
      <p className="mb-6 max-w-3xl text-[13px] text-muted">{t("safeguard.intro")}</p>
      <Tabs value={tab} onValueChange={(v) => set({ tab: v === "events" ? undefined : (v as Tab) })}>
        <TabsList>
          <TabsTrigger value="events">{t("safeguard.tabEvents")}</TabsTrigger>
          <TabsTrigger value="blocks">{t("safeguard.tabBlocks")}</TabsTrigger>
          <TabsTrigger value="appeals">
            {t("safeguard.tabAppeals")}
            <CountBadge n={pending.data?.pending ?? 0} />
          </TabsTrigger>
        </TabsList>
        <TabsContent value="events" className="pt-6">
          <Events user={search.user} hash={search.hash} onFilter={set} />
        </TabsContent>
        <TabsContent value="blocks" className="pt-6">
          <Blocks onEvents={(hash) => set({ tab: undefined, hash, user: undefined })} />
        </TabsContent>
        <TabsContent value="appeals" className="pt-6">
          <Appeals status={search.status ?? "pending"} onStatus={(status) => set({ status })} />
        </TabsContent>
      </Tabs>
    </>
  );
}

// ---- events ------------------------------------------------------------------------

function Events({ user, hash, onFilter }: { user?: string; hash?: string; onFilter: (p: Partial<Search>) => void }) {
  const { t } = useTranslation();
  const [userInput, setUserInput] = useState(user ?? "");
  const debounced = useDebounced(userInput.trim(), 400);
  const uid = /^[0-9a-f-]{36}$/i.test(debounced) ? debounced : user;
  const q = useInfiniteQuery({ ...safeguardInfinite({ user_id: uid, prompt_hash: hash, limit: 100 }), placeholderData: keepPreviousData });
  const rows = useMemo(() => q.data?.pages.flatMap((p) => p.items) ?? [], [q.data]);
  const [open, setOpen] = useState<string | null>(null);
  const col = columnHelper<Ev>();
  const columns = [
    col.accessor("created_at", { header: t("billing.time"), cell: (c) => <span className="num text-[12px]">{formatDateTime(c.getValue())}</span> }),
    col.display({
      id: "user",
      header: t("adminOverview.user"),
      cell: (c) =>
        c.row.original.user_id ? (
          <Link to="/admin/users/$userId" params={{ userId: c.row.original.user_id }} className="font-semibold underline underline-offset-4">
            {c.row.original.username || shortId(c.row.original.user_id)}
          </Link>
        ) : (
          "—"
        ),
    }),
    col.display({ id: "key", header: t("safeguard.key"), cell: (c) => <span className="num text-[12px]">{c.row.original.key_prefix || "—"}</span> }),
    col.accessor("model", { header: t("usage.model"), cell: (c) => <span className="font-semibold">{c.getValue()}</span> }),
    col.accessor("category", { header: t("safeguard.category"), cell: (c) => <CategoryPlate c={c.getValue()} /> }),
    col.accessor("prompt", { header: t("safeguard.prompt"), cell: (c) => <span className="block max-w-[360px] truncate text-[12px]" title={c.getValue()}>{c.getValue() || "—"}</span> }),
    col.accessor("counted", { header: t("safeguard.counted"), cell: (c) => <span className="label-mono text-[11px]">{c.getValue() ? t("safeguard.countedYes") : t("safeguard.countedNo")}</span> }),
    col.display({
      id: "open",
      header: () => <span className="sr-only">{t("common.actions")}</span>,
      cell: (c) => (
        <Button size="sm" onClick={() => setOpen(c.row.original.id)}>
          <FileJson aria-hidden />
          {t("safeguard.view")}
        </Button>
      ),
    }),
  ];
  return (
    <div className="flex flex-col gap-6">
      <div className="grid gap-3 border-2 border-ink p-4 sm:grid-cols-2">
        <Field label={t("logs.userId")} htmlFor="sg-user">
          <Input
            id="sg-user"
            className="num"
            placeholder="uuid"
            value={userInput}
            onChange={(e) => {
              setUserInput(e.target.value);
              if (!e.target.value.trim()) onFilter({ user: undefined });
            }}
          />
        </Field>
        <Field label={t("safeguard.hashFilter")} htmlFor="sg-hash">
          {hash ? (
            <Button id="sg-hash" className="justify-between" onClick={() => onFilter({ hash: undefined })}>
              <span className="num">{hash.slice(0, 16)}…</span>
              <span className="text-[12px]">{t("common.clear")}</span>
            </Button>
          ) : (
            <p id="sg-hash" className="flex h-10 items-center text-[12px] text-muted">
              {t("safeguard.hashFilterHint")}
            </p>
          )}
        </Field>
      </div>
      <Section n={1} title={t("safeguard.list")} bodyClassName="p-0 sm:p-4">
        <DataTable
          label={t("safeguard.list")}
          columns={columns}
          data={rows}
          rowKey={(r) => r.id}
          loading={q.isPending}
          error={q.error}
          onRetry={() => q.refetch()}
          hasMore={q.hasNextPage}
          fetchingMore={q.isFetchingNextPage}
          onLoadMore={() => void q.fetchNextPage()}
          className="max-sm:p-4"
          empty={<EmptyState className="m-4 sm:m-0" title={t("safeguard.empty")} />}
          renderCard={(r) => (
            <RowCard
              title={r.model}
              aside={<CategoryPlate c={r.category} />}
              onClick={() => setOpen(r.id)}
              lines={[
                [t("billing.time"), <span className="num">{formatDateTime(r.created_at)}</span>],
                [t("adminOverview.user"), r.username || "—"],
                [t("safeguard.prompt"), <span className="line-clamp-2">{r.prompt || "—"}</span>],
              ]}
            />
          )}
        />
      </Section>
      <D.Root open={!!open} onOpenChange={(o) => !o && setOpen(null)}>
        {open ? <EventDrawer id={open} onHash={(h) => (setOpen(null), onFilter({ hash: h }))} /> : null}
      </D.Root>
    </div>
  );
}

function EventDrawer({ id, onHash }: { id: string; onHash: (hash: string) => void }) {
  const { t } = useTranslation();
  const q = useQuery(safeguardEventQuery(id));
  const e = q.data;
  return (
    <SheetContent title={t("safeguard.eventTitle")} className="sm:w-[640px]">
      {q.isPending ? (
        <Skeleton className="m-5 h-64" />
      ) : q.error ? (
        <ErrorBox className="m-5" error={q.error} onRetry={() => q.refetch()} />
      ) : e ? (
        <div className="flex flex-col gap-5 p-5" data-testid="sg-event">
          <dl className="grid grid-cols-[110px_1fr] gap-x-3 gap-y-1.5 text-[13px]">
            <dt className="label-mono text-muted">{t("billing.time")}</dt>
            <dd className="num">{formatDateTime(e.created_at)}</dd>
            <dt className="label-mono text-muted">{t("adminOverview.user")}</dt>
            <dd>{e.username || "—"}</dd>
            <dt className="label-mono text-muted">{t("safeguard.key")}</dt>
            <dd>
              {e.key_name || "—"} <span className="num text-[12px] text-muted">{e.key_prefix}</span>
            </dd>
            <dt className="label-mono text-muted">{t("safeguard.account")}</dt>
            <dd>{e.account_name || "—"}</dd>
            <dt className="label-mono text-muted">{t("usage.model")}</dt>
            <dd className="font-semibold">{e.model}</dd>
            <dt className="label-mono text-muted">{t("safeguard.category")}</dt>
            <dd>
              <CategoryPlate c={e.category} />
            </dd>
            <dt className="label-mono text-muted">{t("safeguard.reason")}</dt>
            <dd className="break-words">{e.reason || "—"}</dd>
            <dt className="label-mono text-muted">{t("safeguard.upstream")}</dt>
            <dd className="num">
              {e.status_code} · {e.stream ? "stream" : "json"}
            </dd>
            <dt className="label-mono text-muted">{t("safeguard.counted")}</dt>
            <dd>{e.counted ? t("safeguard.countedYes") : t("safeguard.countedNo")}</dd>
            <dt className="label-mono text-muted">{t("safeguard.hash")}</dt>
            <dd className="flex flex-wrap items-center gap-2">
              <Hash h={e.prompt_hash} />
              {e.prompt_hash ? (
                <button type="button" className="text-[12px] font-semibold underline underline-offset-4" onClick={() => onHash(e.prompt_hash)}>
                  {t("safeguard.sameHash")}
                </button>
              ) : null}
            </dd>
            <dt className="label-mono text-muted">{t("usage.requestId")}</dt>
            <dd className="num text-[12px] break-all">{e.request_id}</dd>
          </dl>
          <div className="flex flex-col gap-1.5">
            <span className="label-mono">{t("safeguard.prompt")}</span>
            <pre className="max-h-64 overflow-auto border border-ink p-3 text-[12px] break-words whitespace-pre-wrap">{e.prompt || t("safeguard.noPrompt")}</pre>
          </div>
          {e.has_body ? <BodyViewer id={e.id} size={e.body_size} /> : <p className="text-[12px] text-muted">{t("safeguard.noBody")}</p>}
        </div>
      ) : null}
    </SheetContent>
  );
}

const CHUNK = 64 * 1024;

/** The stored request body, read in 64 KiB chunks on demand (the first read is audited). */
function BodyViewer({ id, size }: { id: string; size: number }) {
  const { t } = useTranslation();
  const [show, setShow] = useState(false);
  const q = useInfiniteQuery({
    queryKey: [...keys.safeguardEvent(id), "body"],
    queryFn: ({ pageParam }) => unwrap(api.GET("/admin/safeguard-events/{eventId}/body", { params: { path: { eventId: id }, query: { offset: pageParam, limit: CHUNK } } })),
    initialPageParam: 0,
    getNextPageParam: (last) => last.next_offset ?? undefined,
    enabled: show,
    staleTime: Infinity,
  });
  const text = q.data?.pages.map((p) => p.text).join("") ?? "";
  const loaded = q.data?.pages.reduce((n, p) => (p.next_offset ?? p.total_size) - p.offset + n, 0) ?? 0;
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="label-mono">
          {t("safeguard.body")} · {formatBytes(size)}
        </span>
        <div className="flex gap-2">
          {!show ? (
            <Button size="sm" onClick={() => setShow(true)}>
              <FileJson aria-hidden />
              {t("safeguard.showBody")}
            </Button>
          ) : null}
          <Button size="sm" asChild>
            <a href={`/api/v1/admin/safeguard-events/${id}/raw`} download={`safeguard-${id}.json`}>
              <Download aria-hidden />
              {t("safeguard.download")}
            </a>
          </Button>
        </div>
      </div>
      <p className="text-[12px] text-muted">{t("safeguard.bodyAudited")}</p>
      {show ? (
        q.isPending ? (
          <Skeleton className="h-40" />
        ) : q.error ? (
          <ErrorBox error={q.error} onRetry={() => q.refetch()} />
        ) : (
          <>
            <pre data-testid="sg-body" className="max-h-[50dvh] overflow-auto border border-ink p-3 text-[12px] break-all whitespace-pre-wrap">
              {text}
            </pre>
            <div className="flex flex-wrap items-center justify-between gap-2 text-[12px] text-muted">
              <span className="num">{t("safeguard.bodyLoaded", { loaded: formatBytes(loaded), total: formatBytes(size) })}</span>
              {q.hasNextPage ? (
                <Button size="sm" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
                  {t("safeguard.loadMoreBody")}
                </Button>
              ) : null}
            </div>
          </>
        )
      ) : null}
    </div>
  );
}

// ---- blocklist ---------------------------------------------------------------------

function Blocks({ onEvents }: { onEvents: (hash: string) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [status, setStatus] = useState<BlockStatus>("active");
  const [input, setInput] = useState("");
  const query = useDebounced(input.trim(), 400);
  const q = useInfiniteQuery({ ...safeguardBlocksInfinite({ status, q: query || undefined, limit: 100 }), placeholderData: keepPreviousData });
  const rows = useMemo(() => q.data?.pages.flatMap((p) => p.items) ?? [], [q.data]);
  const [open, setOpen] = useState<string | null>(null);
  const [revoking, setRevoking] = useState<Block | null>(null);
  const revoke = useApiMutation({
    mutationFn: (hash: string) => unwrap(api.POST("/admin/safeguard-blocks/{hash}/revoke", { params: { path: { hash } } })),
    success: t("safeguard.revoked"),
    onSuccess: (b) => {
      qc.setQueryData(keys.safeguardBlock(b.hash), b);
      void qc.invalidateQueries({ queryKey: ["admin", "safeguard", "blocks"] });
    },
  });
  const col = columnHelper<Block>();
  const columns = [
    col.accessor("hash", { header: t("safeguard.hash"), cell: (c) => <Hash h={c.getValue()} /> }),
    col.accessor("prompt", {
      header: t("safeguard.prompt"),
      cell: (c) => (
        <button type="button" className="block max-w-[420px] truncate text-left text-[12px] underline-offset-4 hover:underline" title={c.getValue()} onClick={() => setOpen(c.row.original.hash)}>
          {c.getValue()}
        </button>
      ),
    }),
    col.accessor("first_username", { header: t("safeguard.firstUser"), cell: (c) => c.getValue() || "—" }),
    col.accessor("created_at", { header: t("safeguard.addedAt"), cell: (c) => <span className="num text-[12px]">{formatDateTime(c.getValue())}</span> }),
    col.accessor("hits", { header: t("safeguard.hits"), cell: (c) => <span className="num">{formatInt(c.getValue())}</span> }),
    col.display({
      id: "state",
      header: t("safeguard.state"),
      cell: (c) =>
        c.row.original.revoked_at ? (
          <span className="label-mono border border-rule px-1.5 py-0.5 text-muted">{t("safeguard.revokedTag")}</span>
        ) : (
          <span className="label-mono bg-ink px-1.5 py-0.5 text-bg">{t("safeguard.activeTag")}</span>
        ),
    }),
    col.display({
      id: "actions",
      header: () => <span className="sr-only">{t("common.actions")}</span>,
      cell: (c) =>
        !c.row.original.revoked_at ? (
          <Button size="sm" variant="danger" onClick={() => setRevoking(c.row.original)}>
            <Undo2 aria-hidden />
            {t("safeguard.revoke")}
          </Button>
        ) : null,
    }),
  ];
  return (
    <div className="flex flex-col gap-6">
      <div className="grid gap-3 border-2 border-ink p-4 sm:grid-cols-[1fr_200px]">
        <Field label={t("safeguard.searchBlocks")} htmlFor="sg-q">
          <Input id="sg-q" placeholder={t("safeguard.searchBlocksHint")} value={input} onChange={(e) => setInput(e.target.value)} />
        </Field>
        <Field label={t("safeguard.state")} htmlFor="sg-bstatus">
          <NativeSelect id="sg-bstatus" value={status} onChange={(e) => setStatus(e.target.value as BlockStatus)}>
            <option value="active">{t("safeguard.activeTag")}</option>
            <option value="revoked">{t("safeguard.revokedTag")}</option>
            <option value="all">{t("common.all")}</option>
          </NativeSelect>
        </Field>
      </div>
      <p className="text-[12px] text-muted">{t("safeguard.blocksNote")}</p>
      <Section n={1} title={t("safeguard.tabBlocks")} bodyClassName="p-0 sm:p-4">
        <DataTable
          label={t("safeguard.tabBlocks")}
          columns={columns}
          data={rows}
          rowKey={(r) => r.hash}
          loading={q.isPending}
          error={q.error}
          onRetry={() => q.refetch()}
          hasMore={q.hasNextPage}
          fetchingMore={q.isFetchingNextPage}
          onLoadMore={() => void q.fetchNextPage()}
          className="max-sm:p-4"
          empty={<EmptyState className="m-4 sm:m-0" title={t("safeguard.noBlocks")} />}
          renderCard={(r) => (
            <RowCard
              title={<span className="line-clamp-2 text-[13px]">{r.prompt}</span>}
              onClick={() => setOpen(r.hash)}
              lines={[
                [t("safeguard.hash"), <Hash h={r.hash} />],
                [t("safeguard.hits"), formatInt(r.hits)],
                [t("safeguard.state"), r.revoked_at ? t("safeguard.revokedTag") : t("safeguard.activeTag")],
              ]}
            />
          )}
        />
      </Section>
      <Dialog open={!!open} onOpenChange={(o) => !o && setOpen(null)}>
        {open ? <BlockDialog hash={open} onEvents={(h) => (setOpen(null), onEvents(h))} onRevoke={(b) => (setOpen(null), setRevoking(b))} /> : null}
      </Dialog>
      <ConfirmDialog
        open={!!revoking}
        onOpenChange={(o) => !o && setRevoking(null)}
        title={t("safeguard.revokeTitle")}
        description={t("safeguard.revokeBody")}
        confirmLabel={t("safeguard.revoke")}
        onConfirm={() => revoke.mutateAsync(revoking!.hash)}
      />
    </div>
  );
}

function BlockDialog({ hash, onEvents, onRevoke }: { hash: string; onEvents: (hash: string) => void; onRevoke: (b: Block) => void }) {
  const { t } = useTranslation();
  const q = useQuery(safeguardBlockQuery(hash));
  const b = q.data;
  return (
    <DialogContent title={t("safeguard.blockTitle")} wide>
      {q.isPending ? (
        <Skeleton className="h-40" />
      ) : q.error ? (
        <ErrorBox error={q.error} onRetry={() => q.refetch()} />
      ) : b ? (
        <div className="flex flex-col gap-4">
          <dl className="grid grid-cols-[110px_1fr] gap-x-3 gap-y-1.5 text-[13px]">
            <dt className="label-mono text-muted">{t("safeguard.hash")}</dt>
            <dd className="num text-[12px] break-all">{b.hash}</dd>
            <dt className="label-mono text-muted">{t("safeguard.firstUser")}</dt>
            <dd>{b.first_username || "—"}</dd>
            <dt className="label-mono text-muted">{t("safeguard.addedAt")}</dt>
            <dd className="num">{formatDateTime(b.created_at)}</dd>
            <dt className="label-mono text-muted">{t("safeguard.hits")}</dt>
            <dd className="num">
              {formatInt(b.hits)}
              {b.last_hit_at ? ` · ${formatDateTime(b.last_hit_at)}` : ""}
            </dd>
            {b.revoked_at ? (
              <>
                <dt className="label-mono text-muted">{t("safeguard.revokedTag")}</dt>
                <dd className="num">
                  {formatDateTime(b.revoked_at)} · {b.revoked_by_username || "—"}
                </dd>
              </>
            ) : null}
          </dl>
          <pre className="max-h-[40dvh] overflow-auto border border-ink p-3 text-[12px] break-words whitespace-pre-wrap">{b.prompt}</pre>
          <DialogFooter className="mt-0">
            <Button onClick={() => onEvents(b.hash)}>{t("safeguard.sameHash")}</Button>
            {!b.revoked_at ? (
              <Button variant="danger" onClick={() => onRevoke(b)}>
                <Undo2 aria-hidden />
                {t("safeguard.revoke")}
              </Button>
            ) : null}
          </DialogFooter>
        </div>
      ) : null}
    </DialogContent>
  );
}

// ---- appeals -----------------------------------------------------------------------

function Appeals({ status, onStatus }: { status: AppealFilter; onStatus: (s: AppealFilter) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useInfiniteQuery({ ...safeguardAppealsInfinite({ status, limit: 100 }), placeholderData: keepPreviousData });
  const rows = useMemo(() => q.data?.pages.flatMap((p) => p.items) ?? [], [q.data]);
  const [approving, setApproving] = useState<Appeal | null>(null);
  const [rejecting, setRejecting] = useState<Appeal | null>(null);
  const done = (a: Appeal) => {
    void qc.invalidateQueries({ queryKey: ["admin", "safeguard", "appeals"] });
    void qc.invalidateQueries({ queryKey: keys.userSafeguard(a.user_id) });
    void qc.invalidateQueries({ queryKey: keys.user(a.user_id) });
  };
  const approve = useApiMutation({
    mutationFn: (id: string) => unwrap(api.POST("/admin/safeguard-appeals/{appealId}/approve", { params: { path: { appealId: id } } })),
    success: t("safeguard.approved"),
    onSuccess: done,
  });
  return (
    <div className="flex flex-col gap-6">
      <div className="grid gap-3 border-2 border-ink p-4 sm:grid-cols-3">
        <Field label={t("safeguard.appealStatus")} htmlFor="sg-astatus">
          <NativeSelect id="sg-astatus" value={status} onChange={(e) => onStatus(e.target.value as AppealFilter)}>
            {APPEAL_FILTERS.map((s) => (
              <option key={s} value={s}>
                {s === "all" ? t("common.all") : t(`appealStatus.${s}`)}
              </option>
            ))}
          </NativeSelect>
        </Field>
      </div>
      <Section n={1} title={t("safeguard.tabAppeals")} bodyClassName="p-0">
        {q.isPending ? (
          <Skeleton className="m-4 h-24" />
        ) : q.error ? (
          <ErrorBox className="m-4" error={q.error} onRetry={() => q.refetch()} />
        ) : rows.length === 0 ? (
          <EmptyState className="m-4" title={t("safeguard.noAppeals")} />
        ) : (
          <ul className="divide-y divide-rule" data-testid="sg-appeals">
            {rows.map((a) => (
              <li key={a.id} className="flex flex-col gap-2 px-4 py-3">
                <div className="flex flex-wrap items-center gap-3">
                  <Link to="/admin/users/$userId" params={{ userId: a.user_id }} className="font-semibold underline underline-offset-4">
                    {a.username || shortId(a.user_id)}
                  </Link>
                  <AppealStatusTag status={a.status} />
                  <span className="num text-[12px] text-muted">{formatDateTime(a.created_at)}</span>
                  <span className="num ml-auto text-[12px]">{t("safeguard.appealCounts", { used: a.used, allowance: a.allowance, cards: a.reset_cards })}</span>
                </div>
                <p className="text-[13px] break-words whitespace-pre-wrap">{a.note}</p>
                {a.status !== "pending" ? (
                  <p className="text-[12px] text-muted">
                    {t("safeguard.decidedBy", { name: a.decided_by_username || "—", time: formatDateTime(a.decided_at) })}
                    {a.status === "rejected" && a.reject_reason ? ` · ${a.reject_reason}` : ""}
                  </p>
                ) : (
                  <div className="flex flex-wrap gap-2">
                    <Button size="sm" variant="primary" onClick={() => setApproving(a)}>
                      <Check aria-hidden />
                      {t("safeguard.approve")}
                    </Button>
                    <Button size="sm" variant="danger" onClick={() => setRejecting(a)}>
                      <X aria-hidden />
                      {t("safeguard.reject")}
                    </Button>
                    <Link to="/admin/safeguard" search={{ user: a.user_id }} className="self-center text-[12px] font-semibold underline underline-offset-4">
                      {t("safeguard.userEvents")}
                    </Link>
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
        {q.hasNextPage ? (
          <div className="border-t border-rule p-3">
            <Button size="sm" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
              {t("common.loadMore")}
            </Button>
          </div>
        ) : null}
      </Section>
      <ConfirmDialog
        open={!!approving}
        onOpenChange={(o) => !o && setApproving(null)}
        danger={false}
        title={t("safeguard.approveTitle", { name: approving?.username ?? "" })}
        description={t("safeguard.approveBody")}
        confirmLabel={t("safeguard.approve")}
        onConfirm={() => approve.mutateAsync(approving!.id)}
      />
      {rejecting ? <RejectDialog a={rejecting} onClose={() => setRejecting(null)} onDone={done} /> : null}
    </div>
  );
}

function RejectDialog({ a, onClose, onDone }: { a: Appeal; onClose: () => void; onDone: (a: Appeal) => void }) {
  const { t } = useTranslation();
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | undefined>();
  const reject = useApiMutation({
    mutationFn: (r: string) => unwrap(api.POST("/admin/safeguard-appeals/{appealId}/reject", { params: { path: { appealId: a.id } }, body: { reason: r } })),
    success: t("safeguard.rejected"),
    onSuccess: (x) => {
      onDone(x);
      onClose();
    },
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={t("safeguard.rejectTitle", { name: a.username })} description={t("safeguard.rejectHint")}>
        <form
          noValidate
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            const r = reason.trim();
            if (!r) return setError(t("validation.required"));
            if ([...r].length > 1000) return setError(t("validation.max", { n: 1000 }));
            setError(undefined);
            reject.mutate(r);
          }}
        >
          <Field label={t("safeguard.rejectReason")} htmlFor="sg-reason" error={error}>
            <Textarea id="sg-reason" rows={3} aria-invalid={!!error} value={reason} onChange={(e) => setReason(e.target.value)} />
          </Field>
          <DialogFooter className="mt-0">
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" variant="danger" disabled={reject.isPending}>
              {t("safeguard.reject")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
