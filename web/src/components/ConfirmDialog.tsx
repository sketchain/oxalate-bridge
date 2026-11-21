import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "./ui/button";
import { Dialog, DialogContent, DialogFooter } from "./ui/dialog";

/** Second confirmation for destructive actions. */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  danger = true,
  onConfirm,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: ReactNode;
  confirmLabel?: string;
  danger?: boolean;
  onConfirm: () => Promise<unknown> | void;
  children?: ReactNode;
}) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState(false);
  return (
    <Dialog open={open} onOpenChange={(o) => !busy && onOpenChange(o)}>
      <DialogContent title={title} description={typeof description === "string" ? description : undefined}>
        {typeof description !== "string" ? description : null}
        {children}
        <DialogFooter>
          <Button onClick={() => onOpenChange(false)} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button
            variant={danger ? "danger" : "primary"}
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              try {
                await onConfirm();
                onOpenChange(false);
              } catch {
                // the caller reports the error; keep the dialog open
              } finally {
                setBusy(false);
              }
            }}
          >
            {confirmLabel ?? t("common.confirm")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
