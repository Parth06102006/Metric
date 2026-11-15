import { expect, test } from "@playwright/test";

test("anonymous visitors see the public homepage without a login redirect", async ({ page }) => {
  await page.route("**/api/auth/me", (route) => route.fulfill({ status: 401, contentType: "application/json", body: '{"error":"unauthorized"}' }));
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: /See the traffic/ })).toBeVisible();
  await expect(page.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/oauth2/start?rd=%2Fdashboard");
  await expect(page.locator(".landing-footer strong")).toHaveText("METRIC");
  await expect(page.locator(".feature-visual canvas")).toHaveCount(0);
  await page.setViewportSize({ width: 375, height: 812 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
});

test("homepage renders without overflow and supports the motion fallback", async ({ page }, testInfo) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/home");
  await expect(page.getByRole("heading", { name: /See the traffic/ })).toBeVisible();
  await expect(page.getByRole("heading", { name: "From request to insight." })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Send your traffic" })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("home-desktop.png"), fullPage: true });
  await page.setViewportSize({ width: 375, height: 812 });
  await page.screenshot({ path: testInfo.outputPath("home-mobile.png"), fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
  expect(errors).toEqual([]);
});

test("project access and rotated-key visibility work in the browser", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveURL(/\/dashboard$/);
  const response = await page.request.post("/api/projects", { data: { name: `Access ${Date.now()}` } });
  expect(response.status()).toBe(201);
  const project = await response.json() as { project_id: string };
  await page.goto(`/p/${project.project_id}`);
  await expect(page.getByRole("heading", { name: "Project access" })).toBeVisible();
  await page.getByLabel("Find a signed-in user").fill("dev@localhost");
  await page.getByRole("button", { name: "Add", exact: true }).click();
  await expect(page.getByRole("list", { name: "Project members" })).toContainText("dev@localhost");
  await page.getByRole("button", { name: "Remove dev@localhost" }).click();
  await expect(page.getByRole("list", { name: "Project members" })).not.toContainText("dev@localhost");
  await page.getByRole("button", { name: "API key" }).click();
  await page.getByRole("button", { name: "Rotate key" }).click();
  await expect(page.getByText("New key — copy it now")).toBeVisible();
  const key = page.locator("#key-management code");
  await expect(key).toHaveText("••••••••••••••••••••••••");
  await page.getByRole("button", { name: "Show key" }).click();
  await expect(key).not.toHaveText("••••••••••••••••••••••••");
  await page.getByRole("button", { name: "Hide key" }).click();
  await expect(key).toHaveText("••••••••••••••••••••••••");
  await expect(page.getByRole("img", { name: /Error rate across/ })).toBeVisible();
});

test("signed-in users go to the dashboard and can visit home", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveURL(/\/dashboard$/);
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
  await page.getByRole("link", { name: "Product home", exact: false }).click();
  await expect(page).toHaveURL(/\/home$/);
  await expect(page.getByRole("link", { name: "Dashboard", exact: true })).toBeVisible();
});

test("authenticated shell stays responsive and skeleton motion respects reduced motion", async ({ page }, testInfo) => {
  const projectName = `Responsive ${Date.now()}`;
  const created = await page.request.post("/api/projects", { data: { name: projectName } });
  expect(created.status()).toBe(201);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.route("**/api/projects", async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 450));
    await route.continue();
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/dashboard");
  await expect(page.getByRole("status", { name: "Loading projects" })).toBeVisible();
  const animation = await page.locator(".skeleton").first().evaluate((element) => getComputedStyle(element, "::after").animationDuration);
  expect(animation).toBe("0s");
  const pageAnimation = await page.locator("main > div").first().evaluate((element) => parseFloat(getComputedStyle(element).animationDuration));
  expect(pageAnimation).toBeLessThanOrEqual(0.001);
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
  await expect(page.getByRole("status", { name: "Loading projects" })).toBeHidden();
  await expect(page.getByRole("link", { name: new RegExp(projectName) })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.screenshot({ path: testInfo.outputPath("projects-mobile.png"), fullPage: true });
  await page.getByRole("link", { name: "Product home" }).click();
  await expect(page).toHaveURL(/\/home$/);
  await page.goto("/dashboard");
  await expect(page.getByRole("link", { name: new RegExp(projectName) })).toBeVisible();
  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(page.locator("aside.app-sidebar")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(1440);
  await page.screenshot({ path: testInfo.outputPath("projects-desktop.png"), fullPage: true });
  await page.getByRole("link", { name: new RegExp(projectName) }).click();
  await expect(page.getByRole("heading", { name: projectName })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Workspace navigation" }).getByRole("link", { name: "Projects" })).toHaveAttribute("aria-current", "location");
  await page.screenshot({ path: testInfo.outputPath("project-detail-desktop.png"), fullPage: true });
  await page.setViewportSize({ width: 375, height: 812 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
  await page.screenshot({ path: testInfo.outputPath("project-detail-mobile.png"), fullPage: true });
  await page.setViewportSize({ width: 320, height: 700 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
  await expect(page.getByRole("link", { name: "Product home" })).toBeVisible();
});
