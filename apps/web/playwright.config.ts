import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.e2e.ts",
  timeout: 45_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [["list"], ["html", { open: "never" }]],
  use: {
    baseURL: "http://127.0.0.1:15173",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: [
    {
      command: "bash e2e/start-api.sh",
      url: "http://127.0.0.1:18080/health",
      timeout: 180_000,
      reuseExistingServer: false,
      gracefulShutdown: { signal: "SIGTERM", timeout: 10_000 },
    },
    {
      command: "bun run preview --host 127.0.0.1 --port 15173 --strictPort",
      url: "http://127.0.0.1:15173",
      timeout: 120_000,
      reuseExistingServer: false,
      env: { METRIC_PROXY_TARGET: "http://127.0.0.1:18080" },
      gracefulShutdown: { signal: "SIGTERM", timeout: 10_000 },
    },
  ],
});
