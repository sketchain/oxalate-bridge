import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { RotateCcw, Send } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { keys, mySafeguardQuery } from "@/api/queries";
import { PageHeader } from "@/components/PageHeader";
import { Section } from "@/components/Section";
import { StatStrip } from "@/components/StatStrip";
import { EmptyState } from "@/components/Feedback";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { AllowanceMeter, AppealStatusTag, LockPanel, SafeguardViewNotice } from "@/components/Safeguard";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/input";
import { Field } from "@/components/ui/label";
import { useApiMutation } from "@/lib/mutations";
import { formatDateTime, formatInt } from "@/lib/format";

export const Route = createFileRoute("/_app/safeguard")({
  loader: ({ context: { queryClient } }) => queryClient.ensureQueryData(mySafeguardQuery),
  component: SafeguardPage,
});

type State = Schemas["MySafeguard"];

/** The user's safeguard allowance, reset cards, lock, appeal and the requests the blocklist stopped. */
function SafeguardPage() {
  const { t } = useTranslation();
  const { data: s } = useSuspenseQuery(mySafeguardQuery);
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const put = (next: State) => qc.setQueryData(keys.mySafeguard, next);
  const reset = useApiMutation({
    mutationFn: () => unwrap(api.POST("/me/safeguard/reset-card")),
    success: t("mySafeguard.resetDone"),
    onSuccess: put,
  });
  return (
    <>
      <PageHeader code="OB-21" title={t("mySafeguard.title")} />
      <div className="flex flex-col gap-6">
        {s.locked ? <LockPanel s={s} /> : null}
        <p className="max-w-3xl text-[13px] text-muted">{t("mySafeguard.intro")}</p>
        <StatStrip
          stats={[
            {
              label: t("mySafeguard.remaining"),
              value: (
                <span data-testid="sg-remaining">
                  {s.remaining} / {s.allowance}
                </span>
              ),
              note: <AllowanceMeter used={s.used} allowance={s.allowance} />,
              alert: s.remaining === 0,
            },
            { label: t("mySafeguard.cards"), value: <span data-testid="sg-cards">{s.reset_cards}</span>, note: t("mySafeguard.cardsNote") },
            { label: t("mySafeguard.triggers"), value: formatInt(s.events_count), note: t("mySafeguard.triggersNote") },
            { label: t("mySafeguard.blocked"), value: <span data-testid="sg-blocked">{formatInt(s.blocked_count)}</span>, note: t("mySafeguard.blockedNote") },
          ]}
        />
        <SafeguardViewNotice />
        <div className="grid gap-6 xl:grid-cols-2">
          <Section
            n={1}
            title={t("mySafeguard.resetSection")}
            aside={
              <Button size="sm" variant="primary" disabled={s.reset_cards === 0 || s.used === 0 || reset.isPending} onClick={() => setConfirming(true)}>
                <RotateCcw aria-hidden />
                {t("mySafeguard.useCard")}
              </Button>
            }
          >
            <p className="text-[13px]">{t("mySafeguard.resetBody")}</p>
            <p className="mt-2 text-[12px] text-muted">
              {s.reset_cards === 0 ? t("mySafeguard.noCards") : s.used === 0 ? t("mySafeguard.nothingToReset") : t("mySafeguard.cardsLeft", { n: s.reset_cards })}
            </p>
          </Section>
          <AppealSection s={s} onChange={put} />
        </div>
        <BlockedSection s={s} />
        <EventsSection s={s} />
      </div>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        danger={false}
        title={t("mySafeguard.useCardTitle")}
        description={t("mySafeguard.useCardBody", { used: s.used, cards: s.reset_cards - 1 })}
        confirmLabel={t("mySafeguard.useCard")}
        onConfirm={() => reset.mutateAsync()}
      />
    </>
  );
}

