import { z } from "zod";

export const projectSchema = z.object({
  project_id: z.string(),
  name: z.string(),
});

export const createdProjectSchema = projectSchema.extend({
  api_key: z.string(),
});

export const thresholdSchema = z.object({
  p95_ms: z.number(),
  error_rate: z.number(),
  rps_max: z.number(),
  req_per_ip_max: z.number(),
});

export const snapshotSchema = z.object({
  ts: z.string(),
  window_s: z.number(),
  rps: z.number(),
  p50: z.number(),
  p95: z.number(),
  p99: z.number(),
  error_rate: z.number(),
  unique_ips: z.number(),
  top_ip_rps: z.number(),
  avg_payload_bytes: z.number(),
  endpoints: z.record(z.string(), z.number()),
});

export const anomalySchema = z.object({
  id: z.number().optional(),
  type: z.string(),
  severity: z.string(),
  message: z.string(),
  value: z.number(),
  threshold: z.number(),
  analysis: z.string().optional(),
  detected_at: z.string(),
});

export const envelopeSchema = z.discriminatedUnion("event_type", [
  z.object({
    event_type: z.literal("metric_snapshot"),
    project_id: z.string(),
    timestamp: z.string(),
    data: snapshotSchema,
  }),
  z.object({
    event_type: z.literal("anomaly_alert"),
    project_id: z.string(),
    timestamp: z.string(),
    data: anomalySchema,
  }),
]);

export const meSchema = z.object({
  email: z.string(),
  user: z.string().optional(),
  admin: z.boolean(),
});

export type Project = z.infer<typeof projectSchema>;
export type CreatedProject = z.infer<typeof createdProjectSchema>;
export type Threshold = z.infer<typeof thresholdSchema>;
export type Snapshot = z.infer<typeof snapshotSchema>;
export type Anomaly = z.infer<typeof anomalySchema>;
export type Me = z.infer<typeof meSchema>;
