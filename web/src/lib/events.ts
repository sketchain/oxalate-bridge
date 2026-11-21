import { useEffect, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { Schemas } from "@/api/client";
import { keys } from "@/api/queries";

export type AdminEvent =
  | { type: "account_status"; data: Schemas["AccountStatusEvent"] }
  | { type: "request_settled"; data: Schemas["RequestSettledEvent"] };

type Listener = (ev: AdminEvent) => void;
const listeners = new Set<Listener>();

/** Subscribes to admin SSE events while mounted. */
export function useAdminEvent(fn: Listener) {
  const ref = useRef(fn);
  useEffect(() => {
    ref.current = fn;
  });
  useEffect(() => {
    const l: Listener = (ev) => ref.current(ev);
    listeners.add(l);
    return () => {
      listeners.delete(l);
    };
  }, []);
}

/**
 * Opens one EventSource for the admin area. Account status events refresh
 * the account queries (throttled to one refetch per second) so cards update
 * live; every event is also passed to useAdminEvent listeners.
 */
export function useAdminEventSource(enabled: boolean) {
  const qc = useQueryClient();
  useEffect(() => {
    if (!enabled || typeof EventSource === "undefined") return;
    const es = new EventSource("/api/v1/admin/events", { withCredentials: true });
    let timer: number | undefined;
    const refreshAccounts = () => {
      if (timer !== undefined) return;
      timer = window.setTimeout(() => {
        timer = undefined;
        void qc.invalidateQueries({ queryKey: keys.accounts });
        void qc.invalidateQueries({ queryKey: keys.overview });
      }, 1000);
    };
    const emit = (ev: AdminEvent) => listeners.forEach((l) => l(ev));
    es.addEventListener("account_status", (e) => {
      try {
        emit({ type: "account_status", data: JSON.parse((e as MessageEvent).data) });
      } catch {
        // ignore malformed events
      }
      refreshAccounts();
    });
    es.addEventListener("request_settled", (e) => {
      try {
        emit({ type: "request_settled", data: JSON.parse((e as MessageEvent).data) });
      } catch {
        // ignore malformed events
      }
    });
    return () => {
      es.close();
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [enabled, qc]);
}
