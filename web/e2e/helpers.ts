import { expect, type Page } from "@playwright/test";

export const ADMIN = { username: process.env.E2E_ADMIN_USER ?? "admin", password: process.env.E2E_ADMIN_PASSWORD ?? "admin-pass-123" };
export const USER = { username: "alice", password: process.env.E2E_USER_PASSWORD ?? "demo-pass-123" };

export async function login(page: Page, who: { username: string; password: string }, path = "/") {
  await page.goto(`/login?redirect=${encodeURIComponent(path)}`);
  await page.getByLabel("用户名").fill(who.username);
  await page.getByLabel("密码").fill(who.password);
  await page.getByRole("button", { name: "登录" }).click();
  await expect(page).not.toHaveURL(/\/login/);
}

/** USD as the panel formats it. */
export function usd(micro: number): string {
  const abs = Math.abs(micro);
  const digits = abs >= 10_000 || abs === 0 ? 2 : 4;
  return `${micro < 0 ? "-" : ""}$${(abs / 1e6).toLocaleString("en-US", { minimumFractionDigits: digits, maximumFractionDigits: digits })}`;
}
