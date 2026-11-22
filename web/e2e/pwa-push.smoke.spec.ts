import { expect, request, test, type Browser, type BrowserContextOptions, type Page } from "@playwright/test";
import { ADMIN, USER } from "./helpers";

// PWA (manifest, service worker, offline page, update prompt, iOS hint),
// admin API tokens and the notification settings. One login per user for
// the whole file: the login endpoint is rate limited per client IP.

type State = Awaited<ReturnType<Awaited<ReturnType<typeof request.newContext>>["storageState"]>>;
let adminState: State;
let userState: State;

test.beforeAll(async ({ baseURL }) => {
  for (const who of [ADMIN, USER]) {
    const ctx = await request.newContext({ baseURL, extraHTTPHeaders: { "X-Requested-With": "e2e" } });
    expect((await ctx.post("/api/v1/auth/login", { data: { username: who.username, password: who.password } })).ok()).toBeTruthy();
    if (who === ADMIN) adminState = await ctx.storageState();
    else userState = await ctx.storageState();
    await ctx.dispose();
  }
});

async function open(browser: Browser, baseURL: string | undefined, state: State | undefined, opts: BrowserContextOptions = {}) {
  const ctx = await browser.newContext({ baseURL, locale: "zh-CN", timezoneId: "Asia/Shanghai", viewport: { width: 1440, height: 960 }, storageState: state, ...opts });
  const page = await ctx.newPage();
  return { ctx, page };
}

/** Waits until the service worker is active and controls the page. */
async function controlled(page: Page) {
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready;
  });
  if (!(await page.evaluate(() => !!navigator.serviceWorker.controller))) {
    await page.reload();
    await page.waitForFunction(() => !!navigator.serviceWorker.controller);
  }
}

test("the web app manifest is served with the right fields", async ({ request: req }) => {
  const res = await req.get("/manifest.webmanifest");
  expect(res.ok()).toBeTruthy();
  expect(res.headers()["content-type"]).toContain("application/manifest+json");
  const m = await res.json();
  expect(m.name).toBe("Oxalate's Bridge");
  expect(m.short_name).toBe("Bridge");
  expect(m.start_url).toBe("/");
  expect(m.scope).toBe("/");
  expect(m.display).toBe("standalone");
  expect(m.background_color).toBe("#ffffff");
  expect(m.theme_color).toBe("#ffffff");
  const sizes = m.icons.map((i: { sizes: string; purpose: string }) => `${i.sizes}:${i.purpose}`);
  expect(sizes).toEqual(expect.arrayContaining(["192x192:any", "512x512:any", "512x512:maskable"]));
  for (const icon of m.icons) expect((await req.get(icon.src)).ok()).toBeTruthy();
  const sw = await req.get("/sw.js");
  expect(sw.headers()["content-type"]).toContain("javascript");
  expect(sw.headers()["cache-control"]).toBe("no-cache");
});

test("the service worker registers, caches no API calls and shows the offline page", async ({ browser, baseURL }) => {
  const { ctx, page } = await open(browser, baseURL, userState);
  await page.goto("/keys");
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await controlled(page);
  const state = await page.evaluate(async () => (await navigator.serviceWorker.getRegistration())?.active?.state);
  expect(state).toBe("activated");
  // Nothing under /api or /v1 is in the cache.
  const cached = await page.evaluate(async () => {
    const out: string[] = [];
    for (const name of await caches.keys()) for (const req of await (await caches.open(name)).keys()) out.push(new URL(req.url).pathname);
    return out;
  });
  expect(cached.length).toBeGreaterThan(5);
  expect(cached.some((p) => p.startsWith("/api/") || p.startsWith("/v1/"))).toBeFalsy();
  expect(cached).toContain("/offline.html");

  await ctx.setOffline(true);
  await page.goto("/usage");
  await expect(page.getByRole("heading", { name: "当前离线" })).toBeVisible();
  await expect(page.getByRole("button", { name: "重试" })).toBeVisible();
  await ctx.setOffline(false);
  await page.getByRole("button", { name: "重试" }).click();
  await expect(page.getByRole("heading", { name: "当前离线" })).toHaveCount(0);
  await expect(page).toHaveURL(/\/usage/);
  await ctx.close();
});

