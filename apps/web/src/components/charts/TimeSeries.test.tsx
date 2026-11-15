import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { chartRows, TimeSeries } from "./TimeSeries";
import type { LivePoint } from "../../lib/live-store";

const mocks = vi.hoisted(() => ({
  create: vi.fn(), setData: vi.fn(), setScale: vi.fn(), setSize: vi.fn(), destroy: vi.fn(),
  observe: vi.fn(), disconnect: vi.fn(), resize: null as ResizeObserverCallback | null,
}));
vi.mock("uplot", () => ({
  default: class {
    constructor(...args: unknown[]) { mocks.create(...args); }
    setData(data: unknown) { mocks.setData(data); }
    setScale(...args: unknown[]) { mocks.setScale(...args); }
    setSize(size: unknown) { mocks.setSize(size); }
    destroy() { mocks.destroy(); }
  },
}));

const point = (t: number, rps = 3.5): LivePoint => ({ t, rps, p50: 50, p95: 90, p99: 120, errorRate: 5, uniqueIps: 2, windowSeconds: 60 });
const range = { from: 1699999100000, to: 1700000001000 };

beforeEach(() => {
  vi.resetAllMocks();
  mocks.resize = null;
  vi.stubGlobal("ResizeObserver", class {
    constructor(callback: ResizeObserverCallback) { mocks.resize = callback; }
    observe() { mocks.observe(); }
    unobserve() {}
    disconnect() { mocks.disconnect(); }
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("TimeSeries", () => {
  it("shows an empty state, uses the selected wall-clock range, resizes, and cleans up", () => {
    const { rerender, unmount } = render(<TimeSeries kind="traffic" points={[]} range={range} />);
    expect(screen.getByRole("img", { name: "Request rate across 0 samples" })).toBeInTheDocument();
    expect(screen.getByText("No samples in this time range")).toBeInTheDocument();
    expect(mocks.setScale).toHaveBeenLastCalledWith("x", { min: range.from / 1000, max: range.to / 1000 });
    rerender(<TimeSeries kind="traffic" points={[point(1700000000123), point(1700000000456, 4)]} range={range} />);
    expect(screen.getByRole("img", { name: "Request rate across 2 samples" })).toBeInTheDocument();
    expect(mocks.setData).toHaveBeenLastCalledWith([[1700000000.123, 1700000000.456], [3.5, 4]]);
    expect(screen.getByText("View recent samples")).toBeInTheDocument();
    mocks.resize?.([{ contentRect: { width: 100 } } as ResizeObserverEntry], {} as ResizeObserver);
    expect(mocks.setSize).toHaveBeenCalledWith({ width: 240, height: 230 });
    unmount();
    expect(mocks.disconnect).toHaveBeenCalledTimes(1);
    expect(mocks.destroy).toHaveBeenCalledTimes(1);
  });

  it("breaks the line across missing observations instead of interpolating", () => {
    const first = point(1000), second = point(122000);
    expect(chartRows([first, second])).toEqual([{ t: 1000, point: first }, { t: 61500, point: null }, { t: 122000, point: second }]);
    render(<TimeSeries kind="traffic" points={[first, second]} range={range} />);
    expect(mocks.setData).toHaveBeenLastCalledWith([[1, 61.5, 122], [3.5, null, 3.5]]);
  });

  it("plots true percentile observations and no latency or error rate when no requests were observed", () => {
    const points = [point(1000), point(6000, 0)];
    const { unmount } = render(<TimeSeries kind="latency" points={points} range={range} />);
    expect(mocks.setData).toHaveBeenLastCalledWith([[1, 6], [50, null], [90, null], [120, null]]);
    unmount();
    render(<TimeSeries kind="errors" points={points} range={range} />);
    expect(mocks.setData).toHaveBeenLastCalledWith([[1, 6], [5, null]]);
    expect(screen.getByText("No requests")).toBeInTheDocument();
  });
});
