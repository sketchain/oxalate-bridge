import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { AlertTriangle, Ban, Copy, KeyRound, Plus } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { adminTokensQuery, keys } from "@/api/queries";
import { formatDateTime } from "@/lib/format";
import { copyText } from "@/lib/hooks";
import { useApiMutation } from "@/lib/mutations";
import { cn } from "@/lib/utils";
import { ConfirmDialog } from "./ConfirmDialog";
import { ErrorBox } from "./Feedback";
import { Section } from "./Section";
import { Button } from "./ui/button";
import { Dialog, DialogContent, DialogFooter } from "./ui/dialog";
import { Input, NativeSelect, Textarea } from "./ui/input";
import { Field } from "./ui/label";
import { Skeleton } from "./ui/skeleton";

type Token = Schemas["AdminToken"];
type Scope = Schemas["AdminTokenScope"];
const EXPIRY = ["never", "7d", "30d", "90d", "365d", "custom"] as const;
type Expiry = (typeof EXPIRY)[number];

function expiryOf(choice: Expiry, custom: string): string | null | "invalid" {
  if (choice === "never") return null;
  if (choice === "custom") {
    const d = new Date(custom);
    return Number.isNaN(d.getTime()) || d.getTime() <= Date.now() ? "invalid" : d.toISOString();
  }
  return new Date(Date.now() + Number.parseInt(choice, 10) * 86_400_000).toISOString();
}

function StatusTag({ status }: { status: Token["status"] }) {
  const { t } = useTranslation();
  return (
    <span
      className={cn(
        "label-mono inline-block px-1.5 py-0.5 whitespace-nowrap",
        status === "active" && "bg-ink text-bg",
        status === "expired" && "border border-ink text-muted",
        status === "revoked" && "bg-red text-on-red",
      )}
    >
      {t(`adminTokens.status.${status}`)}
    </span>
  );
}

