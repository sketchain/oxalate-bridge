import { defineConfig, devices } from "@playwright/test";

// Runs against a bridge serving the built panel, with cmd/fakecpa and
// `bridge seed-demo` data (see `make e2e`). E2E_BASE_URL overrides the URL;
// PW_CHROMIUM_PATH points at a preinstalled Chromium.
const baseURL = process.env.E2E_BASE_URL ?? "http://127.0.0.1:8080";
const launchOptions = process.env.PW_CHROMIUM_PATH ? { executablePath: process.env.PW_CHROMIUM_PATH } : {};

export default defineConfig({
  testDir: "e2e",
  timeout: 30_000,
  expect: { timeout: 8_000 },
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: { baseURL, trace: "off", locale: "zh-CN", timezoneId: "Asia/Shanghai", launchOptions },
  projects: [
    { name: "smoke", testMatch: /smoke\.spec\.ts/, use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 960 } } },
    { name: "screenshots", testMatch: /screenshots\.spec\.ts/, use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 960 } } },
  ],
});
