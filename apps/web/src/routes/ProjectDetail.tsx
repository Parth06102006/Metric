import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, Copy, KeyRound, Lightbulb, LightbulbOff } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type ChangeEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import { TimeSeries } from "../components/charts/TimeSeries";
import { ErrorRateChart } from "../components/charts/ErrorRateChart";
import { Badge, Button, Card, ErrorNote, Input, LoadingRows, Skeleton, Stat } from "../components/ui";
import { useMe } from "../features/identity/api";
import { LiveConnection, type ConnectionStatus } from "../features/live/connection";
import { getMetricHistory } from "../features/metrics/api";
import { mergeAnomalies } from "../features/projects/anomalies";
import {
  assignOwner,
  changeViewer,
  listKnownUsers,
  listViewers,
  deleteProject,
  getProject,
  listAnomalies,
  revokeApiKey,
  rotateApiKey,
} from "../features/projects/api";
import { getThresholds, updateThresholds } from "../features/thresholds/api";
import { cn } from "../lib/cn";
import { copyToClipboard } from "../lib/clipboard";
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
  const me = useMe();
  const isAdmin = me.data?.admin === true;

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
  const [historyError, setHistoryError] = useState<string | null>(null);
  const retryHistory = useRef<() => void>(() => undefined);
  const [liveAnomalies, setLiveAnomalies] = useState<Anomaly[]>([]);
  const [draft, setDraft] = useState<Threshold | null>(null);
  const [keyOpen, setKeyOpen] = useState(false);
  const [anomalyPage, setAnomalyPage] = useState(0);
  const [rangeMinutes, setRangeMinutes] = useState<15 | 60>(15);
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 5_000);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    if (thresholdsQuery.data) setDraft(thresholdsQuery.data);
  }, [thresholdsQuery.data]);

  useEffect(() => {
    if (!projectId) return;

    store.reset();
    setKeyOpen(false);
    setAnomalyPage(0);
    setLiveAnomalies([]);
    setHistoryError(null);
    let active = true;
    let controller: AbortController | undefined;
    const refreshHistory = () => {
      controller?.abort();
      const request = new AbortController();
      controller = request;
      void getMetricHistory(projectId, request.signal).then((snapshots) => {
        if (!active || request.signal.aborted) return;
        store.mergeHistory(snapshots);
        setHistoryError(null);
      }).catch((error: unknown) => {
        if (!active || request.signal.aborted) return;
        setHistoryError(errorMessage(error, "Could not load metric history"));
      });
    };
    retryHistory.current = refreshHistory;
    refreshHistory();

    const connection = new LiveConnection(projectId, {
      onSnapshot: (snapshot) => { if (active) { setNow(Date.now()); store.push(snapshot); } },
      onAnomaly: (anomaly) => {
        if (active) setLiveAnomalies((current) => mergeAnomalies([anomaly], current));
      },
      onStatus: (next) => {
        if (!active) return;
        setStatus(next);
        if (next === "open") {
          // WebSocket delivery is best-effort; reconcile anything missed offline.
          refreshHistory();
          setLiveAnomalies([]);
          void queryClient.invalidateQueries({ queryKey: ["anomalies", projectId] });
        }
      },
    });

    connection.connect();
    return () => {
      active = false;
      controller?.abort();
      retryHistory.current = () => undefined;
      connection.close();
    };
  }, [projectId, store, queryClient]);

  const saveThresholds = useMutation({
    mutationFn: (next: Threshold) => updateThresholds(projectId!, next),
    onSuccess: (saved) => {
      setDraft(saved);
      void queryClient.invalidateQueries({ queryKey: ["thresholds", projectId] });
    },
  });

  const mergedAnomalies = useMemo(() => {
    return mergeAnomalies(liveAnomalies, anomaliesQuery.data ?? []);
  }, [liveAnomalies, anomaliesQuery.data]);

  if (!projectId) {
    return <ErrorNote message="Missing project id." />;
  }

  if (projectQuery.isLoading) {
    return <div className="space-y-6" role="status" aria-label="Loading project"><span className="sr-only">Loading project</span><Skeleton className="h-6 w-28" /><Skeleton className="h-12 w-2/3" /><div className="grid grid-cols-2 gap-3 lg:grid-cols-4">{Array.from({length:4},(_,i)=><Skeleton key={i} className="h-28 rounded-xl" />)}</div><Skeleton className="h-72 rounded-xl" /></div>;
  }

  if (projectQuery.isError) {
    return <ErrorNote message={errorMessage(projectQuery.error, "Could not load project")} />;
  }

  const latest = live.points.at(-1);
  const range = { from: now - rangeMinutes * 60_000, to: now };
  const visiblePoints = live.points.filter((point) => point.t >= range.from && point.t <= range.to);
  const stale = latest && now - latest.t > Math.max(60_000, latest.windowSeconds * 2_000);
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "local time";
  const pageCount = Math.max(1, Math.ceil(mergedAnomalies.length / 5));
  const currentPage = Math.min(anomalyPage, pageCount - 1);
  const visibleAnomalies = mergedAnomalies.slice(currentPage * 5, currentPage * 5 + 5);

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
    <div className="space-y-6 sm:space-y-8">
      <div className="flex flex-wrap items-start justify-between gap-4 border-b border-line/70 pb-5 sm:pb-7">
        <div>
          <Link
            to="/dashboard"
            className="inline-flex items-center gap-1 text-xs text-muted transition-colors hover:text-ink"
          >
            <ChevronLeft className="size-3.5" aria-hidden />
            All projects
          </Link>
          <h1 className="mt-2 text-3xl font-semibold tracking-tight sm:text-4xl">
            {projectQuery.data?.name ?? "Project"}
          </h1>
          <p className="mt-2 break-all font-mono text-xs text-muted">{projectId}</p>
        </div>

        <div className="flex items-center gap-4">
          <span className="inline-flex items-center gap-2 text-xs text-muted" aria-live="polite">
            <span className={cn("size-1.5 rounded-full", status === "open" ? "bg-ok" : status === "connecting" ? "bg-warn" : "bg-danger")} />
            {statusLabel}
          </span>
          {isAdmin && <Button variant="ghost" onClick={() => setKeyOpen((open) => !open)} aria-expanded={keyOpen} aria-controls="key-management"><KeyRound className="size-4" aria-hidden /> API key</Button>}
        </div>
      </div>

      {isAdmin && keyOpen && <div id="key-management"><IngestKeyCard key={projectId} projectId={projectId} /></div>}

      <section aria-labelledby="metrics-heading" className="space-y-4">
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div><h2 id="metrics-heading" className="text-lg font-semibold tracking-tight">Metrics overview</h2>
            <p className="mt-1 text-xs text-muted">{latest ? `Latest sample: ${new Date(latest.t).toLocaleString()} · ${latest.windowSeconds}s rolling window` : "Waiting for the first sample"}{stale ? " · No recent sample" : ""}{latest && latest.t > now + 5_000 ? " · Server time is ahead of this device" : ""}</p>
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <span className="text-xs text-muted">{timezone}</span>
            <div role="group" aria-label="Chart time range" className="inline-flex rounded-md border border-line bg-canvas p-1">
              {([15, 60] as const).map((minutes) => <button key={minutes} type="button" aria-pressed={rangeMinutes === minutes} onClick={() => { setNow(Date.now()); setRangeMinutes(minutes); }} className={cn("min-h-9 rounded px-3 text-xs font-medium transition-colors", rangeMinutes === minutes ? "bg-raised text-ink" : "text-muted hover:text-ink")}>{minutes === 15 ? "15 min" : "1 hour"}</button>)}
            </div>
          </div>
        </div>
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <Stat label="Request rate" value={latest ? `${formatNumber(latest.rps, 2)} RPS` : "—"} hint="Latest rolling window" />
          <Stat label="P95 latency" value={latest && latest.rps > 0 ? `${formatNumber(latest.p95, 0)} ms` : "—"} hint="95th percentile" />
          <Stat label="Error rate" value={latest && latest.rps > 0 ? formatPercent(latest.errorRate, 2) : "—"} hint="5xx share of requests" />
          <Stat label="Unique IPs" value={latest ? formatNumber(latest.uniqueIps, 0) : "—"} hint="Latest rolling window" />
        </div>
        {historyError && (
          <div className="space-y-2">
            <ErrorNote message={`Metric history unavailable: ${historyError}. Live updates will continue.`} />
            <Button variant="ghost" onClick={() => retryHistory.current()}>Retry history</Button>
          </div>
        )}
        <p className="text-xs leading-relaxed text-muted">Points are measurements at the displayed time, calculated over a rolling window. Gaps mean samples are unavailable; they do not imply zero traffic. <span className="font-mono">{live.points.length} {live.points.length === 1 ? "sample" : "samples"}</span> loaded (up to 900).</p>
        <div className="grid min-w-0 grid-cols-1 gap-4 xl:grid-cols-2">
          <Card className="min-w-0 overflow-hidden border-line/90 bg-surface/80 p-4 sm:p-5">
            <div className="mb-4 border-b border-line/70 pb-3 xl:min-h-16"><h3 className="text-base font-semibold">Request traffic</h3><p className="mt-1 text-xs text-muted">Average requests per second within each rolling window</p></div>
            <TimeSeries kind="traffic" points={visiblePoints} range={range} />
          </Card>
          <Card className="min-w-0 overflow-hidden border-line/90 bg-surface/80 p-4 sm:p-5">
            <div className="mb-4 border-b border-line/70 pb-3 xl:min-h-16"><h3 className="text-base font-semibold">Error rate</h3><p className="mt-1 text-xs text-muted">HTTP 5xx responses as a share of requests</p></div>
            <ErrorRateChart points={visiblePoints} range={range} />
          </Card>
          <Card className="min-w-0 overflow-hidden border-line/90 bg-surface/80 p-4 sm:p-5 xl:col-span-2">
            <div className="mb-4 border-b border-line/70 pb-3"><h3 className="text-base font-semibold">Latency percentiles</h3><p className="mt-1 text-xs text-muted">Response times in milliseconds; higher percentiles expose slow requests</p></div>
            <TimeSeries kind="latency" points={visiblePoints} range={range} />
          </Card>
        </div>
      </section>

      {isAdmin && <MembersCard projectId={projectId} ownerEmail={projectQuery.data?.owner_email ?? ""} />}

      <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.15fr)]">
        <Card className="p-4">
          <h2 className="text-sm font-medium">Alert thresholds</h2>
          <p className="mt-0.5 text-xs text-muted">A value of 0 disables that rule.</p>

          {thresholdsQuery.isLoading && (
            <div className="mt-4">
              <div role="status" aria-label="Loading thresholds" className="space-y-3"><span className="sr-only">Loading thresholds</span><Skeleton className="h-10" /><Skeleton className="h-10" /></div>
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
                if (isAdmin && !saveThresholds.isPending) saveThresholds.mutate(draft);
              }}
            >
              <fieldset disabled={!isAdmin || saveThresholds.isPending} className="grid gap-3 sm:grid-cols-2">
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
              </fieldset>

              {saveThresholds.isError && (
                <ErrorNote
                  message={errorMessage(saveThresholds.error, "Could not save thresholds")}
                />
              )}

              {isAdmin ? (
                <div className="flex items-center gap-3">
                  <Button type="submit" disabled={saveThresholds.isPending}>
                    {saveThresholds.isPending ? "Saving…" : "Save thresholds"}
                  </Button>
                  {saveThresholds.isSuccess && <span className="text-xs text-ok">Saved</span>}
                </div>
              ) : (
                <p className="text-xs text-muted">Only administrators can change thresholds.</p>
              )}
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

          <p className="mt-1 text-xs text-muted">Showing up to 100 recent anomalies.</p>
          {anomaliesQuery.isLoading && (
            <div className="mt-4">
              <LoadingRows label="Loading anomalies" rows={2} />
            </div>
          )}

          {anomaliesQuery.isError && (
            <div className="mt-4">
              <ErrorNote message={errorMessage(anomaliesQuery.error, "Could not load anomalies")} />
            </div>
          )}

          {!anomaliesQuery.isLoading && !anomaliesQuery.isError && mergedAnomalies.length === 0 && (
            <p className="mt-4 text-sm text-muted">No anomalies detected.</p>
          )}

          {mergedAnomalies.length > 0 && (
            <ul
              className="mt-4 max-h-[26rem] space-y-3 overflow-y-auto pr-1"
              aria-live="polite"
              aria-label="Anomaly history"
              tabIndex={0}
            >
              {visibleAnomalies.map((anomaly) => (
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
                  {anomaly.analysis_status && (
                    <p className="mt-1 text-xs text-muted">
                      Advisory analysis: {anomaly.analysis_status.replaceAll("_", " ")}
                    </p>
                  )}
                  {anomaly.diagnosis && (
                    <div className="mt-2 space-y-1 text-xs text-muted">
                      {anomaly.diagnosis.evidence_sufficient ? (
                        <p>
                          Pattern: {anomaly.diagnosis.pattern.replaceAll("_", " ")}; investigate: {anomaly.diagnosis.direction.replaceAll("_", " ")}.
                          Jev urgency: {formatNumber(anomaly.diagnosis.urgency, 1)} / 2; alert severity is unchanged.
                        </p>
                      ) : (
                        <p>Insufficient context for a suggested investigation.</p>
                      )}
                      {anomaly.diagnosis.evidence?.request_count !== undefined && (
                        <p>
                          Evidence: {formatNumber(anomaly.diagnosis.evidence.request_count, 0)} requests in {anomaly.diagnosis.evidence.window_seconds}s
                          {anomaly.diagnosis.evidence.baseline
                            ? `; ${anomaly.diagnosis.evidence.baseline.samples} baseline snapshots`
                            : "; baseline unavailable"}.
                        </p>
                      )}
                      <p>Model: {anomaly.diagnosis.model}. Advisory only; confidence is not proof.</p>
                    </div>
                  )}
                </li>
              ))}
            </ul>
          )}
          {pageCount > 1 && <nav aria-label="Anomaly pages" className="mt-4 flex items-center justify-between gap-2 border-t border-line pt-3 text-xs text-muted">
            <Button variant="ghost" onClick={() => setAnomalyPage(Math.max(0, currentPage - 1))} disabled={currentPage === 0}>Previous</Button>
            <span>Page {currentPage + 1} of {pageCount}</span>
            <Button variant="ghost" onClick={() => setAnomalyPage(Math.min(pageCount - 1, currentPage + 1))} disabled={currentPage === pageCount - 1}>Next</Button>
          </nav>}
        </Card>
      </div>

      {isAdmin && (
        <section className="space-y-4" aria-labelledby="admin-heading">
          <div>
            <h2 id="admin-heading" className="text-sm font-medium">
              Administration
            </h2>
            <p className="mt-0.5 text-xs text-muted">
              Administrators only. Every action is recorded.
            </p>
          </div>

          <div className="grid items-start gap-4 lg:grid-cols-2">
            <OwnershipCard
              projectId={projectId}
              ownerEmail={projectQuery.data?.owner_email ?? ""}
            />
          </div>

          <DangerZone projectId={projectId} projectName={projectQuery.data?.name ?? ""} />
        </section>
      )}
    </div>
  );
}

