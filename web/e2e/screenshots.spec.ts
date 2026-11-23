import { test, type Cookie, type Page } from "@playwright/test";
import { ADMIN, USER, login } from "./helpers";

// Page screenshots for docs/screenshots: light, dark and phone for each page.
const OUT = process.env.SCREENSHOT_DIR ?? "../docs/screenshots";
type Prepare = (page: Page) => Promise<void>;

// One login per user for the whole run: the login endpoint is rate limited
// per client IP (30 per 15 minutes) and there are more pages than that.
const sessions = new Map<string, Cookie[]>();
async function signIn(page: Page, who: typeof USER) {
  const saved = sessions.get(who.username);
  if (saved) {
    await page.context().addCookies(saved);
    return;
  }
  await login(page, who, "/");
  sessions.set(who.username, await page.context().cookies());
}

/** Fills the query page with alice's "Claude Code" key (owner query enabled) and runs the query. */
const queryWithKey: Prepare = async (page) => {
  const keys = await (await page.request.get("/api/v1/keys")).json();
  const k = keys.items.find((x: { name: string }) => x.name === "Claude Code") ?? keys.items[0];
  const secret = (await (await page.request.get(`/api/v1/keys/${k.id}/secret`)).json()).key;
  await page.goto("/query");
  await page.getByLabel("API Key").fill(secret);
  await page.getByRole("button", { name: "查询", exact: true }).click();
  await page.getByTestId("query-result").waitFor();
};

const openKeyDrawer: Prepare = async (page) => {
  await page.getByRole("button", { name: "查看 Claude Code 的详情" }).first().click();
  await page.getByTestId("key-drawer").getByRole("heading", { name: "限额与用量" }).waitFor();
};

const IPHONE_UA = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1";
const FAKE_KEYS = { p256dh: "BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM", auth: "tBHItJI5svbpez7KI4CCXg" };

/** Two admin tokens (one used, one revoked) so the group shows every state. */
const seedTokens: Prepare = async (page) => {
  const list = await (await page.request.get("/api/v1/admin/admin-tokens")).json();
  if (list.items.length < 3) {
    const headers = { "X-Requested-With": "screenshots" };
    const grafana = await (await page.request.post("/api/v1/admin/admin-tokens", { headers, data: { name: "grafana", scope: "read", ip_allowlist: ["127.0.0.1", "10.0.0.0/8"] } })).json();
    await page.request.get("/api/v1/admin/overview", { headers: { Authorization: `Bearer ${grafana.secret}` } });
    await page.request.post("/api/v1/admin/admin-tokens", { headers, data: { name: "ops-bot", scope: "write", expires_at: new Date(Date.now() + 90 * 86_400_000).toISOString() } });
    const old = await (await page.request.post("/api/v1/admin/admin-tokens", { headers, data: { name: "old-widget", scope: "read" } })).json();
    await page.request.post(`/api/v1/admin/admin-tokens/${old.token.id}/revoke`, { headers });
    await page.reload();
    await page.waitForLoadState("networkidle");
  }
  await page.locator("#admin-tokens").scrollIntoViewIfNeeded();
};

/** Opens the creation dialog, creates a token and leaves the one-time dialog open. */
const tokenCreated: Prepare = async (page) => {
  await page.getByRole("region", { name: /管理 API Token/ }).getByRole("button", { name: "新建 Token" }).click();
  const dialog = page.getByRole("dialog", { name: "新建 Token" });
  await dialog.getByLabel("名称").fill("scriptable-widget");
  await dialog.getByRole("button", { name: "创建" }).click();
  await page.getByTestId("token-created").waitFor();
};

/** alice with two registered devices (fake push endpoints, never sent to). */
const notificationDevices: Prepare = async (page) => {
  const headers = { "X-Requested-With": "screenshots" };
  const list = await (await page.request.get("/api/v1/push/subscriptions")).json();
  if (list.items.length === 0) {
    await page.request.post("/api/v1/push/subscriptions", {
      headers: { ...headers, "User-Agent": IPHONE_UA },
      data: { endpoint: "https://web.push.apple.com/screenshots-iphone", keys: FAKE_KEYS },
    });
    await page.request.post("/api/v1/push/subscriptions", {
      headers: { ...headers, "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 15_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36" },
      data: { endpoint: "https://fcm.googleapis.com/fcm/send/screenshots-mac", keys: FAKE_KEYS },
    });
    await page.reload();
    await page.waitForLoadState("networkidle");
  }
};

