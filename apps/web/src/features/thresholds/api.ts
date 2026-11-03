import { zGetThresholdsResponse, zUpdateThresholdsBody, zUpdateThresholdsResponse } from "../../generated/api/zod.gen";
import { request } from "../../lib/api";
import type { Threshold } from "../../types/api";

export function getThresholds(projectId: string) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/thresholds`, zGetThresholdsResponse);
}

export function updateThresholds(projectId: string, thresholds: Threshold) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/thresholds`, zUpdateThresholdsResponse, {
    method: "PUT",
    body: JSON.stringify(zUpdateThresholdsBody.parse(thresholds)),
  });
}