function MembersCard({ projectId, ownerEmail }: { projectId: string; ownerEmail: string }) {
  const queryClient = useQueryClient();
  const [email, setEmail] = useState("");
  const users = useQuery({ queryKey: ["known-users"], queryFn: listKnownUsers });
  const viewers = useQuery({ queryKey: ["viewers", projectId], queryFn: () => listViewers(projectId) });
  const change = useMutation({
    mutationFn: ({ address, method }: { address: string; method: "POST" | "DELETE" }) => changeViewer(projectId, address, method),
    onSuccess: () => { setEmail(""); void queryClient.invalidateQueries({ queryKey: ["viewers", projectId] }); },
  });
  const selected = users.data?.find((user) => user.toLowerCase() === email.trim().toLowerCase());
  return <Card className="p-4 sm:p-5">
    <div className="flex flex-wrap items-start justify-between gap-3"><div><h2 className="text-sm font-semibold">Project access</h2><p className="mt-1 text-xs text-muted">Add people who have already signed in. Members can view, but cannot change settings.</p></div></div>
    {ownerEmail && <p className="mt-4 text-xs text-muted">Owner: <span className="text-ink">{ownerEmail}</span></p>}
    {viewers.isLoading && <LoadingRows label="Loading project members" rows={2} />}
    {viewers.isError && <ErrorNote message={errorMessage(viewers.error, "Could not load members")} />}
    {viewers.data && <ul className="mt-3 divide-y divide-line" aria-label="Project members">
      {viewers.data.map((user) => <li key={user} className="flex items-center justify-between gap-3 py-2 text-sm"><span className="min-w-0 break-all">{user} <span className="text-xs text-muted">· View only</span></span><Button variant="quiet" disabled={change.isPending} onClick={() => change.mutate({ address: user, method: "DELETE" })} aria-label={`Remove ${user}`}>Remove</Button></li>)}
    </ul>}
    {users.isError && <ErrorNote message={errorMessage(users.error, "Could not load known users")} />}
    {change.isError && <ErrorNote message={errorMessage(change.error, "Could not update access")} />}
    <form className="mt-3 flex flex-wrap items-end gap-2" onSubmit={(event) => { event.preventDefault(); if (selected && !change.isPending) change.mutate({ address: selected, method: "POST" }); }}>
      <div className="min-w-0 flex-1"><label htmlFor="viewer-email" className="text-xs font-medium text-muted">Find a signed-in user</label><Input id="viewer-email" type="email" list="known-users" value={email} onChange={(event) => setEmail(event.target.value)} placeholder="name@example.com" className="mt-1" /><datalist id="known-users">{users.data?.map((user) => <option key={user} value={user} />)}</datalist></div>
      <Button type="submit" disabled={!selected || change.isPending || viewers.data?.some((v) => v.toLowerCase() === selected.toLowerCase())}>Add</Button>
    </form>
    {email.trim() && !selected && <p className="mt-2 text-xs text-muted">Choose a user who has signed in to Metric.</p>}
  </Card>;
}

