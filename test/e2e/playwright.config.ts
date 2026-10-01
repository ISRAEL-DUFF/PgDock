import { defineConfig, devices } from "@playwright/test";

// The control plane under test, e.g. https://pgdock.test (install bundle)
// or http://127.0.0.1:18080 (dev server).
const baseURL = process.env.PGDOCK_E2E_URL ?? "http://127.0.0.1:18080";
// Map hostnames to 127.0.0.1 so tests can use real names without DNS.
const hostRules = process.env.PGDOCK_E2E_HOST_RULES;

export default defineConfig({
  testDir: ".",
  timeout: 5 * 60_000,
  expect: { timeout: 30_000 },
  fullyParallel: false,
  workers: 1,
  reporter: [["list"]],
  use: {
    baseURL,
    ignoreHTTPSErrors: !!process.env.PGDOCK_E2E_IGNORE_HTTPS_ERRORS,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    permissions: ["clipboard-read", "clipboard-write"],
    launchOptions: {
      executablePath: process.env.PGDOCK_E2E_CHROMIUM || undefined,
      // Mapped names point at a local stack: never send them through a proxy.
      args: hostRules ? [`--host-resolver-rules=${hostRules}`, "--no-proxy-server"] : [],
    },
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
