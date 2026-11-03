// The schemas and wire types are generated from apps/api/openapi.yaml.
// Keep these aliases for the existing UI and WebSocket consumers.
export {
  zProject as projectSchema,
  zCreatedProject as createdProjectSchema,
  zThreshold as thresholdSchema,
  zSnapshot as snapshotSchema,
  zDiagnosis as diagnosisSchema,
  zAnomaly as anomalySchema,
  zLiveEvent as envelopeSchema,
  zMe as meSchema,
  zAuditEvent as auditEventSchema,
} from "../generated/api/zod.gen";

export type {
  Project,
  CreatedProject,
  Threshold,
  Snapshot,
  Anomaly,
  Me,
  AuditEvent,
} from "../generated/api/types.gen";
