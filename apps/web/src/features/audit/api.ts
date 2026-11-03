import { zListAuditResponse } from "../../generated/api/zod.gen";
import { request } from "../../lib/api";

/** Page size for the audit feed; the API caps a single read at 1000. */
export const AUDIT_PAGE_SIZE = 100;

export type AuditFilters = {
  action?: string;
  projectId?: string;
  /** Exclusive cursor: timestamp and id of the oldest event on screen. */
  before?: string;
  beforeId?: number;
  limit?: number;
};

/** Builds the query string the audit endpoint expects. Pure, so it is tested. */
export function buildAuditQuery(filters: AuditFilters = {}): string {
  const params = new URLSearchParams();
  if (filters.action) params.set("action", filters.action);
  if (filters.projectId) params.set("project_id", filters.projectId);
  if (filters.before) params.set("before", filters.before);
  if (filters.before !== undefined && filters.beforeId !== undefined) {
    params.set("before_id", String(filters.beforeId));
  }
  params.set("limit", String(filters.limit ?? AUDIT_PAGE_SIZE));
  return params.toString();
}

export function listAudit(filters: AuditFilters = {}) {
  return request(`/api/audit?${buildAuditQuery(filters)}`, zListAuditResponse);
}

/** Known audit actions, in the order they appear in the filter menu. */
export const AUDIT_ACTIONS: ReadonlyArray<{ value: string; label: string }> = [
  { value: "project.create", label: "Project created" },
  { value: "project.owner.assign", label: "Owner assigned" },
  { value: "project.viewer.add", label: "Viewer added" },
  { value: "project.viewer.remove", label: "Viewer removed" },
  { value: "project.delete", label: "Project deleted" },
  { value: "api_key.rotate", label: "API key rotated" },
  { value: "api_key.revoke", label: "API key revoked" },
  { value: "threshold.update", label: "Thresholds updated" },
  { value: "auth.denied", label: "Sign-in denied" },
  { value: "admin.denied", label: "Admin action denied" },
  { value: "ingest.denied", label: "Ingest denied" },
  { value: "access.denied", label: "Project access denied" },
];

export function auditActionLabel(action: string): string {
  return AUDIT_ACTIONS.find((entry) => entry.value === action)?.label ?? action;
}
