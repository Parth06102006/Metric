import { useEffect, useRef } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";

import type { LivePoint } from "../../lib/live-store";
import { cn } from "../../lib/cn";

type Props = {
  points: LivePoint[];
  height?: number;
  className?: string;
};

const baseAxis: uPlot.Axis = {
  stroke: "#8b93a1",
  grid: { stroke: "rgba(255,255,255,0.06)" },
  ticks: { stroke: "rgba(255,255,255,0.08)" },
};

const timeAxis: uPlot.Axis = {
  ...baseAxis,
  side: 2,
  space: 70,
  // A live window only needs the clock. Dropping the date also avoids the
  // first tick crowding the next one with a two-line label. Include seconds
  // when uPlot picks a sub-minute tick interval, or labels repeat.
  values: (_u, splits) => {
    const step = splits.length > 1 ? Math.abs(splits[1] - splits[0]) : 60;
    return splits.map((value) =>
      new Date(value * 1000).toLocaleTimeString(undefined, {
        hour: "2-digit",
        minute: "2-digit",
        ...(step < 60 ? { second: "2-digit" as const } : {}),
        hour12: false,
      }),
    );
  },
};

function zeroBased(floor: number) {
  return (_u: uPlot, _min: number, max: number): [number, number] => [
    0,
    Math.max(floor, Number.isFinite(max) ? max * 1.25 : floor),
  ];
}

function buildOptions(width: number, height: number): uPlot.Options {
  return {
    width,
    height,
    padding: [12, 14, 0, 0],
    // The x range is fixed to a live window, so drag-to-zoom would fight it.
    cursor: { drag: { x: false, y: false } },
    legend: { show: true },
    scales: {
      x: {
        time: true,
        // uPlot's automatic range expands a single sample to a multi-year
        // span. Anchor the view to the newest sample and a recent window.
        range: (u) => {
          const xs = u.data[0] as number[];
          const fallback = Math.floor(Date.now() / 1000);
          const last = xs.length > 0 && Number.isFinite(xs[xs.length - 1])
            ? xs[xs.length - 1]
            : fallback;
          const first = xs.length > 0 && Number.isFinite(xs[0]) ? xs[0] : last;
          // Narrow screens get a shorter window so time ticks stay legible.
          const minimum = u.width < 520 ? 120 : 300;
          const span = Math.max(minimum, Math.min(3600, last - first + 30));
          return [last - span, last];
        },
      },
      rps: { range: zeroBased(10) },
      p95: { range: zeroBased(100) },
    },
    axes: [
      { ...timeAxis },
      { ...baseAxis, side: 3, scale: "rps" },
      { ...baseAxis, side: 1, scale: "p95", grid: { show: false } },
    ],
    series: [
      {},
      {
        label: "RPS",
        scale: "rps",
        stroke: "#4ade80",
        width: 2,
        fill: "rgba(74,222,128,0.08)",
        // Draw markers while the series is still short; hide them once the
        // line itself is dense enough to read.
        points: { show: (u) => u.data[0].length < 60, size: 4 },
      },
      {
        label: "P95 ms",
        scale: "p95",
        stroke: "#fbbf24",
        width: 2,
        points: { show: (u) => u.data[0].length < 60, size: 4 },
      },
    ],
  };
}

/**
 * uPlot owns its canvas. React only hands it aligned arrays; no per-sample
 * React render happens here.
 */
export function TimeSeries({ points, height = 300, className }: Props) {
  const hostRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;

    const plot = new uPlot(buildOptions(host.clientWidth || 640, height), [[], [], []], host);
    plotRef.current = plot;

    const observer = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (!entry) return;
      plot.setSize({ width: Math.max(240, entry.contentRect.width), height });
    });
    observer.observe(host);

    return () => {
      observer.disconnect();
      plot.destroy();
      plotRef.current = null;
    };
  }, [height]);

  useEffect(() => {
    const plot = plotRef.current;
    if (!plot) return;

    const data: uPlot.AlignedData = [
      points.map((point) => Math.floor(point.t / 1000)),
      points.map((point) => point.rps),
      points.map((point) => point.p95),
    ];
    plot.setData(data);
  }, [points]);

  return (
    <div className={cn("relative", className)}>
      <div
        ref={hostRef}
        role="img"
        aria-label={`Request rate and P95 latency across ${points.length} live samples`}
      />
      {points.length === 0 && (
        <div className="pointer-events-none absolute inset-0 grid place-items-center text-sm text-muted">
          Waiting for traffic…
        </div>
      )}
    </div>
  );
}
