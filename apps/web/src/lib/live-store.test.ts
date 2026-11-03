import { describe, expect, it } from "vitest";
import type { Snapshot } from "../types/api";
import { createLiveStore } from "./live-store";

function sample(ts: string, rps: number): Snapshot {
  return { ts, rps, window_s: 60, p50: 0, p95: rps, p99: 0, error_rate: 0,
    unique_ips: 1, top_ip_rps: 0, avg_payload_bytes: 0, endpoints: {} };
}

describe("live metric history", () => {
  it("merges late history chronologically, deduplicates persisted/live precision, and retains the newest capacity", () => {
    const store = createLiveStore(3);
    store.push(sample("2025-01-01T00:00:03.123456Z", 3));
    store.push(sample("2025-01-01T00:00:04Z", 4));
    store.mergeHistory([
      sample("2025-01-01T00:00:03.123Z", 3),
      sample("2025-01-01T00:00:01Z", 1),
      sample("2025-01-01T00:00:02Z", 2),
    ]);
    expect(store.getSnapshot().points.map((p) => p.rps)).toEqual([2, 3, 4]);
    store.push(sample("2025-01-01T00:00:02Z", 2));
    expect(store.getSnapshot().points.map((p) => p.rps)).toEqual([2, 3, 4]);
    store.mergeHistory([sample("2025-01-01T00:00:03.123Z", 30)]);
    expect(store.getSnapshot().points.map((p) => p.rps)).toEqual([30, 3, 4]);
    store.mergeHistory([sample("2025-01-01T00:00:04Z", 4), sample("2025-01-01T00:00:04Z", 40)]);
    expect(store.getSnapshot().points.at(-1)?.rps).toBe(4);
  });

  it("retains the original window and all percentile values, including distinct samples", () => {
    const store = createLiveStore();
    const first = { ...sample("2026-01-01T00:00:00Z", 2), p50: 10, p95: 50, p99: 100, window_s: 120 };
    store.push(first);
    store.push({ ...first, p99: 200 });
    expect(store.getSnapshot().points).toMatchObject([
      { p50: 10, p95: 50, p99: 100, windowSeconds: 120 },
      { p50: 10, p95: 50, p99: 200, windowSeconds: 120 },
    ]);
  });
});
