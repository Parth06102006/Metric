import { zGetMetricHistoryResponse, zGetMetricsResponse } from "../../generated/api/zod.gen";
import { request } from "../../lib/api";

export function getMetrics(projectId: string) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/metrics`, zGetMetricsResponse);
}

export function getMetricHistory(projectId: string, signal?: AbortSignal) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/metrics/history`, zGetMetricHistoryResponse, { signal });
}