function ScopeChoice({ value, onChange }: { value: Scope; onChange: (v: Scope) => void }) {
  const { t } = useTranslation();
  return (
    <fieldset>
      <legend className="label-mono mb-1.5">{t("adminTokens.scope")}</legend>
      <div className="grid border-2 border-ink sm:grid-cols-2">
        {(["read", "write"] as const).map((s, i) => (
          <label
            key={s}
            className={cn(
              "flex cursor-pointer flex-col gap-0.5 px-3 py-2.5 has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-ink",
              i > 0 && "border-t-2 border-ink sm:border-t-0 sm:border-l-2",
              value === s ? "bg-yellow text-on-yellow" : "hover:bg-rule",
            )}
          >
            <span className="flex items-center gap-2 text-[13px] font-semibold">
              <input type="radio" name="token-scope" value={s} checked={value === s} onChange={() => onChange(s)} className="accent-[var(--ink)]" />
              {t(`adminTokens.scopes.${s}`)}
            </span>
            <span className={cn("text-[12px]", value === s ? "" : "text-muted")}>{t(`adminTokens.scopeHint.${s}`)}</span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

function CreateDialog({ open, onOpenChange, onCreated }: { open: boolean; onOpenChange: (o: boolean) => void; onCreated: (c: Schemas["AdminTokenCreated"]) => void }) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [scope, setScope] = useState<Scope>("read");
  const [expiry, setExpiry] = useState<Expiry>("90d");
  const [custom, setCustom] = useState("");
  const [ips, setIps] = useState("");
  const [tried, setTried] = useState(false);
  const exp = expiryOf(expiry, custom);
  const nameErr = tried && !name.trim() ? t("validation.required") : undefined;
  const expErr = tried && exp === "invalid" ? t("adminTokens.expiryInvalid") : undefined;
  const create = useApiMutation({
    mutationFn: (body: Schemas["AdminTokenCreate"]) => unwrap(api.POST("/admin/admin-tokens", { body })),
    onSuccess: (c) => {
      setName("");
      setScope("read");
      setExpiry("90d");
      setCustom("");
      setIps("");
      setTried(false);
      onCreated(c);
    },
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent title={t("adminTokens.create")} description={t("adminTokens.createHint")}>
        <form
          noValidate
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            setTried(true);
            if (!name.trim() || exp === "invalid") return;
            const list = ips
              .split(/[\s,]+/)
              .map((s) => s.trim())
              .filter(Boolean);
            create.mutate({ name: name.trim(), scope, expires_at: exp, ip_allowlist: list.length ? list : null });
          }}
        >
          <Field label={t("adminTokens.name")} htmlFor="token-name" error={nameErr}>
            <Input id="token-name" maxLength={64} value={name} aria-invalid={!!nameErr} onChange={(e) => setName(e.target.value)} placeholder="grafana" />
          </Field>
          <ScopeChoice value={scope} onChange={setScope} />
          <Field label={t("adminTokens.expiry")} htmlFor="token-expiry" error={expErr}>
            <div className="flex flex-wrap gap-2">
              <NativeSelect id="token-expiry" className="w-40" value={expiry} onChange={(e) => setExpiry(e.target.value as Expiry)}>
                {EXPIRY.map((x) => (
                  <option key={x} value={x}>
                    {t(`adminTokens.expiryOption.${x}`)}
                  </option>
                ))}
              </NativeSelect>
              {expiry === "custom" ? (
                <Input type="datetime-local" aria-label={t("adminTokens.expiryCustom")} className="num w-56" value={custom} aria-invalid={!!expErr} onChange={(e) => setCustom(e.target.value)} />
              ) : null}
            </div>
          </Field>
          <Field label={t("adminTokens.ipAllowlist")} htmlFor="token-ips" hint={t("adminTokens.ipHint")}>
            <Textarea id="token-ips" rows={3} className="num" value={ips} onChange={(e) => setIps(e.target.value)} placeholder={"203.0.113.7\n10.0.0.0/8"} />
          </Field>
          <DialogFooter className="mt-2">
            <Button type="button" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {t("adminTokens.createSubmit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** The full token, shown once right after creation. */
function CreatedDialog({ created, onClose }: { created: Schemas["AdminTokenCreated"] | null; onClose: () => void }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  return (
    <Dialog
      open={!!created}
      onOpenChange={(o) => {
        if (!o) {
          setCopied(false);
          onClose();
        }
      }}
    >
      {created ? (
        <DialogContent title={t("adminTokens.createdTitle")} wide>
          <div className="flex flex-col gap-4" data-testid="token-created">
            <div role="alert" className="flex items-start gap-3 border-2 border-ink bg-yellow px-4 py-3 text-on-yellow">
              <AlertTriangle className="mt-0.5 size-5 shrink-0" aria-hidden />
              <p className="text-[13px] font-semibold">{t("adminTokens.onceWarning")}</p>
            </div>
            <div>
              <div className="label-mono mb-1.5">
                {created.token.name} · {t(`adminTokens.scopes.${created.token.scope}`)}
              </div>
              <div className="flex flex-col border-2 border-ink sm:flex-row">
                <code className="num min-w-0 flex-1 px-3 py-3 text-[14px] break-all select-all" data-testid="token-secret">
                  {created.secret}
                </code>
                <Button
                  variant={copied ? "primary" : "secondary"}
                  className="h-auto min-h-11 border-0 border-t-2 border-ink sm:border-t-0 sm:border-l-2"
                  onClick={async () => {
                    try {
                      await copyText(created.secret);
                      setCopied(true);
                      toast.success(t("common.copied"));
                    } catch {
                      toast.error(t("common.copyFailed"));
                    }
                  }}
                >
                  <Copy aria-hidden />
                  {copied ? t("adminTokens.copiedShort") : t("common.copy")}
                </Button>
              </div>
            </div>
            <div className="text-[12px] text-muted">
              <div className="label-mono mb-1">{t("adminTokens.usage")}</div>
              <code className="num block border border-rule px-3 py-2 break-all">curl -H &quot;Authorization: Bearer oba-…&quot; {window.location.origin}/api/v1/admin/overview</code>
            </div>
            <DialogFooter className="mt-0">
              <Button variant="primary" onClick={onClose}>
                {t("adminTokens.saved")}
              </Button>
            </DialogFooter>
          </div>
        </DialogContent>
      ) : null}
    </Dialog>
  );
}

export function AdminTokens({ n }: { n: number }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery(adminTokensQuery);
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<Schemas["AdminTokenCreated"] | null>(null);
  const [revoking, setRevoking] = useState<Token | null>(null);
  const revoke = useApiMutation({
    mutationFn: (id: string) => unwrap(api.POST("/admin/admin-tokens/{tokenId}/revoke", { params: { path: { tokenId: id } } })),
    success: t("adminTokens.revoked"),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.adminTokens }),
  });
  const items = q.data?.items ?? [];
  return (
    <Section
      n={n}
      title={t("adminTokens.title")}
      id="admin-tokens"
      aside={
        <Button size="sm" variant="primary" onClick={() => setCreating(true)}>
          <Plus aria-hidden />
          {t("adminTokens.create")}
        </Button>
      }
    >
      <p className="mb-4 text-[13px] text-muted">{t("adminTokens.intro")}</p>
      {q.error ? <ErrorBox error={q.error} onRetry={() => q.refetch()} /> : null}
      {q.isPending ? (
        <Skeleton className="h-32" />
      ) : items.length === 0 ? (
        <div className="flex items-center gap-3 border-2 border-dashed border-ink px-4 py-6 text-[13px] text-muted">
          <KeyRound className="size-5" aria-hidden />
          {t("adminTokens.empty")}
        </div>
      ) : (
        // relative: the sr-only header cell is absolutely positioned and must not escape the scroller.
        <div className="scroll-x relative">
          <table className="w-full min-w-[760px] text-[13px]" data-testid="admin-tokens">
            <thead>
              <tr className="border-t-2 border-b border-ink text-left">
                {(["name", "prefix", "scope", "expires", "lastUsed", "status"] as const).map((c) => (
                  <th key={c} scope="col" className="label-mono px-2 py-2 font-normal">
                    {t(`adminTokens.col.${c}`)}
                  </th>
                ))}
                <th scope="col" className="w-12">
                  <span className="sr-only">{t("common.actions")}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {items.map((tk) => (
                <tr key={tk.id} className={cn("border-b border-rule align-top", tk.status !== "active" && "text-muted")} data-testid="admin-token-row">
                  <td className="px-2 py-2.5">
                    <div className="font-semibold text-ink">{tk.name}</div>
                    <div className="text-[12px] text-muted">{t("adminTokens.createdBy", { user: tk.created_by_username || "—", at: formatDateTime(tk.created_at) })}</div>
                  </td>
                  <td className="num px-2 py-2.5 whitespace-nowrap">{tk.prefix}</td>
                  <td className="px-2 py-2.5">
                    <span className={cn("label-mono px-1.5 py-0.5", tk.scope === "write" ? "bg-yellow text-on-yellow" : "border border-ink")}>{t(`adminTokens.scopes.${tk.scope}`)}</span>
                    {tk.ip_allowlist?.length ? <div className="num mt-1 text-[11px] text-muted">{tk.ip_allowlist.join(", ")}</div> : null}
                  </td>
                  <td className="num px-2 py-2.5 whitespace-nowrap">{tk.expires_at ? formatDateTime(tk.expires_at) : t("adminTokens.never")}</td>
                  <td className="num px-2 py-2.5">
                    {tk.last_used_at ? (
                      <>
                        <div className="whitespace-nowrap">{formatDateTime(tk.last_used_at)}</div>
                        <div className="text-[11px] text-muted">{tk.last_used_ip}</div>
                      </>
                    ) : (
                      <span className="text-muted">{t("adminTokens.neverUsed")}</span>
                    )}
                  </td>
                  <td className="px-2 py-2.5">
                    <StatusTag status={tk.status} />
                  </td>
                  <td className="px-2 py-2">
                    {tk.status === "revoked" ? null : (
                      <Button variant="ghost" size="icon-sm" aria-label={t("adminTokens.revokeNamed", { name: tk.name })} title={t("adminTokens.revoke")} onClick={() => setRevoking(tk)}>
                        <Ban aria-hidden />
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <CreateDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={(c) => {
          setCreating(false);
          setCreated(c);
          void qc.invalidateQueries({ queryKey: keys.adminTokens });
        }}
      />
      <CreatedDialog created={created} onClose={() => setCreated(null)} />
      <ConfirmDialog
        open={!!revoking}
        onOpenChange={(o) => !o && setRevoking(null)}
        title={t("adminTokens.revokeTitle", { name: revoking?.name ?? "" })}
        description={t("adminTokens.revokeBody")}
        confirmLabel={t("adminTokens.revoke")}
        onConfirm={() => (revoking ? revoke.mutateAsync(revoking.id) : undefined)}
      />
    </Section>
  );
}
