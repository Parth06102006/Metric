import { expect, test, type Page, type WebSocketRoute } from "@playwright/test";

type CreatedProject = { project_id: string; api_key: string; name: string };

async function createProject(page: Page, name: string): Promise<CreatedProject> {
  await page.goto("/");
  await page.getByRole("button", { name: "New project" }).click();
  await page.getByLabel("Project name").fill(name);
  const created = page.waitForResponse((response) =>
    response.url().endsWith("/api/projects") && response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Create", exact: true }).click();
  const response = await created;
  expect(response.status()).toBe(201);
  const project = (await response.json()) as CreatedProject;
  await expect(page.getByRole("status").filter({ hasText: `Project created: ${name}` })).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: project.api_key })).toBeVisible();
  await page.getByRole("link", { name }).click();
  await expect(page.getByRole("heading", { name, exact: true })).toBeVisible();
  await expect(page.getByText("Live", { exact: true })).toBeVisible();
  return project;
}

function errorLogs() {
  return Array.from({ length: 20 }, () => ({
    ts: new Date().toISOString(), endpoint: "/e2e/checkout", method: "GET",
    status: 500, latency_ms: 50, ip: "192.0.2.1", payload_bytes: 100,
  }));
}

test("create a project, ingest errors, and view a persistent incident", async ({ page, request }) => {
  const project = await createProject(page, `Browser flow ${Date.now()}`);
  await page.getByLabel("Error rate (%)").fill("10");
  await page.getByRole("button", { name: "Save thresholds" }).click();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();

  const denied = await request.post(`/ingest/${project.project_id}`, {
    headers: { "X-API-Key": "wrong" }, data: { logs: errorLogs() },
  });
  expect(denied.status()).toBe(401);
  const accepted = await request.post(`/ingest/${project.project_id}`, {
    headers: { "X-API-Key": project.api_key }, data: { logs: errorLogs() },
  });
  expect(accepted.status()).toBe(202);

  const history = page.getByRole("list", { name: "Anomaly history" });
  await expect(history.getByText("high_error_rate", { exact: true })).toBeVisible();
  const chart = page.getByRole("img", { name: /Latency percentiles across/ });
  const overlay = chart.locator(".u-over");
  await overlay.scrollIntoViewIfNeeded();
  const bounds = await overlay.boundingBox();
  expect(bounds).not.toBeNull();
  await page.mouse.move(bounds!.x + bounds!.width - 5, bounds!.y + bounds!.height / 2);
  await expect(chart.locator("..").locator(".pointer-events-none.absolute.z-10")).toContainText("P95");
  await expect(history.getByText("Advisory analysis: disabled")).toBeVisible();
  await expect(history.locator("li")).toHaveCount(1);
  await page.reload();
  await expect(page.getByLabel("Error rate (%)")).toHaveValue("10");
  await expect(page.getByRole("list", { name: "Anomaly history" }).getByText("high_error_rate", { exact: true })).toBeVisible();
  await expect(page.getByRole("list", { name: "Anomaly history" }).locator("li")).toHaveCount(1);
});

test("reconnects and recovers an incident missed during an outage", async ({ page, request }) => {
  let blocked = false;
  let attemptsWhileBlocked = 0;
  let activeSocket: WebSocketRoute | undefined;
  await page.routeWebSocket("**/ws/**", (socket) => {
    if (blocked) {
      attemptsWhileBlocked++;
      void socket.close();
    } else {
      activeSocket = socket;
      socket.connectToServer();
    }
  });
  const project = await createProject(page, `Reconnect flow ${Date.now()}`);
  expect(activeSocket).toBeDefined();
  blocked = true;
  await activeSocket!.close();
  await expect(page.getByText("Reconnecting", { exact: true })).toBeVisible();

  const accepted = await request.post(`/ingest/${project.project_id}`, {
    headers: { "X-API-Key": project.api_key }, data: { logs: errorLogs() },
  });
  expect(accepted.status()).toBe(202);
  await expect.poll(async () => {
    const response = await request.get(`/api/projects/${project.project_id}/anomalies`);
    return (await response.json() as unknown[]).length;
  }, { timeout: 10_000 }).toBe(1);
  await expect(page.getByText("high_error_rate", { exact: true })).toHaveCount(0);
  // Hold the outage through at least one failed reconnect, not just the initial disconnect.
  await expect.poll(() => attemptsWhileBlocked, { timeout: 10_000 }).toBeGreaterThanOrEqual(1);
  blocked = false;
  const recoveryStarted = performance.now();
  await expect(page.getByText("Live", { exact: true })).toBeVisible();
  const history = page.getByRole("list", { name: "Anomaly history" });
  await expect(history.getByText("high_error_rate", { exact: true })).toBeVisible();
  await expect(history.locator("li")).toHaveCount(1);
  console.log(`WebSocket recovery: ${Math.round(performance.now() - recoveryStarted)} ms; blocked reconnect attempts: ${attemptsWhileBlocked}; duplicate incidents: 0`);
});
