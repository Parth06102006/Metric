import type { Anomaly } from "../../types/api";

function key(anomaly: Anomaly): string {
  return `${anomaly.id ?? anomaly.detected_at}:${anomaly.type}`;
}

function isFinal(anomaly: Anomaly): boolean {
  return ["completed", "failed", "skipped", "disabled"].includes(anomaly.analysis_status ?? "");
}

// A fast analysis can be published before the original pending event. Never let
// a late pending event replace a final result for the same incident.
export function mergeAnomalies(live: Anomaly[], saved: Anomaly[]): Anomaly[] {
  const byKey = new Map<string, Anomaly>();
  for (const anomaly of [...live, ...saved]) {
    const existing = byKey.get(key(anomaly));
    if (!existing || (!isFinal(existing) && isFinal(anomaly))) {
      byKey.set(key(anomaly), anomaly);
    }
  }
  return [...byKey.values()]
    .sort((a, b) => new Date(b.detected_at).getTime() - new Date(a.detected_at).getTime())
    .slice(0, 50);
}
