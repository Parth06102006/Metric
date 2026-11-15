import { expect, test, type APIResponse } from "@playwright/test";
import type { z } from "zod";
import {
  zAssignProjectOwnerResponse, zCreateProjectResponse, zError, zGetHealthResponse,
  zGetMeResponse, zGetMetricsResponse, zGetProjectResponse, zGetThresholdsResponse,
  zIngestLogsResponse, zListAnomaliesResponse, zListAuditResponse, zListProjectsResponse,
  zRotateProjectKeyResponse, zUpdateThresholdsResponse,
} from "../src/generated/api/zod.gen";

async function validate<T>(response: APIResponse, status: number, schema: z.ZodType<T>): Promise<T> {
  expect(response.status()).toBe(status);
  return schema.parse(await response.json());
}

test("real HTTP responses conform to generated OpenAPI validators", async ({ request }) => {
  await validate(await request.get("http://127.0.0.1:18080/health"), 200, zGetHealthResponse);
  const me = await validate(await request.get("/api/auth/me"), 200, zGetMeResponse);
  expect(me.admin).toBe(true);
  const project = await validate(await request.post("/api/projects", { data: { name: `Contract ${Date.now()}` } }), 201, zCreateProjectResponse);
  const base = `/api/projects/${project.project_id}`;
  expect((await validate(await request.get("/api/projects"), 200, zListProjectsResponse)).some(p => p.project_id === project.project_id)).toBe(true);
  await validate(await request.get(base), 200, zGetProjectResponse);
  await validate(await request.get(`${base}/thresholds`), 200, zGetThresholdsResponse);
  await validate(await request.put(`${base}/thresholds`, { data: { p95_ms: 100, error_rate: 10, rps_max: 100, req_per_ip_max: 30 } }), 200, zUpdateThresholdsResponse);
  await validate(await request.get(`${base}/metrics`), 200, zGetMetricsResponse);
  await validate(await request.get(`${base}/anomalies`), 200, zListAnomaliesResponse);
  await validate(await request.get("/api/audit"), 200, zListAuditResponse);
  await validate(await request.patch(base, { data: { owner_email: "" } }), 200, zAssignProjectOwnerResponse);
  await validate(await request.post(`/ingest/${project.project_id}`, {
    headers: { "X-API-Key": "invalid" }, data: { logs: [] },
  }), 401, zError);
  await validate(await request.post(`/ingest/${project.project_id}`, {
    headers: { "X-API-Key": project.api_key },
    data: { logs: [{ ts: new Date().toISOString(), endpoint: "/contract", method: "GET", status: 200, ip: "192.0.2.1" }] },
  }), 202, zIngestLogsResponse);
  expect((await request.delete(`${base}/api-key`)).status()).toBe(204);
  await validate(await request.post(`${base}/api-key`), 200, zRotateProjectKeyResponse);
  expect((await request.delete(base)).status()).toBe(204);
  await validate(await request.get(base), 404, zError);
});