test("a waiting new version shows the refresh bar", async ({ browser, baseURL }) => {
  const { ctx, page } = await open(browser, baseURL, userState);
  await page.goto("/");
  await controlled(page);
  await expect(page.getByTestId("update-banner")).toHaveCount(0);
  // A new script URL is a new version of the same registration.
  await page.evaluate(() => navigator.serviceWorker.register("/sw.js?e2e=next", { scope: "/" }));
  const bar = page.getByTestId("update-banner");
  await expect(bar).toContainText("有新版本，点击刷新");
  const reloaded = page.waitForEvent("load");
  await bar.getByRole("button").click();
  await reloaded;
  await expect(page.getByTestId("update-banner")).toHaveCount(0);
  await ctx.close();
});

test("iOS Safari outside the home screen shows a dismissible install hint once", async ({ browser, baseURL }) => {
  const { ctx, page } = await open(browser, baseURL, undefined, {
    viewport: { width: 390, height: 844 },
    isMobile: true,
    hasTouch: true,
    userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
  });
  await page.goto("/login");
  const hint = page.getByTestId("ios-install-banner");
  await expect(hint).toContainText("添加到主屏幕");
  await hint.getByRole("button", { name: "关闭引导" }).click();
  await expect(hint).toHaveCount(0);
  await page.reload();
  await expect(page.getByRole("heading", { level: 1 }).first()).toBeVisible();
  await expect(page.getByTestId("ios-install-banner")).toHaveCount(0);
  await ctx.close();
});

test("the desktop browser does not show the iOS hint", async ({ page }) => {
  await page.goto("/login");
  await expect(page.getByRole("button", { name: "登录" })).toBeVisible();
  await expect(page.getByTestId("ios-install-banner")).toHaveCount(0);
});

test("admin creates an API token, sees it once, uses it and revokes it", async ({ browser, baseURL }) => {
  const { ctx, page } = await open(browser, baseURL, adminState);
  await page.goto("/admin/settings");
  const group = page.getByRole("region", { name: /管理 API Token/ });
  await group.getByRole("button", { name: "新建 Token" }).click();
  const dialog = page.getByRole("dialog", { name: "新建 Token" });
  const name = `e2e-${Date.now()}`;
  await dialog.getByLabel("名称").fill(name);
  await dialog.getByText("只读", { exact: true }).click();
  await dialog.getByLabel("IP 白名单").fill("127.0.0.1\n::1\n172.16.0.0/12\n10.0.0.0/8\n192.168.0.0/16");
  await dialog.getByRole("button", { name: "创建" }).click();

  const created = page.getByTestId("token-created");
  await expect(created).toContainText("关闭后无法再次查看");
  const secret = (await page.getByTestId("token-secret").textContent())!.trim();
  expect(secret).toMatch(/^oba-[A-Za-z0-9]{40,}$/);
  await ctx.grantPermissions(["clipboard-read", "clipboard-write"]);
  await created.getByRole("button", { name: "复制" }).click();
  await expect(created.getByRole("button", { name: "已复制" })).toBeVisible();
  await page.getByRole("button", { name: "我已保存，关闭" }).click();
  await expect(page.getByTestId("token-secret")).toHaveCount(0);

  const row = group.getByTestId("admin-token-row").filter({ hasText: name });
  await expect(row).toContainText("只读");
  await expect(row).toContainText("有效");
  await expect(row).not.toContainText(secret);

  const bare = await request.newContext({ baseURL });
  const auth = { Authorization: `Bearer ${secret}` };
  expect((await bare.get("/api/v1/admin/overview", { headers: auth })).status()).toBe(200);
  const denied = await bare.post("/api/v1/admin/reconcile", { headers: auth });
  expect(denied.status()).toBe(403);
  expect((await denied.json()).error.code).toBe("admin_token_read_only");

  await row.getByRole("button", { name: `吊销 ${name}` }).click();
  await page.getByRole("dialog").getByRole("button", { name: "吊销" }).click();
  await expect(row).toContainText("已吊销");
  const revoked = await bare.get("/api/v1/admin/overview", { headers: auth });
  expect(revoked.status()).toBe(401);
  expect((await revoked.json()).error.code).toBe("admin_token_revoked");
  await bare.dispose();
  await ctx.close();
});