/** iOS Safari (not installed): the add-to-home-screen hint. */
const asIPhone = async (page: Page) => {
  await page.addInitScript((ua) => {
    Object.defineProperty(navigator, "userAgent", { get: () => ua });
    localStorage.removeItem("ob.iosInstallDismissed");
  }, IPHONE_UA);
};

const pages: {
  who: typeof USER | null;
  name: string;
  path: string;
  prepare?: Prepare;
  viewportOnly?: boolean;
  /** Runs before the page loads. */
  init?: Prepare;
  /** Captures only this element. */
  element?: string;
}[] = [
  { who: null, name: "login", path: "/login" },
  { who: USER, name: "user-overview", path: "/" },
  { who: USER, name: "user-keys", path: "/keys" },
  { who: USER, name: "user-usage", path: "/usage" },
  { who: USER, name: "user-plans", path: "/plans" },
  { who: USER, name: "user-key-drawer", path: "/keys", prepare: openKeyDrawer, viewportOnly: true },
  { who: USER, name: "user-pricing", path: "/pricing" },
  { who: USER, name: "query", path: "/query", prepare: queryWithKey },
  { who: ADMIN, name: "admin-overview", path: "/admin" },
  { who: ADMIN, name: "admin-accounts", path: "/admin/accounts" },
  { who: ADMIN, name: "admin-account-detail", path: "/admin/accounts/:first" },
  { who: ADMIN, name: "admin-subscriptions", path: "/admin/subscriptions" },
  { who: ADMIN, name: "admin-redeem", path: "/admin/redeem" },
  { who: ADMIN, name: "admin-tokens", path: "/admin/settings", prepare: seedTokens, element: "section:has(#admin-tokens)" },
  { who: ADMIN, name: "admin-token-created", path: "/admin/settings", prepare: tokenCreated, viewportOnly: true },
  { who: USER, name: "user-notifications", path: "/settings/notifications", prepare: notificationDevices },
  { who: null, name: "offline", path: "/offline.html", viewportOnly: true },
  { who: USER, name: "ios-install-banner", path: "/", init: asIPhone, viewportOnly: true },
];

async function resolve(page: Page, path: string) {
  if (!path.includes(":first")) return path;
  const list = await (await page.request.get("/api/v1/admin/accounts")).json();
  return path.replace(":first", list.items[0].id);
}

const variants = [
  { suffix: "light", colorScheme: "light" as const, viewport: { width: 1440, height: 960 }, mobile: false },
  { suffix: "dark", colorScheme: "dark" as const, viewport: { width: 1440, height: 960 }, mobile: false },
  { suffix: "mobile", colorScheme: "light" as const, viewport: { width: 390, height: 844 }, mobile: true },
];

for (const v of variants) {
  test.describe(v.suffix, () => {
    test.use({ colorScheme: v.colorScheme, viewport: v.viewport, isMobile: v.mobile, hasTouch: v.mobile, deviceScaleFactor: v.mobile ? 2 : 1 });
    for (const p of pages) {
      test(`${p.name} (${v.suffix})`, async ({ page }) => {
        if (p.who) await signIn(page, p.who);
        if (p.init) await p.init(page);
        await page.goto(await resolve(page, p.path));
        await page.waitForLoadState("networkidle");
        if (p.prepare) await p.prepare(page);
        await page.waitForTimeout(800);
        if (v.mobile && !p.viewportOnly) {
          // A full-page capture draws position:fixed elements where the first
          // viewport ends; pin the tab bar to the end of the document instead.
          await page.addStyleTag({ content: '#root > div:not(.pwa-bar) { position: relative; } [data-testid="bottom-tabs"] { position: absolute !important; }' });
        }
        if (p.element) await page.locator(p.element).screenshot({ path: `${OUT}/${p.name}-${v.suffix}.png` });
        else await page.screenshot({ path: `${OUT}/${p.name}-${v.suffix}.png`, fullPage: !p.viewportOnly });
      });
    }
  });
}