function IngestKeyCard({ projectId }: { projectId: string }) {
  const [rotatedKey, setRotatedKey] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [confirmingRevoke, setConfirmingRevoke] = useState(false);
  const [revealed, setRevealed] = useState(false);
  const [revoked, setRevoked] = useState(false);

  const rotate = useMutation({
    mutationFn: () => rotateApiKey(projectId),
    onSuccess: (project) => {
      setRotatedKey(project.api_key);
      setRevealed(false);
      setCopied(false);
      setRevoked(false);
      setConfirmingRevoke(false);
    },
  });

  const revoke = useMutation({
    mutationFn: () => revokeApiKey(projectId),
    onSuccess: () => {
      setRotatedKey(null);
      setRevoked(true);
      setConfirmingRevoke(false);
    },
  });

  const copyKey = async () => {
    if (!rotatedKey) return;
    const ok = await copyToClipboard(rotatedKey);
    setCopied(ok);
    if (ok) window.setTimeout(() => setCopied(false), 2000);
  };

  return (
    <Card className="p-4">
      <h3 className="text-sm font-medium">Ingest key</h3>
      <p className="mt-0.5 text-xs text-muted">
        Services use this key to send request logs. Rotating stops the old one immediately;
        the new key is shown once.
      </p>

      {rotatedKey && (
        <div className="mt-3 rounded-md border border-accent/40 bg-accent/5 p-3" role="status">
          <p className="text-xs font-medium">New key — copy it now</p>
          <code className="mt-2 block overflow-x-auto rounded-md border border-line bg-canvas px-3 py-2 font-mono text-xs">
            {revealed ? rotatedKey : "••••••••••••••••••••••••"}
          </code>
          <Button variant="ghost" onClick={() => setRevealed((shown) => !shown)} aria-pressed={revealed} className="mt-2 mr-2 px-2.5 py-1.5 text-xs">
            {revealed ? <LightbulbOff className="size-3.5" aria-hidden /> : <Lightbulb className="size-3.5" aria-hidden />}
            {revealed ? "Hide key" : "Show key"}
          </Button>
          <Button variant="ghost" onClick={copyKey} className="mt-2 px-2.5 py-1.5 text-xs">
            <Copy className="size-3.5" aria-hidden />
            {copied ? "Copied" : "Copy key"}
          </Button>
        </div>
      )}

      {revoked && !rotatedKey && (
        <p className="mt-3 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn">
          Ingest is disabled until you rotate a new key.
        </p>
      )}

      {rotate.isError && (
        <div className="mt-3">
          <ErrorNote message={errorMessage(rotate.error, "Could not rotate the key")} />
        </div>
      )}
      {revoke.isError && (
        <div className="mt-3">
          <ErrorNote message={errorMessage(revoke.error, "Could not revoke the key")} />
        </div>
      )}

      <div className="mt-4 flex flex-wrap items-center gap-2">
        <Button
          variant="ghost"
          onClick={() => rotate.mutate()}
          disabled={rotate.isPending}
        >
          {rotate.isPending ? "Rotating…" : "Rotate key"}
        </Button>

        {confirmingRevoke ? (
          <>
            <Button
              variant="ghost"
              className="border-danger/40 text-danger hover:bg-danger/10"
              onClick={() => revoke.mutate()}
              disabled={revoke.isPending}
              aria-busy={revoke.isPending}
            >
              {revoke.isPending ? "Revoking…" : "Confirm revoke"}
            </Button>
            <Button
              variant="quiet"
              onClick={() => setConfirmingRevoke(false)}
              disabled={revoke.isPending}
            >
              Cancel
            </Button>
          </>
        ) : (
          <Button variant="ghost" onClick={() => setConfirmingRevoke(true)}>
            Revoke key
          </Button>
        )}
      </div>

      {confirmingRevoke && (
        <p className="mt-2 text-xs text-muted" role="status">
          Ingest requests are rejected until a new key is rotated.
        </p>
      )}
    </Card>
  );
}

