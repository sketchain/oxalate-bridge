import { test, type Page } from "@playwright/test";
import { ADMIN, USER, login } from "./helpers";

// Page screenshots for docs/screenshots: light, dark and phone for each page.
const OUT = process.env.SCREENSHOT_DIR ?? "../docs/screenshots";
const pages: { who: typeof USER | null; name: string; path: string }[] = [
  { who: null, name: "login", path: "/login" },
  { who: USER, name: "user-overview", path: "/" },
  { who: USER, name: "user-keys", path: "/keys" },
  { who: USER, name: "user-usage", path: "/usage" },
  { who: USER, name: "user-plans", path: "/plans" },
  { who: ADMIN, name: "admin-overview", path: "/admin" },
  { who: ADMIN, name: "admin-accounts", path: "/admin/accounts" },
  { who: ADMIN, name: "admin-account-detail", path: "/admin/accounts/:first" },
  { who: ADMIN, name: "admin-subscriptions", path: "/admin/subscriptions" },
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
        if (p.who) await login(page, p.who, "/");
        await page.goto(await resolve(page, p.path));
        await page.waitForLoadState("networkidle");
        await page.waitForTimeout(800);
        if (v.mobile) {
          // A full-page capture draws position:fixed elements where the first
          // viewport ends; pin the tab bar to the end of the document instead.
          await page.addStyleTag({ content: '#root > div { position: relative; } [data-testid="bottom-tabs"] { position: absolute !important; }' });
        }
        await page.screenshot({ path: `${OUT}/${p.name}-${v.suffix}.png`, fullPage: true });
      });
    }
  });
}
