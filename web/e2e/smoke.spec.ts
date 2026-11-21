import { expect, test } from "@playwright/test";
import { ADMIN, USER, login, usd } from "./helpers";

test("unauthenticated visitors are sent to the login page", async ({ page }) => {
  await page.goto("/usage");
  await expect(page).toHaveURL(/\/login\?redirect=%2Fusage/);
  await page.getByLabel("用户名").fill(USER.username);
  await page.getByLabel("密码").fill("wrong-password");
  await page.getByRole("button", { name: "登录" }).click();
  await expect(page.getByRole("alert")).toContainText("登录已失效或用户名密码错误");
});

test("login and the overview shows the user's figures", async ({ page }) => {
  await login(page, USER);
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  const balance = await (await page.request.get("/api/v1/balance")).json();
  const strip = page.locator("dl").first();
  await expect(strip).toContainText(usd(balance.available_micro));
  await expect(strip).toContainText(`预扣中 ${usd(balance.held_micro)}`);
  const subs = await (await page.request.get("/api/v1/subscriptions")).json();
  const remaining = subs.items
    .filter((s: { status: string }) => s.status === "active")
    .reduce((a: number, s: { current_period?: { remaining_micro: number } }) => a + Math.max(0, s.current_period?.remaining_micro ?? 0), 0);
  await expect(strip).toContainText(usd(remaining));
  const avail = page.getByRole("region", { name: "可用额度" });
  await expect(avail.getByText("预估", { exact: true })).toBeVisible();
  await expect(avail.getByRole("table", { name: "普通与强制调用可用额度" })).toContainText("普通调用");
  await expect(page.getByRole("region", { name: "最近请求" }).locator("li")).toHaveCount(6);
});

test("create a key, see it in full and copy it", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await login(page, USER, "/keys");
  await page.getByRole("button", { name: "新建 Key" }).first().click();
  const dialog = page.getByRole("dialog", { name: "新建 API Key" });
  await dialog.getByLabel("名称").fill("smoke key");
  await dialog.getByRole("switch", { name: "开启 PLAN 档" }).click();
  await dialog.getByRole("button", { name: "创建" }).click();
  const created = page.getByRole("dialog", { name: "Key 已创建" });
  const key = (await created.getByTestId("new-key").textContent())?.trim() ?? "";
  expect(key).toMatch(/^sk-/);
  await created.getByRole("button", { name: "复制完整 Key" }).click();
  await expect(page.getByText("已复制到剪贴板")).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(key);
  await created.getByRole("button", { name: "完成" }).click();
  await expect(page.getByRole("table", { name: "Key 列表" })).toContainText("smoke key");
});

test("admin assigns a pending subscription to an account", async ({ page }) => {
  await login(page, ADMIN, "/admin");
  await expect(page.getByRole("status").filter({ hasText: "等待分配账号" })).toBeVisible();
  await page.getByRole("link", { name: "去分配" }).click();
  await expect(page).toHaveURL(/\/admin\/subscriptions/);
  const table = page.getByRole("table", { name: "待分配队列" });
  await expect(table).toContainText("Sonnet 轻量包");
  await table.getByRole("button", { name: "分配" }).first().click();
  const dialog = page.getByRole("dialog", { name: "分配账号" });
  await dialog.getByText("claude-max-tokyo").click();
  // Capacity and warnings come from the backend dry run.
  const check = dialog.getByTestId("bind-check");
  await expect(check).toBeVisible();
  await expect(check.getByText("已售周额度")).toBeVisible();
  await dialog.getByRole("button", { name: /确认/ }).click();
  await expect(page.getByText(/^已分配$|请留意下列提醒/).first()).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByText("没有待分配的订阅")).toBeVisible();
});

test("account statuses are translated and oversold accounts are flagged", async ({ page }) => {
  await login(page, ADMIN, "/admin/accounts");
  const seoul = page.getByRole("article", { name: "claude-max-seoul" });
  await expect(seoul.locator('[data-status="five_hour_full"]')).toHaveText("5h 已满");
  // Raw reason codes never reach the page.
  await expect(page.locator("main")).not.toContainText(/5h_exhausted|week_exhausted|penalized|quota_stale/);
  const summary = (await (await page.request.get("/api/v1/admin/accounts")).json()).summary;
  const line = page.getByTestId("oversold-line").first();
  if (summary.oversold_count > 0) {
    await expect(line).toContainText(`超卖账号 ${summary.oversold_count} 个`);
    await expect(page.getByTestId("headroom-oversold").first()).toContainText("预留超出剩余");
  } else {
    await expect(line).toHaveText("没有超卖账号");
  }
  await page.goto("/admin");
  await expect(page.getByTestId("oversold-line").first()).toBeVisible();
});

