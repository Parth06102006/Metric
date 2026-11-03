import { expect, test } from "@playwright/test";

test("charts share a wall-clock range and expose actual timestamped samples", async ({ page }, testInfo) => {
  const created = await page.request.post("/api/projects", { data: { name: `Charts ${Date.now()}` } });
  expect(created.status()).toBe(201);
  const project = await created.json() as { project_id: string };
  const now = Date.now();
  const makeSnapshot = (ageMinutes: number, rps: number) => ({
    ts: new Date(now - ageMinutes * 60_000).toISOString(), window_s: 60, rps,
    p50: 20, p95: 80, p99: 120, error_rate: 2.5, unique_ips: 4,
    top_ip_rps: 0.1, avg_payload_bytes: 20, endpoints: {},
  });
  await page.route("**/metrics/history", (route) => route.fulfill({ json: [makeSnapshot(20, 1), makeSnapshot(5, 2), makeSnapshot(1, 3)] }));
  await page.routeWebSocket("**/ws/**", (socket) => { void socket.close(); });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(`/p/${project.project_id}`);
  const traffic = page.getByRole("img", { name: "Request rate across 2 samples" });
  await expect(traffic).toBeVisible();
  await expect(page.getByRole("img", { name: "Latency percentiles across 2 samples" })).toBeVisible();
  await expect(page.getByRole("img", { name: "Error rate across 2 samples" })).toBeVisible();
  await expect(page.getByRole("button", { name: "15 min" })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByText(/Latest sample:/)).toContainText("60s rolling window");
  await page.getByRole("button", { name: "1 hour" }).click();
  await expect(page.getByRole("img", { name: "Request rate across 3 samples" })).toBeVisible();
  await page.getByRole("button", { name: "15 min" }).click();
  const overlay = traffic.locator(".u-over");
  await overlay.scrollIntoViewIfNeeded();
  const box = await overlay.boundingBox();
  expect(box).not.toBeNull();
  await page.mouse.move(box!.x + box!.width * (14 / 15), box!.y + box!.height / 2);
  const tooltip = traffic.locator("..").locator(".pointer-events-none.absolute.z-10");
  await expect(tooltip).toContainText("Requests / sec: 3 req/s");
  await expect(tooltip).toContainText("60s rolling window");
  await page.getByText("View recent samples").first().click();
  await expect(page.getByRole("table", { name: "Most recent samples for Request rate" })).toContainText("3 req/s");
  await page.screenshot({ path: testInfo.outputPath("graphs-desktop.png"), fullPage: true });
  await page.setViewportSize({ width: 375, height: 812 });
  await page.screenshot({ path: testInfo.outputPath("graphs-mobile.png"), fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
});

test.describe("local time display", () => {
  test.use({ timezoneId: "Asia/Kolkata" });
  test("labels the chosen local timezone and the sample's real local time", async ({ page }) => {
    const created = await page.request.post("/api/projects", { data: { name: `Timezone ${Date.now()}` } });
    expect(created.status()).toBe(201);
    const project = await created.json() as { project_id: string };
    const ts = new Date(Date.now() - 30_000).toISOString();
    await page.route("**/metrics/history", (route) => route.fulfill({ json: [{ ts, window_s: 60, rps: 2, p50: 10, p95: 20, p99: 30, error_rate: 0, unique_ips: 1, top_ip_rps: 0, avg_payload_bytes: 0, endpoints: {} }] }));
    await page.routeWebSocket("**/ws/**", (socket) => { void socket.close(); });
    await page.goto(`/p/${project.project_id}`);
    await expect(page.getByText("Asia/Calcutta").or(page.getByText("Asia/Kolkata"))).toBeVisible();
    const expected = await page.evaluate((timestamp) => new Date(timestamp).toLocaleString(), ts);
    await expect(page.getByText(/Latest sample:/)).toContainText(expected);
    await expect(page.getByRole("img", { name: "Request rate across 1 sample" })).toBeVisible();
  });
});
