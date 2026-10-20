import { request } from "../../lib/api";
import { snapshotSchema } from "../../types/api";

export function getMetrics(projectId: string) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/metrics`, snapshotSchema);
}