function OwnershipCard({ projectId, ownerEmail }: { projectId: string; ownerEmail: string }) {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState(ownerEmail);

  useEffect(() => {
    setDraft(ownerEmail);
  }, [ownerEmail]);

  const assign = useMutation({
    mutationFn: (email: string) => assignOwner(projectId, email),
    onSuccess: (project) => {
      queryClient.setQueryData(["project", projectId], project);
      void queryClient.invalidateQueries({ queryKey: ["projects"] });
    },
  });

  return (
    <Card className="p-4">
      <h3 className="text-sm font-medium">Ownership</h3>
      <p className="mt-0.5 text-xs text-muted">
        The owner can see this project. Leave it empty for administrators only.
      </p>

      <form
        className="mt-4 space-y-3"
        onSubmit={(event) => {
          event.preventDefault();
          assign.mutate(draft.trim());
        }}
      >
        <div>
          <label htmlFor="owner-email" className="text-xs font-medium text-muted">
            Owner email
          </label>
          <Input
            id="owner-email"
            type="email"
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            placeholder="nobody (admin-only)"
            autoComplete="off"
            className="mt-1"
          />
        </div>

        {assign.isError && (
          <ErrorNote message={errorMessage(assign.error, "Could not update the owner")} />
        )}

        <div className="flex items-center gap-3">
          <Button
            type="submit"
            variant="ghost"
            disabled={assign.isPending || draft.trim() === ownerEmail}
          >
            {assign.isPending ? "Saving…" : "Save owner"}
          </Button>
          {assign.isSuccess && <span className="text-xs text-ok">Saved</span>}
        </div>
      </form>
    </Card>
  );
}

