import { useSyncExternalStore } from "react";

/*
 * PWA runtime: service worker registration and updates, the install prompt
 * (beforeinstallprompt on Android / desktop, a hint on iOS Safari) and
 * display-mode detection. State lives in small stores read with
 * useSyncExternalStore.
 */

function store<T>(initial: T) {
  let value = initial;
  const subs = new Set<() => void>();
  return {
    get: () => value,
    set(v: T) {
      value = v;
      subs.forEach((f) => f());
    },
    subscribe(f: () => void) {
      subs.add(f);
      return () => subs.delete(f);
    },
  };
}

// ---- service worker -----------------------------------------------------------

const waiting = store<ServiceWorker | null>(null);
const registration = store<ServiceWorkerRegistration | null>(null);

function track(reg: ServiceWorkerRegistration) {
  registration.set(reg);
  if (reg.waiting && navigator.serviceWorker.controller) waiting.set(reg.waiting);
  reg.addEventListener("updatefound", () => {
    const sw = reg.installing;
    if (!sw) return;
    sw.addEventListener("statechange", () => {
      // "installed" with a controller = an update waiting to take over (the
      // very first install has no controller and activates by itself).
      if (sw.state === "installed" && navigator.serviceWorker.controller) waiting.set(sw);
    });
  });
}

/** Registers /sw.js (production builds only) and watches for updates. */
export function registerServiceWorker() {
  if (!import.meta.env.PROD || !("serviceWorker" in navigator)) return;
  let reloading = false;
  navigator.serviceWorker.addEventListener("controllerchange", () => {
    // Only reload when the user asked for the new version.
    if (reloading || !applying) return;
    reloading = true;
    window.location.reload();
  });
  const go = () =>
    navigator.serviceWorker
      .register("/sw.js", { scope: "/" })
      .then((reg) => {
        track(reg);
        // Look for a new version every hour while the panel stays open.
        window.setInterval(() => void reg.update().catch(() => {}), 60 * 60_000);
      })
      .catch(() => {
        // No service worker (private mode, unsupported): the panel works without it.
      });
  if (document.readyState === "complete") void go();
  else window.addEventListener("load", () => void go(), { once: true });
}

let applying = false;

/** Activates the waiting version; the page reloads when it takes control. */
export function applyUpdate() {
  const sw = waiting.get();
  if (!sw) return;
  applying = true;
  sw.postMessage({ type: "SKIP_WAITING" });
}

export function useUpdateWaiting(): boolean {
  return useSyncExternalStore(waiting.subscribe, () => waiting.get() !== null, () => false);
}

export function useSwRegistration(): ServiceWorkerRegistration | null {
  return useSyncExternalStore(registration.subscribe, registration.get, () => null);
}

// ---- install --------------------------------------------------------------------

interface BeforeInstallPromptEvent extends Event {
  prompt(): Promise<void>;
  userChoice: Promise<{ outcome: "accepted" | "dismissed" }>;
}

const installEvent = store<BeforeInstallPromptEvent | null>(null);

/** Captures beforeinstallprompt early (it fires once, before React mounts). */
export function captureInstallPrompt() {
  window.addEventListener("beforeinstallprompt", (e) => {
    e.preventDefault();
    installEvent.set(e as BeforeInstallPromptEvent);
  });
  window.addEventListener("appinstalled", () => installEvent.set(null));
}

export function useInstallPrompt(): BeforeInstallPromptEvent | null {
  return useSyncExternalStore(installEvent.subscribe, installEvent.get, () => null);
}

/** Shows the browser's install dialog; the event can be used once. */
export async function promptInstall(): Promise<boolean> {
  const e = installEvent.get();
  if (!e) return false;
  await e.prompt();
  const choice = await e.userChoice;
  installEvent.set(null);
  return choice.outcome === "accepted";
}

// ---- platform ----------------------------------------------------------------------

/** iPhone / iPad (iPadOS reports itself as a Mac with touch). */
export function isIOS(ua = navigator.userAgent, touchPoints = navigator.maxTouchPoints ?? 0): boolean {
  return /iphone|ipad|ipod/i.test(ua) || (/macintosh/i.test(ua) && touchPoints > 1);
}

/** Running as an installed app (home screen / standalone window). */
export function isStandalone(): boolean {
  const nav = navigator as Navigator & { standalone?: boolean };
  return nav.standalone === true || window.matchMedia?.("(display-mode: standalone)").matches === true;
}

const IOS_HINT_KEY = "ob.iosInstallDismissed";

export function iosHintDismissed(): boolean {
  try {
    return localStorage.getItem(IOS_HINT_KEY) === "1";
  } catch {
    return false;
  }
}

export function dismissIosHint() {
  try {
    localStorage.setItem(IOS_HINT_KEY, "1");
  } catch {
    // storage unavailable: hidden for this page only
  }
}

// ---- push ----------------------------------------------------------------------------

export function pushSupported(): boolean {
  return "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
}

/** VAPID public key (base64url) as the bytes PushManager.subscribe wants. */
export function vapidKeyBytes(key: string): Uint8Array<ArrayBuffer> {
  const pad = "=".repeat((4 - (key.length % 4)) % 4);
  const raw = atob((key + pad).replace(/-/g, "+").replace(/_/g, "/"));
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}
