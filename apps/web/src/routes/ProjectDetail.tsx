import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft } from "lucide-react";
import { useEffect, useMemo, useState, type ChangeEvent } from "react";
import { Link, useParams } from "react-router-dom";

import { TimeSeries } from "../components/charts/TimeSeries";
import { Badge, Button, Card, ErrorNote, Input, Spinner, Stat } from "../components/ui";
import { LiveConnection, type ConnectionStatus } from "../features/live/connection";
import { getProject, listAnomalies } from "../features/projects/api";
import { getThresholds, updateThresholds } from "../features/thresholds/api";
import { cn } from "../lib/cn";
import { formatNumber, formatPercent, formatRelative } from "../lib/format";
import { createLiveStore, useLiveStore } from "../lib/live-store";
import type { Anomaly, Threshold } from "../types/api";

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback;
}

function severityTone(severity: string): "neutral" | "warn" | "danger" {
  if (severity === "critical") return "danger";
  if (severity === "high") return "warn";
  return "neutral";
}

function NumberField({
  id,
  label,
  hint,
  value,
  onChange,
}: {
  id: string;
  label: string;
  hint?: string;
  value: number;
  onChange: (event: ChangeEvent<HTMLInputElement>) => void;
}) {
  return (
    <div>
      <label htmlFor={id} className="text-xs font-medium text-muted">
        {label}
      </label>
      <Input
        id={id}
        type="number"
        min={0}
        step="any"
        value={value}
        onChange={onChange}
        className="mt-1 font-mono"
      />
      {hint && <p className="mt-1 text-[11px] text-muted">{hint}</p>}
    </div>
  );
}