function DangerZone({ projectId, projectName }: { projectId: string; projectName: string }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [armed, setArmed] = useState(false);
  const [confirmation, setConfirmation] = useState("");

  const remove = useMutation({
    mutationFn: () => deleteProject(projectId),
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: ["project", projectId] });
      queryClient.removeQueries({ queryKey: ["thresholds", projectId] });
      queryClient.removeQueries({ queryKey: ["anomalies", projectId] });
      void queryClient.invalidateQueries({ queryKey: ["projects"] });
      navigate("/dashboard", { replace: true });
    },
  });

  const matches = confirmation === projectName && projectName !== "";

  return (
    <Card className="border-danger/30 p-4">
      <h3 className="text-sm font-medium">Delete project</h3>
      <p className="mt-0.5 text-xs text-muted">
        Removes the project and all its history. This cannot be undone.
      </p>

      {remove.isError && (
        <div className="mt-3">
          <ErrorNote message={errorMessage(remove.error, "Could not delete the project")} />
        </div>
      )}

      {!armed ? (
        <Button
          variant="ghost"
          className="mt-4 border-danger/40 text-danger hover:bg-danger/10"
          onClick={() => {
            setConfirmation("");
            setArmed(true);
          }}
        >
          Delete project
        </Button>
      ) : (
        <form
          className="mt-4 space-y-3"
          onSubmit={(event) => {
            event.preventDefault();
            if (matches) remove.mutate();
          }}
        >
          <div>
            <label htmlFor="delete-confirmation" className="text-xs font-medium text-muted">
              Type <span className="font-mono text-ink">{projectName}</span> to confirm
            </label>
            <Input
              id="delete-confirmation"
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              autoComplete="off"
              autoFocus
              className="mt-1"
            />
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <Button
              type="submit"
              variant="ghost"
              className="border-danger/40 text-danger hover:bg-danger/10"
              disabled={!matches || remove.isPending}
              aria-busy={remove.isPending}
            >
              {remove.isPending ? "Deleting…" : "Delete permanently"}
            </Button>
            <Button
              type="button"
              variant="quiet"
              onClick={() => {
                setArmed(false);
                setConfirmation("");
              }}
              disabled={remove.isPending}
            >
              Cancel
            </Button>
          </div>
        </form>
      )}
    </Card>
  );
}
