import type { LivePoint } from "../../lib/live-store";
import { TimeSeries, type ChartRange } from "./TimeSeries";

export function ErrorRateChart({ points, range }: { points: LivePoint[]; range: ChartRange }) {
  return <TimeSeries kind="errors" points={points} range={range} />;
}