function AppealSection({ s, onChange }: { s: State; onChange: (s: State) => void }) {
  const { t } = useTranslation();
  const schema = z.object({ note: z.string().trim().min(1, t("validation.required")).max(2000, t("validation.max", { n: 2000 })) });
  const form = useForm<{ note: string }>({ resolver: zodResolver(schema), defaultValues: { note: "" } });
  const submit = useApiMutation({
    mutationFn: (note: string) => unwrap(api.POST("/me/safeguard/appeals", { body: { note } })),
    success: t("mySafeguard.appealSent"),
    onSuccess: (next) => {
      form.reset({ note: "" });
      onChange(next);
    },
  });
  const a = s.appeal;
  const canAppeal = s.locked && a?.status !== "pending";
  return (
    <Section n={2} title={t("mySafeguard.appealSection")} aside={a ? <AppealStatusTag status={a.status} /> : null}>
      <div className="flex flex-col gap-4" data-testid="sg-appeal">
        {a ? (
          <dl className="grid grid-cols-[96px_1fr] gap-x-3 gap-y-1.5 border border-ink px-3 py-2 text-[13px]">
            <dt className="label-mono text-muted">{t("mySafeguard.appealAt")}</dt>
            <dd className="num">{formatDateTime(a.created_at)}</dd>
            <dt className="label-mono text-muted">{t("mySafeguard.appealNote")}</dt>
            <dd className="break-words whitespace-pre-wrap">{a.note}</dd>
            {a.decided_at ? (
              <>
                <dt className="label-mono text-muted">{t("mySafeguard.decidedAt")}</dt>
                <dd className="num">{formatDateTime(a.decided_at)}</dd>
              </>
            ) : null}
            {a.status === "rejected" ? (
              <>
                <dt className="label-mono text-muted">{t("mySafeguard.rejectReason")}</dt>
                <dd className="break-words whitespace-pre-wrap">{a.reject_reason}</dd>
              </>
            ) : null}
          </dl>
        ) : null}
        {a?.status === "pending" ? <p className="text-[13px]">{t("mySafeguard.appealPending")}</p> : null}
        {canAppeal ? (
          <form noValidate className="flex flex-col gap-3" onSubmit={form.handleSubmit((v) => submit.mutate(v.note.trim()))}>
            <Field label={t("mySafeguard.appealNoteLabel")} htmlFor="sg-note" hint={t("mySafeguard.appealHint")} error={form.formState.errors.note?.message}>
              <Textarea id="sg-note" rows={4} aria-invalid={!!form.formState.errors.note} {...form.register("note")} />
            </Field>
            <div>
              <Button type="submit" variant="primary" disabled={submit.isPending}>
                <Send aria-hidden />
                {t("mySafeguard.submitAppeal")}
              </Button>
            </div>
          </form>
        ) : null}
        {!s.locked && a?.status !== "pending" ? <p className="text-[12px] text-muted">{t("mySafeguard.appealOnlyLocked")}</p> : null}
      </div>
    </Section>
  );
}

function BlockedSection({ s }: { s: State }) {
  const { t } = useTranslation();
  return (
    <Section n={3} title={t("mySafeguard.blockedSection")} aside={<span className="num text-[12px] text-muted">{t("mySafeguard.total", { n: formatInt(s.blocked_count) })}</span>} bodyClassName="p-0">
      <p className="border-b border-rule px-4 py-2 text-[12px] text-muted">{t("mySafeguard.blockedHint")}</p>
      {s.recent_blocked.length === 0 ? (
        <EmptyState className="m-4" title={t("mySafeguard.noBlocked")} />
      ) : (
        <ul className="divide-y divide-rule" data-testid="sg-blocked-list">
          {s.recent_blocked.map((b, i) => (
            <li key={`${b.created_at}-${i}`} className="flex flex-wrap items-center justify-between gap-2 px-4 py-2.5 text-[13px]">
              <span className="num">{formatDateTime(b.created_at)}</span>
              <span className="text-muted">
                {b.key_name || t("keys.unnamed")} <span className="num text-[12px]">{b.key_prefix}</span>
              </span>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}

function EventsSection({ s }: { s: State }) {
  const { t } = useTranslation();
  return (
    <Section n={4} title={t("mySafeguard.eventsSection")} aside={<span className="num text-[12px] text-muted">{t("mySafeguard.total", { n: formatInt(s.events_count) })}</span>} bodyClassName="p-0">
      {s.recent_events.length === 0 ? (
        <EmptyState className="m-4" title={t("mySafeguard.noEvents")} />
      ) : (
        <ul className="divide-y divide-rule">
          {s.recent_events.map((e, i) => (
            <li key={`${e.created_at}-${i}`} className="grid gap-1 px-4 py-2.5 text-[13px] sm:grid-cols-[180px_1fr_auto_auto] sm:items-center sm:gap-4">
              <span className="num">{formatDateTime(e.created_at)}</span>
              <span className="font-semibold">{e.model}</span>
              <span className="label-mono bg-red px-1.5 py-0.5 text-on-red">{e.category || "—"}</span>
              <span className="label-mono text-muted">{e.counted ? t("mySafeguard.counted") : t("mySafeguard.notCounted")}</span>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}
