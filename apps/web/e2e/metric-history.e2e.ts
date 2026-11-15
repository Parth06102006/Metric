import { expect, test, type Page, type WebSocketRoute } from "@playwright/test";

type Project = { project_id: string; api_key: string };
type Snapshot = { rps: number };

async function createAndIngest(page: Page): Promise<Project> {
  const created = await page.request.post("/api/projects", { data: { name: `Metric history ${Date.now()}` } });
  expect(created.status()).toBe(201);
  const project = await created.json() as Project;
  const ingested = await page.request.post(`/ingest/${project.project_id}`, {
    headers: { "X-API-Key": project.api_key },
    data: { logs: [{ ts: new Date().toISOString(), endpoint: "/history", method: "GET", status: 200,
      latency_ms: 42, ip: "192.0.2.1", payload_bytes: 10 }] },
  });
  expect(ingested.status()).toBe(202);
  return project;
}

async function count(page: Page): Promise<number> {
  const text = await page.locator("text=/^\\d+ samples?$/").innerText();
  return Number.parseInt(text, 10);
}

test("real reload restores persisted graph history without WebSocket delivery", async ({ page }) => {
  let block = false;
  await page.routeWebSocket("**/ws/**", (socket) => {
    if (block) void socket.close(); else socket.connectToServer();
  });
  const project = await createAndIngest(page);
  await page.goto(`/p/${project.project_id}`);
  await expect.poll(async () => {
    const res = await page.request.get(`/api/projects/${project.project_id}/metrics/history`);
    return (await res.json() as Snapshot[]).length;
  }).toBeGreaterThanOrEqual(4);
  await expect.poll(() => count(page)).toBeGreaterThanOrEqual(4);
  block = true;
  const loaded = page.waitForResponse((response) => response.url().endsWith(`/api/projects/${project.project_id}/metrics/history`) && response.status() === 200);
  await page.reload();
  const snapshots = await (await loaded).json() as Snapshot[];
  expect(snapshots.length).toBeGreaterThanOrEqual(4);
  await expect.poll(() => count(page)).toBe(snapshots.length);
  await expect(page.getByRole("img", { name: `Request rate across ${snapshots.length} samples` })).toBeVisible();
});

test("reconnect restores persisted samples missed during the outage", async ({ page }) => {
  let blocked = false;
  let socket: WebSocketRoute | undefined;
  await page.routeWebSocket("**/ws/**", (route) => {
    if (blocked) void route.close();
    else { socket = route; route.connectToServer(); }
  });
  const project = await createAndIngest(page);
  await page.goto(`/p/${project.project_id}`);
  await expect(page.getByText("Live", { exact: true })).toBeVisible();
  await expect.poll(() => count(page)).toBeGreaterThanOrEqual(3);
  blocked = true;
  await socket!.close();
  await expect(page.getByText("Reconnecting", { exact: true })).toBeVisible();
  const before = await count(page);
  await expect.poll(async () => {
    const res = await page.request.get(`/api/projects/${project.project_id}/metrics/history`);
    return (await res.json() as Snapshot[]).length;
  }).toBeGreaterThan(before + 2);
  expect(await count(page)).toBe(before);
  const refreshed = page.waitForResponse((response) => response.url().endsWith(`/api/projects/${project.project_id}/metrics/history`) && response.status() === 200);
  blocked = false;
  await expect(page.getByText("Live", { exact: true })).toBeVisible();
  await refreshed;
  await expect.poll(() => count(page)).toBeGreaterThan(before + 2);
});