test("the login page shows the new logo", async ({ page }) => {
  await page.goto("/login");
  await expect(page.getByTestId("logo-mark")).toBeVisible();
  await expect(page.getByText("OXALATE'S BRIDGE")).toBeVisible();
  const icons = await page.locator('link[rel="icon"], link[rel="apple-touch-icon"]').evaluateAll((ls) => ls.map((l) => l.getAttribute("href")));
  expect(icons).toEqual(expect.arrayContaining(["/favicon.svg", "/favicon-32.png", "/apple-touch-icon.png"]));
  for (const href of ["/favicon.svg", "/favicon-32.png", "/apple-touch-icon.png"]) {
    expect((await page.request.get(href)).status()).toBe(200);
  }
});

test("dark mode toggle is applied and remembered", async ({ page }) => {
  await login(page, USER);
  const html = page.locator("html");
  await expect(html).not.toHaveClass(/dark/);
  await page.getByRole("button", { name: "深色" }).first().click();
  await expect(html).toHaveClass(/dark/);
  expect(await page.evaluate(() => getComputedStyle(document.body).backgroundColor)).toBe("rgb(11, 11, 11)");
  await page.reload();
  await expect(html).toHaveClass(/dark/);
  await page.goto("/settings");
  await page.getByText("浅色", { exact: true }).click();
  await expect(html).not.toHaveClass(/dark/);
});

const USER_PAGES = ["/", "/keys", "/usage", "/plans", "/billing", "/settings", "/me"];

test.describe("mobile", () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });

  test("no horizontal overflow on any user page", async ({ page }) => {
    await login(page, USER);
    for (const path of USER_PAGES) {
      await page.goto(path);
      await page.waitForLoadState("networkidle");
      const width = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, viewport: window.innerWidth }));
      expect(width.scroll, `scrollWidth on ${path}`).toBeLessThanOrEqual(width.viewport);
    }
  });

  test("the bottom tab bar is fixed to the bottom and never covers the content", async ({ page }) => {
    await login(page, USER);
    for (const path of USER_PAGES) {
      await page.goto(path);
      await page.waitForLoadState("networkidle");
      const tabs = page.getByTestId("bottom-tabs");
      const vh = page.viewportSize()!.height;
      let box = (await tabs.boundingBox())!;
      expect(Math.round(box.y + box.height), `tab bar bottom on ${path}`).toBe(vh);
      await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
      await page.waitForTimeout(150);
      box = (await tabs.boundingBox())!;
      expect(Math.round(box.y + box.height), `tab bar bottom after scrolling on ${path}`).toBe(vh);
      const last = await page.evaluate(() => {
        const container = document.querySelector("#main > div");
        const el = container?.lastElementChild;
        return el ? el.getBoundingClientRect().bottom : 0;
      });
      expect(last, `last content element on ${path}`).toBeGreaterThan(0);
      expect(last, `last content element overlaps the tab bar on ${path}`).toBeLessThanOrEqual(box.y);
    }
  });

  test("availability is one card per tier on a phone", async ({ page }) => {
    await login(page, USER);
    const cards = page.getByTestId("availability-cards");
    await expect(cards).toBeVisible();
    const normal = cards.getByRole("listitem", { name: "普通调用" });
    for (const h of ["1h", "5h", "1d", "7d"]) await expect(normal.getByText(h, { exact: true })).toBeVisible();
    await expect(cards.getByRole("listitem", { name: "强制调用" })).toBeVisible();
    await expect(page.getByRole("table", { name: "普通与强制调用可用额度" })).toBeHidden();
  });

  test("overview works on a phone with bottom tabs", async ({ page }) => {
    await login(page, USER);
    const tabs = page.getByRole("navigation", { name: "用户" }).last();
    await expect(tabs.getByRole("link", { name: "概览" })).toHaveAttribute("aria-current", "page");
    await expect(page.getByRole("region", { name: "可用额度" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
    await tabs.getByRole("link", { name: "我的" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "我的" })).toBeVisible();
  });
});
