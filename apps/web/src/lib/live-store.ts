import { useSyncExternalStore } from "react";

import type { Snapshot } from "../types/api";

export type LivePoint = {
  t: number;
  rps: number;
  p95: number;
  errorRate: number;
  uniqueIps: number;
};

export type LiveState = {
  version: number;
  points: LivePoint[];
};

export function createLiveStore(capacity = 900) {
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

  return {
    push(snapshot: Snapshot) {
      const point: LivePoint = {
        t: new Date(snapshot.ts).getTime(),
        rps: snapshot.rps,
        p95: snapshot.p95,
        errorRate: snapshot.error_rate,
        uniqueIps: snapshot.unique_ips,
      };

      const overflow = Math.max(0, state.points.length - capacity + 1);
      const points = state.points.slice(overflow);
      points.push(point);

      state = { version: state.version + 1, points };
      emit();
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
