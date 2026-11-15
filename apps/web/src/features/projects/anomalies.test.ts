import { describe, expect, it } from "vitest";
import { anomalySchema, envelopeSchema, type Anomaly } from "../../types/api";
import { mergeAnomalies } from "./anomalies";

const pending: Anomaly = {
  id: 42, type: "high_latency", severity: "high", message: "slow",
  value: 200, threshold: 100, analysis_status: "pending", detected_at: "2026-01-01T00:00:00Z",
};
const completed: Anomaly = {
  ...pending, analysis_status: "completed", diagnosis: {
    pattern: "latency_degradation", direction: "latency", urgency: 1,
    pattern_confidence: 0.8, direction_confidence: 0.8, urgency_confidence: 0.7,
    evidence_probability: 0.9, model: "jev-test", input_tokens: 100, evidence_sufficient: true,
  },
};

describe("incident analysis", () => {
  it("preserves diagnosis fields from REST and WebSocket responses", () => {
    expect(anomalySchema.parse(completed).diagnosis).toEqual(completed.diagnosis);
    const event = envelopeSchema.parse({ event_type: "anomaly_alert", project_id: "p", timestamp: pending.detected_at, data: completed });
    expect(event.event_type === "anomaly_alert" && event.data.diagnosis).toEqual(completed.diagnosis);
  });

  it("does not regress a completed incident to a late pending event", () => {
    expect(mergeAnomalies([pending, completed], [])).toEqual([completed]);
    expect(mergeAnomalies([completed], [pending])).toEqual([completed]);
    expect(mergeAnomalies([pending], [completed])).toEqual([completed]);
  });

  it("accepts older incidents with no analysis metadata", () => {
    const { analysis_status: _status, ...legacy } = pending;
    expect(anomalySchema.safeParse(legacy).success).toBe(true);
  });
});
