import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { LimitDraft, LimitErrors } from "@/lib/keyLimits";
import { Input, Textarea } from "@/components/ui/input";
import { Field, Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { ScopeToggle } from "./ScopeToggle";

/** A titled group inside the key dialog. */
export function FieldGroup({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <fieldset className="border-2 border-ink">
      <legend className="label-mono mx-3 bg-bg px-1.5">{title}</legend>
      <div className="flex flex-col gap-4 px-4 pt-1 pb-4">
        {hint ? <p className="text-[12px] text-muted">{hint}</p> : null}
        {children}
      </div>
    </fieldset>
  );
}

/** Limit and access-control fields of a key (create, edit and the admin editor). */
export function KeyLimitFields({ draft, onChange, errors, idPrefix = "kl" }: { draft: LimitDraft; onChange: (d: LimitDraft) => void; errors: LimitErrors; idPrefix?: string }) {
  const { t } = useTranslation();
  const set = <K extends keyof LimitDraft>(k: K, v: LimitDraft[K]) => onChange({ ...draft, [k]: v });
  const err = (k: keyof LimitDraft) => (errors[k] ? t(errors[k]!) : undefined);
  const money = (k: "quota" | "l5h" | "l1d" | "l7d", label: string) => (
    <Field label={label} htmlFor={`${idPrefix}-${k}`} hint={t("keyLimits.usdUnlimited")} error={err(k)}>
      <Input id={`${idPrefix}-${k}`} inputMode="decimal" className="num" placeholder="∞" aria-invalid={!!errors[k]} value={draft[k]} onChange={(e) => set(k, e.target.value)} />
    </Field>
  );
  return (
    <>
      <FieldGroup title={t("keyLimits.groupQuota")} hint={t("keyLimits.groupQuotaHint")}>
        <div className="flex flex-col gap-1.5">
          <Label id={`${idPrefix}-scope-label`}>{t("keyLimits.scopeLabel")}</Label>
          <ScopeToggle value={draft.scope} onChange={(v) => set("scope", v)} labelledBy={`${idPrefix}-scope-label`} />
          <p className="text-[12px] text-muted">{t(`keyLimits.scopeHint.${draft.scope}`)}</p>
        </div>
        {money("quota", t("keyLimits.quota"))}
        <div className="grid gap-4 sm:grid-cols-3">
          {money("l5h", t("keyLimits.window5h"))}
          {money("l1d", t("keyLimits.window1d"))}
          {money("l7d", t("keyLimits.window7d"))}
        </div>
        <p className="text-[12px] text-muted">{t("keyLimits.windowHint")}</p>
      </FieldGroup>
      <FieldGroup title={t("keyLimits.groupAccess")}>
        <Field label={t("keyLimits.expires")} htmlFor={`${idPrefix}-expires`} hint={t("keyLimits.expiresHint")} error={err("expires")}>
          <Input id={`${idPrefix}-expires`} type="datetime-local" className="num" value={draft.expires} onChange={(e) => set("expires", e.target.value)} />
        </Field>
        <Field label={t("keyLimits.models")} htmlFor={`${idPrefix}-models`} hint={t("keyLimits.modelsHint")} error={err("models")}>
          <Textarea id={`${idPrefix}-models`} rows={2} className="num" placeholder="claude-sonnet-4.6" value={draft.models} onChange={(e) => set("models", e.target.value)} />
        </Field>
        <Field label={t("keyLimits.ips")} htmlFor={`${idPrefix}-ips`} hint={t("keyLimits.ipsHint")} error={err("ips")}>
          <Textarea id={`${idPrefix}-ips`} rows={2} className="num" placeholder="203.0.113.0/24" value={draft.ips} onChange={(e) => set("ips", e.target.value)} />
        </Field>
        <div className="flex items-start justify-between gap-4 border border-ink px-3 py-2.5">
          <div>
            <Label htmlFor={`${idPrefix}-owner`}>{t("keyLimits.ownerQuery")}</Label>
            <p className="mt-0.5 text-[12px] text-muted">{t("keyLimits.ownerQueryHint")}</p>
          </div>
          <Switch id={`${idPrefix}-owner`} checked={draft.ownerQuery} onCheckedChange={(v) => set("ownerQuery", v)} />
        </div>
      </FieldGroup>
    </>
  );
}