test("notification settings: switches, threshold, devices and the test button", async ({ browser, baseURL }) => {
  const { ctx, page } = await open(browser, baseURL, userState);
  const api = await request.newContext({ baseURL, storageState: userState, extraHTTPHeaders: { "X-Requested-With": "e2e" } });
  // Start without devices.
  for (const d of (await (await api.get("/api/v1/push/subscriptions")).json()).items) await api.delete(`/api/v1/push/subscriptions/${d.id}`);

  await page.goto("/settings");
  await page.getByRole("link", { name: "通知" }).click();
  await expect(page).toHaveURL(/\/settings\/notifications/);
  await expect(page.getByTestId("device-state")).toBeVisible();
  // Users see only their own events.
  await expect(page.getByRole("heading", { name: "我的账户" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "管理员事件" })).toHaveCount(0);

  // A switch is saved at once.
  const sw = page.getByRole("switch", { name: "自动续费失败" });
  await expect(sw).toBeChecked();
  await sw.click();
  await expect(sw).not.toBeChecked();
  await expect
    .poll(async () => (await (await api.get("/api/v1/notifications/prefs")).json()).items.find((i: { event: string }) => i.event === "auto_renew_failed").enabled)
    .toBe(false);
  await page.reload();
  await expect(page.getByRole("switch", { name: "自动续费失败" })).not.toBeChecked();
  await page.getByRole("switch", { name: "自动续费失败" }).click();
  await expect(page.getByRole("switch", { name: "自动续费失败" })).toBeChecked();

  // Threshold in USD.
  const before = (await (await api.get("/api/v1/notifications/prefs")).json()).balance_threshold_micro;
  await page.getByLabel("余额提醒阈值").fill("12.5");
  await page.getByTestId("pref-balance_low").getByRole("button", { name: "保存" }).click();
  await expect(page.getByText("阈值已保存")).toBeVisible();
  expect((await (await api.get("/api/v1/notifications/prefs")).json()).balance_threshold_micro).toBe(12_500_000);
  expect((await api.put("/api/v1/notifications/prefs", { data: { balance_threshold_micro: before } })).ok()).toBeTruthy();

  // No device yet: the test button says so.
  await page.getByRole("button", { name: "发送测试通知" }).click();
  await expect(page.getByText("还没有开启推送的设备")).toBeVisible();

  // The subscription API: only push services, then the device appears and can be removed.
  const keys = { p256dh: "BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM", auth: "tBHItJI5svbpez7KI4CCXg" };
  expect((await api.post("/api/v1/push/subscriptions", { data: { endpoint: "https://evil.example/push", keys } })).status()).toBe(400);
  const res = await api.post("/api/v1/push/subscriptions", {
    data: { endpoint: `https://fcm.googleapis.com/fcm/send/e2e-${Date.now()}`, keys },
    headers: { "User-Agent": "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Mobile Safari/537.36" },
  });
  expect(res.status()).toBe(201);
  expect((await res.json()).device_label).toBe("Android · Chrome");
  await page.reload();
  const row = page.getByTestId("device-row").filter({ hasText: "Android · Chrome" });
  await expect(row).toBeVisible();
  await row.getByRole("button", { name: "删除设备 Android · Chrome" }).click();
  await expect(page.getByText("设备已删除")).toBeVisible();
  await expect(page.getByTestId("device-row")).toHaveCount(0);
  await api.dispose();
  await ctx.close();
});

test("admins see the admin events in the notification settings", async ({ browser, baseURL }) => {
  const { ctx, page } = await open(browser, baseURL, adminState);
  await page.goto("/settings/notifications");
  await expect(page.getByRole("heading", { name: "管理员事件" })).toBeVisible();
  await expect(page.getByRole("switch", { name: "账号认证异常" })).toBeChecked();
  await expect(page.getByRole("switch", { name: "Safeguard 拦截" })).not.toBeChecked();
  await ctx.close();
});

test("the admin settings page (token table included) has no horizontal overflow on a phone", async ({ browser, baseURL }) => {
  const { ctx, page } = await open(browser, baseURL, adminState, { viewport: { width: 390, height: 844 } });
  await page.goto("/admin/settings");
  await expect(page.getByRole("region", { name: /管理 API Token/ })).toBeVisible();
  await page.waitForLoadState("networkidle");
  const w = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, viewport: window.innerWidth }));
  expect(w.scroll).toBeLessThanOrEqual(w.viewport);
  await ctx.close();
});
