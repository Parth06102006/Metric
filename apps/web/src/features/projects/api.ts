import {
  zAssignProjectOwnerBody, zAssignProjectOwnerResponse, zCreateProjectBody,
  zCreateProjectResponse, zGetProjectResponse, zListAnomaliesResponse,
  zListProjectsResponse, zRotateProjectKeyResponse, zListKnownUsersResponse,
  zListProjectViewersResponse, zViewerRequest,
} from "../../generated/api/zod.gen";
import { request, requestNoContent } from "../../lib/api";

export function listProjects() {
  return request("/api/projects", zListProjectsResponse);
}

export function getProject(projectId: string) {
  return request(`/api/projects/${encodeURIComponent(projectId)}`, zGetProjectResponse);
}

export function createProject(name: string) {
  return request("/api/projects", zCreateProjectResponse, {
    method: "POST",
    body: JSON.stringify(zCreateProjectBody.parse({ name })),
  });
}

export function assignOwner(projectId: string, ownerEmail: string) {
  return request(`/api/projects/${encodeURIComponent(projectId)}`, zAssignProjectOwnerResponse, {
    method: "PATCH",
    body: JSON.stringify(zAssignProjectOwnerBody.parse({ owner_email: ownerEmail })),
  });
}

export function listKnownUsers() {
  return request("/api/users", zListKnownUsersResponse);
}

export function listViewers(projectId: string) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/viewers`, zListProjectViewersResponse);
}

export function changeViewer(projectId: string, email: string, method: "POST" | "DELETE") {
  return requestNoContent(`/api/projects/${encodeURIComponent(projectId)}/viewers`, {
    method, body: JSON.stringify(zViewerRequest.parse({ email })),
  });
}

export function rotateApiKey(projectId: string) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/api-key`, zRotateProjectKeyResponse, {
    method: "POST",
  });
}

export function revokeApiKey(projectId: string) {
  return requestNoContent(`/api/projects/${encodeURIComponent(projectId)}/api-key`, {
    method: "DELETE",
  });
}

export function deleteProject(projectId: string) {
  return requestNoContent(`/api/projects/${encodeURIComponent(projectId)}`, {
    method: "DELETE",
  });
}

export function listAnomalies(projectId: string, limit = 100) {
  return request(
    `/api/projects/${encodeURIComponent(projectId)}/anomalies?limit=${limit}`,
    zListAnomaliesResponse,
  );
}