export function ProjectDetail() {
  const { projectId } = useParams<{ projectId: string }>();
  const queryClient = useQueryClient();

  const projectQuery = useQuery({
    queryKey: ["project", projectId],
    queryFn: () => getProject(projectId!),
    enabled: Boolean(projectId),
  });

  const thresholdsQuery = useQuery({
    queryKey: ["thresholds", projectId],
    queryFn: () => getThresholds(projectId!),
    enabled: Boolean(projectId),
  });

  const anomaliesQuery = useQuery({
    queryKey: ["anomalies", projectId],
    queryFn: () => listAnomalies(projectId!),
    enabled: Boolean(projectId),
  });

  const store = useMemo(() => createLiveStore(), [projectId]);
  const live = useLiveStore(store);

  const [status, setStatus] = useState<ConnectionStatus>("connecting");
  const [liveAnomalies, setLiveAnomalies] = useState<Anomaly[]>([]);
  const [draft, setDraft] = useState<Threshold | null>(null);

  useEffect(() => {
    if (thresholdsQuery.data) setDraft(thresholdsQuery.data);
  }, [thresholdsQuery.data]);

  useEffect(() => {
    if (!projectId) return;

    store.reset();
    setLiveAnomalies([]);

    const connection = new LiveConnection(projectId, {
      onSnapshot: (snapshot) => store.push(snapshot),
      onAnomaly: (anomaly) =>
        setLiveAnomalies((current) => [anomaly, ...current].slice(0, 50)),
      onStatus: setStatus,
    });

    connection.connect();
    return () => connection.close();
  }, [projectId, store]);

  const saveThresholds = useMutation({
    mutationFn: (next: Threshold) => updateThresholds(projectId!, next),
    onSuccess: (saved) => {
      setDraft(saved);
      void queryClient.invalidateQueries({ queryKey: ["thresholds", projectId] });
    },
  });

  const mergedAnomalies = useMemo(() => {
    const seen = new Set<string>();
    const merged = [...liveAnomalies, ...(anomaliesQuery.data ?? [])];

    return merged
      .filter((anomaly) => {
        const key = `${anomaly.id ?? anomaly.detected_at}:${anomaly.type}`;
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
      })
      .sort(
        (left, right) =>
          new Date(right.detected_at).getTime() - new Date(left.detected_at).getTime(),
      )
      .slice(0, 50);
  }, [liveAnomalies, anomaliesQuery.data]);

  if (!projectId) {
    return <ErrorNote message="Missing project id." />;
  }

  if (projectQuery.isLoading) {
    return <Spinner label="Loading project" />;
  }

  if (projectQuery.isError) {
    return <ErrorNote message={errorMessage(projectQuery.error, "Could not load project")} />;
  }

  const latest = live.points.at(-1);

  const setField =
    (key: keyof Threshold) =>
    (event: ChangeEvent<HTMLInputElement>) => {
      const parsed = Number.parseFloat(event.target.value);
      setDraft((current) =>
        current ? { ...current, [key]: Number.isFinite(parsed) ? parsed : 0 } : current,
      );
    };

  const statusLabel =
    status === "open" ? "Live" : status === "connecting" ? "Connecting" : "Reconnecting";

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <Link
            to="/"
            className="inline-flex items-center gap-1 text-xs text-muted transition-colors hover:text-ink"
          >
            <ChevronLeft className="size-3.5" aria-hidden />
            All projects
          </Link>
          <h1 className="mt-2 text-2xl font-semibold tracking-tight">
            {projectQuery.data?.name ?? "Project"}
          </h1>
          <p className="mt-1 font-mono text-xs text-muted">{projectId}</p>
        </div>

        <span className="inline-flex items-center gap-2 text-xs text-muted" aria-live="polite">
          <span
            className={cn(
              "size-1.5 rounded-full",
              status === "open" ? "bg-ok" : status === "connecting" ? "bg-warn" : "bg-danger",
            )}
          />
          {statusLabel}
        </span>
      </div>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="Request rate" value={latest ? `${formatNumber(latest.rps, 2)} RPS` : "—"} />
        <Stat label="P95 latency" value={latest ? `${formatNumber(latest.p95, 0)} ms` : "—"} hint="95th percentile" />
        <Stat label="Error rate" value={latest ? formatPercent(latest.errorRate, 2) : "—"} hint="Server error share" />
        <Stat label="Unique IPs" value={latest ? formatNumber(latest.uniqueIps, 0) : "—"} />
      </div>

      <Card className="p-4">
        <div className="mb-3 flex flex-wrap items-end justify-between gap-2">
          <h2 className="text-sm font-medium">Live traffic</h2>
          <span className="font-mono text-[11px] text-muted">
            {live.points.length} {live.points.length === 1 ? "sample" : "samples"}
          </span>
        </div>
        <TimeSeries points={live.points} />
      </Card>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.15fr)]">
        <Card className="p-4">
          <h2 className="text-sm font-medium">Alert thresholds</h2>
          <p className="mt-0.5 text-xs text-muted">A value of 0 disables that rule.</p>

          {thresholdsQuery.isLoading && (
            <div className="mt-4">
              <Spinner label="Loading thresholds" />
            </div>
          )}

          {thresholdsQuery.isError && (
            <div className="mt-4">
              <ErrorNote message={errorMessage(thresholdsQuery.error, "Could not load thresholds")} />
            </div>
          )}

          {draft && (
            <form
              className="mt-4 space-y-4"
              onSubmit={(event) => {
                event.preventDefault();
                saveThresholds.mutate(draft);
              }}
            >
              <div className="grid gap-3 sm:grid-cols-2">
                <NumberField
                  id="p95_ms"
                  label="P95 latency (ms)"
                  value={draft.p95_ms}
                  onChange={setField("p95_ms")}
                />
                <NumberField
                  id="error_rate"
                  label="Error rate (%)"
                  value={draft.error_rate}
                  onChange={setField("error_rate")}
                />
                <NumberField
                  id="rps_max"
                  label="Max RPS"
                  value={draft.rps_max}
                  onChange={setField("rps_max")}
                />
                <NumberField
                  id="req_per_ip_max"
                  label="Max RPS per IP"
                  value={draft.req_per_ip_max}
                  onChange={setField("req_per_ip_max")}
                />
              </div>

              {saveThresholds.isError && (
                <ErrorNote
                  message={errorMessage(saveThresholds.error, "Could not save thresholds")}
                />
              )}

              <div className="flex items-center gap-3">
                <Button type="submit" disabled={saveThresholds.isPending}>
                  {saveThresholds.isPending ? "Saving…" : "Save thresholds"}
                </Button>
                {saveThresholds.isSuccess && <span className="text-xs text-ok">Saved</span>}
              </div>
            </form>
          )}
        </Card>

        <Card className="p-4">
          <div className="flex items-center justify-between gap-3">
            <h2 className="text-sm font-medium">Anomalies</h2>
            <Badge tone={mergedAnomalies.length > 0 ? "warn" : "neutral"}>
              {mergedAnomalies.length}
            </Badge>
          </div>

          {anomaliesQuery.isLoading && (
            <div className="mt-4">
              <Spinner label="Loading anomalies" />
            </div>
          )}

          {!anomaliesQuery.isLoading && mergedAnomalies.length === 0 && (
            <p className="mt-4 text-sm text-muted">No anomalies detected.</p>
          )}

          {mergedAnomalies.length > 0 && (
            <ul
              className="mt-4 max-h-[26rem] space-y-3 overflow-y-auto pr-1"
              aria-live="polite"
              aria-label="Anomaly history"
              tabIndex={0}
            >
              {mergedAnomalies.map((anomaly) => (
                <li
                  key={`${anomaly.id ?? anomaly.detected_at}-${anomaly.type}`}
                  className="border-t border-line pt-3 first:border-0 first:pt-0"
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge tone={severityTone(anomaly.severity)}>{anomaly.severity}</Badge>
                      <span className="font-mono text-[11px] text-muted">{anomaly.type}</span>
                    </div>
                    <time
                      className="shrink-0 text-[11px] text-muted"
                      dateTime={anomaly.detected_at}
                    >
                      {formatRelative(anomaly.detected_at)}
                    </time>
                  </div>
                  <p className="mt-1.5 text-sm">{anomaly.message}</p>
                  {anomaly.analysis && (
                    <p className="mt-1 text-xs leading-relaxed text-muted">{anomaly.analysis}</p>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>
    </div>
  );
}
