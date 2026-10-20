import { request } from "../../lib/api";
import { thresholdSchema, type Threshold } from "../../types/api";

export function getThresholds(projectId: string) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/thresholds`, thresholdSchema);
}

export function updateThresholds(projectId: string, thresholds: Threshold) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/thresholds`, thresholdSchema, {
    method: "PUT",
    body: JSON.stringify(thresholds),
  });
}
