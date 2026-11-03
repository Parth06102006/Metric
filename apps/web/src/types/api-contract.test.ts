import { afterEach, describe, expect, it, vi } from "vitest";

import { listProjects } from "../features/projects/api";
import { updateThresholds } from "../features/thresholds/api";
import { zAnomaly, zAuditEvent, zIngestLogsBody, zLiveEvent, zSnapshot, zThreshold } from "../generated/api/zod.gen";

const thresholds = { p95_ms: 1000, error_rate: 5, rps_max: 100, req_per_ip_max: 30 };

afterEach(() => vi.unstubAllGlobals());

describe("generated OpenAPI validators", () => {
  it("accepts valid threshold data and rejects impossible values", () => {
    expect(zThreshold.safeParse(thresholds).success).toBe(true);
    expect(zThreshold.safeParse({ ...thresholds, error_rate: 101 }).success).toBe(false);
    expect(zThreshold.safeParse({ ...thresholds, p95_ms: -1 }).success).toBe(false);
  });

  it("validates outgoing threshold bodies before fetch", () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    expect(() => updateThresholds("p", { ...thresholds, error_rate: 101 })).toThrow();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("rejects malformed server responses at the REST boundary", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      status: 200, ok: true, json: async () => [{ project_id: 123, name: "bad" }],
    }));
    await expect(listProjects()).rejects.toThrow();
  });

  it("validates offset timestamps and string-keyed records in generated Zod 4 schemas", () => {
    const snapshot = {
      ts: "2026-01-01T12:00:00+05:30", window_s: 60, rps: 1, p50: 10, p95: 20, p99: 30,
      error_rate: 5, unique_ips: 2, top_ip_rps: 0.1, avg_payload_bytes: 40,
      endpoints: { "/products": 3, "/orders": 2 },
    };
    expect(zSnapshot.parse(snapshot).endpoints).toEqual(snapshot.endpoints);
    expect(zSnapshot.safeParse({ ...snapshot, ts: "not-a-date" }).success).toBe(false);
    expect(zSnapshot.safeParse({ ...snapshot, endpoints: { "/products": "3" } }).success).toBe(false);
    const event = { id: 1, ts: snapshot.ts, actor_email: "admin@example.com", action: "project.create",
      target_project_id: "p", outcome: "success", request_id: "r", details: { source: "test" } };
    expect(zAuditEvent.parse(event).details).toEqual({ source: "test" });
    expect(zAuditEvent.safeParse({ ...event, details: { source: 4 } }).success).toBe(false);
  });

  it("checks ingest shape and accepts records with optional zero-valued fields omitted", () => {
    const record = { ts: "2026-01-01T00:00:00Z", endpoint: "/a", method: "GET", status: 200, ip: "192.0.2.1" };
    expect(zIngestLogsBody.safeParse({ logs: [record] }).success).toBe(true);
    expect(zIngestLogsBody.safeParse({ logs: [{ ...record, status: 42 }] }).success).toBe(false);
    expect(zIngestLogsBody.safeParse({ logs: [] }).success).toBe(false);
  });

  it("validates the real diagnosis evidence shape and live event discriminator", () => {
    const anomaly = {
      id: 1, type: "high_error_rate", severity: "critical", message: "error", value: 20,
      threshold: 5, detected_at: "2026-01-01T00:00:00Z",
      diagnosis: {
        pattern: "unclear", direction: "more_evidence", urgency: 0,
        pattern_confidence: 0.5, direction_confidence: 0.5, urgency_confidence: 0.5,
        evidence_probability: 0.5, model: "test", input_tokens: 1, evidence_sufficient: false,
        evidence: {
          trigger_type: "high_error_rate", severity: "critical", value: 20, threshold: 5,
          window_seconds: 60, request_count: 20, rps: 1, p50: 10, p95: 20, p99: 30,
          error_rate: 20, top_ip_rps: 0.1, unique_ips: 4,
        },
      },
    };
    expect(zAnomaly.parse(anomaly).diagnosis?.evidence?.request_count).toBe(20);
    const envelope = { event_type: "anomaly_alert", project_id: "p", timestamp: anomaly.detected_at, data: anomaly };
    expect(zLiveEvent.safeParse(envelope).success).toBe(true);
    expect(zLiveEvent.safeParse({ ...envelope, event_type: "unknown" }).success).toBe(false);
    expect(zLiveEvent.safeParse({ ...envelope, data: { ...anomaly, detected_at: "invalid" } }).success).toBe(false);
  });
});
