import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Megaphone, X } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { announcementsQuery, keys } from "@/api/queries";
import { useApiMutation } from "@/lib/mutations";
import { cn } from "@/lib/utils";
import { AnnouncementText } from "./AnnouncementText";

/** Announcements in effect, above the page content. Warning = full yellow, info = ink frame. Dismissal is stored per user on the server. */
export function AnnouncementBanner() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery(announcementsQuery);
  const dismiss = useApiMutation({
    mutationFn: (id: string) => unwrap(api.POST("/announcements/{announcementId}/dismiss", { params: { path: { announcementId: id } } })),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: keys.announcements });
      const prev = qc.getQueryData<Schemas["AnnouncementList"]>(keys.announcements);
      qc.setQueryData<Schemas["AnnouncementList"]>(keys.announcements, (old) => (old ? { items: old.items.filter((a) => a.id !== id) } : old));
      return { prev };
    },
    onError: (_e, _id, ctx) => {
      if (ctx?.prev) qc.setQueryData(keys.announcements, ctx.prev);
    },
  });
  const items = q.data?.items ?? [];
  if (!items.length) return null;
  return (
    <div className="mb-6 flex flex-col gap-3" data-testid="announcements">
      {items.map((a) => {
        const warn = a.level === "warning";
        return (
          <section
            key={a.id}
            role={warn ? "alert" : "status"}
            aria-labelledby={`ann-${a.id}`}
            data-testid="announcement"
            className={cn("flex items-start gap-3 border-2 border-ink px-4 py-3", warn ? "bg-yellow text-on-yellow" : "bg-bg")}
          >
            <span className={cn("mt-0.5 inline-flex size-6 shrink-0 items-center justify-center", warn ? "bg-on-yellow text-yellow" : "bg-ink text-bg")} aria-hidden>
              {warn ? <AlertTriangle className="size-4" /> : <Megaphone className="size-4" />}
            </span>
            <div className="min-w-0 flex-1">
              <h2 id={`ann-${a.id}`} className="text-[14px] font-bold">
                <span className="label-mono mr-2">{t(`announcement.level.${a.level}`)}</span>
                {a.title}
              </h2>
              {a.body ? <AnnouncementText text={a.body} className="mt-1 text-[13px]" /> : null}
            </div>
            <button
              type="button"
              onClick={() => dismiss.mutate(a.id)}
              aria-label={t("announcement.dismiss", { title: a.title })}
              className={cn("-mr-1 inline-flex size-8 shrink-0 items-center justify-center border-2 border-transparent", warn ? "hover:border-on-yellow" : "hover:border-ink")}
            >
              <X className="size-4" aria-hidden />
            </button>
          </section>
        );
      })}
    </div>
  );
}
