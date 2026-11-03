import { useEffect, useMemo, useRef } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";

import type { LivePoint } from "../../lib/live-store";
import { cn } from "../../lib/cn";

export type ChartKind = "traffic" | "latency" | "errors";
export type ChartRange = { from: number; to: number };

type Props = {
  points: LivePoint[];
  kind: ChartKind;
  range: ChartRange;
  height?: number;
  className?: string;
};

type SeriesDef = { label: string; color: string; fill?: string; dash?: number[]; value: (point: LivePoint) => number | null };
const series: Record<ChartKind, SeriesDef[]> = {
  traffic: [{ label: "Requests / sec", color: "#6ee7b7", fill: "rgba(110,231,183,0.09)", value: (p) => p.rps }],
  latency: [
    { label: "P50", color: "#7dd3fc", value: (p) => p.rps > 0 ? p.p50 : null },
    { label: "P95", color: "#fcd34d", dash: [7, 4], value: (p) => p.rps > 0 ? p.p95 : null },
    { label: "P99", color: "#c4b5fd", dash: [2, 4], value: (p) => p.rps > 0 ? p.p99 : null },
  ],
  errors: [{ label: "5xx error rate", color: "#fda4af", fill: "rgba(253,164,175,0.09)", value: (p) => p.rps > 0 ? p.errorRate : null }],
};
const labels: Record<ChartKind, string> = { traffic: "Request rate", latency: "Latency percentiles", errors: "Error rate" };
const units: Record<ChartKind, string> = { traffic: "req/s", latency: "ms", errors: "%" };

type ChartRow = { t: number; point: LivePoint | null };

/** Insert an explicit null between distant samples; absence is not zero traffic. */
export function chartRows(points: LivePoint[]): ChartRow[] {
  const rows: ChartRow[] = [];
  for (const point of points) {
    const prev = rows.at(-1)?.point;
    if (prev && point.t - prev.t > Math.max(60_000, 2_000 * Math.max(prev.windowSeconds, point.windowSeconds))) {
      rows.push({ t: prev.t + (point.t - prev.t) / 2, point: null });
    }
    rows.push({ t: point.t, point });
  }
  return rows;
}

function sampleTime(t: number): string {
  return new Date(t).toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit", timeZoneName: "short" });
}

