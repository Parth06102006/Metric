import { z } from "zod";

import { request } from "../../lib/api";
import {
  anomalySchema,
  createdProjectSchema,
  meSchema,
  projectSchema,
} from "../../types/api";

export function listProjects() {
  return request("/api/projects", z.array(projectSchema));
}

export function getProject(projectId: string) {
  return request(`/api/projects/${encodeURIComponent(projectId)}`, projectSchema);
}

export function createProject(name: string) {
  return request("/api/projects", createdProjectSchema, {
    method: "POST",
    body: JSON.stringify({ name }),
  });
}

export function getMe() {
  return request("/api/auth/me", meSchema);
}

export function listAnomalies(projectId: string, limit = 100) {
  return request(
    `/api/projects/${encodeURIComponent(projectId)}/anomalies?limit=${limit}`,
    z.array(anomalySchema),
  );
}
