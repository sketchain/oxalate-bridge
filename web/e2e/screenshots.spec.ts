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

const pages: { who: typeof USER | null; name: string; path: string; prepare?: Prepare; viewportOnly?: boolean }[] = [
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
        await page.goto(await resolve(page, p.path));
        await page.waitForLoadState("networkidle");
        if (p.prepare) await p.prepare(page);
        await page.waitForTimeout(800);
        if (v.mobile) {
          // A full-page capture draws position:fixed elements where the first
          // viewport ends; pin the tab bar to the end of the document instead.
          await page.addStyleTag({ content: '#root > div { position: relative; } [data-testid="bottom-tabs"] { position: absolute !important; }' });
        }
        await page.screenshot({ path: `${OUT}/${p.name}-${v.suffix}.png`, fullPage: !p.viewportOnly });
      });
    }
  });
}
