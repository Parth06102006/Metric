import { useSyncExternalStore } from "react";

import type { Snapshot } from "../types/api";

export type LivePoint = {
  t: number;
  rps: number;
  p50: number;
  p95: number;
  p99: number;
  errorRate: number;
  uniqueIps: number;
  windowSeconds: number;
};

export type LiveState = {
  version: number;
  points: LivePoint[];
};

export const LIVE_STORE_CAPACITY = 900;

function toPoint(snapshot: Snapshot): LivePoint {
  return {
    // SQLite persists milliseconds; Date normalizes the live timestamp to the same precision.
    t: new Date(snapshot.ts).getTime(),
    rps: snapshot.rps,
    p50: snapshot.p50,
    p95: snapshot.p95,
    p99: snapshot.p99,
    errorRate: snapshot.error_rate,
    uniqueIps: snapshot.unique_ips,
    windowSeconds: snapshot.window_s,
  };
}

export function createLiveStore(capacity = LIVE_STORE_CAPACITY) {
  let state: LiveState = { version: 0, points: [] };
  const listeners = new Set<() => void>();
  let frame: number | null = null;

  const emit = () => {
    if (frame !== null) return;
    frame = requestAnimationFrame(() => {
      frame = null;
      for (const listener of listeners) listener();
    });
  };

  const merge = (snapshots: Snapshot[], history = false) => {
    // Identical projected points represent the same sample even if the wire
    // timestamp has more precision than the persisted timestamp. Keep distinct
    // samples with equal timestamps if their plotted values differ.
    const unique = new Map<string, LivePoint>();
    const incoming = snapshots.map(toPoint);
    // On a timestamp tie, late history must not supersede current live data.
    const combined = history ? [...incoming, ...state.points] : [...state.points, ...incoming];
    for (const point of combined) {
      const key = JSON.stringify([point.t, point.rps, point.p50, point.p95, point.p99, point.errorRate, point.uniqueIps, point.windowSeconds]);
      unique.delete(key);
      unique.set(key, point);
    }
    const points = [...unique.values()].sort((a, b) => a.t - b.t).slice(-capacity);
    state = { version: state.version + 1, points };
    emit();
  };

  return {
    push(snapshot: Snapshot) {
      merge([snapshot]);
    },

    mergeHistory(snapshots: Snapshot[]) {
      merge(snapshots, true);
    },

    reset() {
      state = { version: state.version + 1, points: [] };
      emit();
    },

    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },

    getSnapshot() {
      return state;
    },
  };
}

export type LiveStore = ReturnType<typeof createLiveStore>;

export function useLiveStore(store: LiveStore): LiveState {
  return useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot);
}