function valueText(value: number | null, kind: ChartKind): string {
  if (value === null) return "No requests";
  return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: kind === "latency" ? 0 : 2 }).format(value)} ${units[kind]}`;
}

function options(kind: ChartKind, width: number, height: number, onCursor: (plot: uPlot) => void): uPlot.Options {
  const defs = series[kind];
  return {
    width, height,
    padding: [12, 12, 0, 0],
    hooks: { setCursor: [onCursor] },
    cursor: { y: false, drag: { x: false, y: false }, points: { size: 8, width: 2 } },
    legend: { show: false },
    scales: {
      x: { time: true },
      y: { range: (_u, _min, max) => [0, kind === "errors" ? Math.min(100, Math.max(5, Number.isFinite(max) ? max * 1.2 : 5)) : Math.max(kind === "traffic" ? 1 : 10, Number.isFinite(max) ? max * 1.2 : 1)] },
    },
    axes: [
      { stroke: "#aeb8c5", grid: { stroke: "rgba(255,255,255,0.035)" }, ticks: { stroke: "rgba(255,255,255,0.1)" }, space: 70,
        values: (_u, splits) => splits.map((value) => new Date(value * 1000).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false })) },
      { stroke: "#aeb8c5", grid: { stroke: "rgba(255,255,255,0.07)" }, ticks: { show: false }, size: 56,
        values: (_u, splits) => splits.map((value) => `${Number(value.toFixed(2))}${kind === "errors" ? "%" : ""}`) },
    ],
    series: [{}, ...defs.map((def) => ({
      label: def.label, stroke: def.color, fill: def.fill, dash: def.dash, width: 2,
      points: { show: (u: uPlot) => u.data[0].length < 45, size: 5 },
    }))],
  };
}

/** A single aligned time scale per panel; gaps and no-request percentiles stay null. */
export function TimeSeries({ points, kind, range, height = 230, className }: Props) {
  const hostRef = useRef<HTMLDivElement>(null);
  const tooltipRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);
  const rows = useMemo(() => chartRows(points), [points]);
  const rowsRef = useRef(rows);
  rowsRef.current = rows;
  const defs = series[kind];

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const plot = new uPlot(options(kind, host.clientWidth || 640, height, (u) => {
      const tooltip = tooltipRef.current;
      if (!tooltip) return;
      const idx = u.cursor.idx;
      const row = idx == null ? undefined : rowsRef.current[idx];
      if (!row || u.cursor.left == null || u.cursor.left < 0 || u.cursor.top == null) { tooltip.hidden = true; return; }
      const pointerTime = u.posToVal(u.cursor.left, "x") * 1000;
      const point = row.point;
      if (!point || Math.abs(pointerTime - point.t) > Math.max(60_000, point.windowSeconds * 1000)) {
        tooltip.textContent = `No nearby sample · ${sampleTime(pointerTime)}`;
      } else {
        tooltip.textContent = `${sampleTime(point.t)} · ${defs.map((def) => `${def.label}: ${valueText(def.value(point), kind)}`).join(" · ")} · ${point.windowSeconds}s rolling window`;
      }
      tooltip.hidden = false;
      tooltip.style.left = `${Math.max(0, Math.min(u.over.offsetLeft + u.cursor.left + 12, host.clientWidth - 230))}px`;
      tooltip.style.top = `${Math.max(0, u.over.offsetTop + u.cursor.top - 16)}px`;
    }), [[], ...defs.map(() => [])] as uPlot.AlignedData, host);
    plotRef.current = plot;
    const observer = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (entry) plot.setSize({ width: Math.max(240, entry.contentRect.width), height });
    });
    observer.observe(host);
    return () => { observer.disconnect(); plot.destroy(); plotRef.current = null; };
  }, [kind, height, defs]);

  useEffect(() => {
    const plot = plotRef.current;
    if (!plot) return;
    plot.setData([rows.map((row) => row.t / 1000), ...defs.map((def) => rows.map((row) => row.point ? def.value(row.point) : null))] as uPlot.AlignedData);
    plot.setScale("x", { min: range.from / 1000, max: range.to / 1000 });
  }, [rows, defs, range.from, range.to]);

  return (
    <div className={cn("min-w-0", className)}>
      <div className="mb-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted" aria-label={`${labels[kind]} legend`}>
        {defs.map((def) => <span key={def.label} className="inline-flex items-center gap-1.5"><span aria-hidden className="w-4 border-t-2" style={{ borderColor: def.color, borderStyle: def.dash ? (def.dash[0] === 2 ? "dotted" : "dashed") : "solid" }} />{def.label} <span className="text-muted/80">({units[kind]})</span></span>)}
      </div>
      <div className="relative">
        <div ref={hostRef} role="img" aria-label={`${labels[kind]} across ${points.length} ${points.length === 1 ? "sample" : "samples"}`} />
        <div ref={tooltipRef} hidden className="pointer-events-none absolute z-10 max-w-56 rounded border border-line bg-raised p-2 text-xs leading-relaxed text-ink shadow-lg" />
        {points.length === 0 && <div className="pointer-events-none absolute inset-0 grid place-items-center text-sm text-muted">No samples in this time range</div>}
      </div>
      {points.length > 0 && <details className="mt-2 text-xs text-muted">
        <summary className="cursor-pointer hover:text-ink">View recent samples</summary>
        <div className="mt-2 overflow-x-auto"><table className="w-full text-left tabular"><caption className="sr-only">Most recent samples for {labels[kind]}</caption><thead><tr><th className="py-1 pr-4">Sample time</th>{defs.map((def) => <th key={def.label} className="py-1 pr-4">{def.label}</th>)}</tr></thead><tbody>{points.slice(-10).reverse().map((point, index) => <tr key={`${point.t}-${index}`}><td className="py-1 pr-4 whitespace-nowrap">{sampleTime(point.t)}</td>{defs.map((def) => <td key={def.label} className="py-1 pr-4 whitespace-nowrap">{valueText(def.value(point), kind)}</td>)}</tr>)}</tbody></table></div>
      </details>}
    </div>
  );
}
