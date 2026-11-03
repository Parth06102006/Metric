import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useMe } from "../features/identity/api";
import { getMetricHistory } from "../features/metrics/api";
import { assignOwner, changeViewer, deleteProject, getProject, listAnomalies, listKnownUsers, listViewers, revokeApiKey, rotateApiKey } from "../features/projects/api";
import { getThresholds, updateThresholds } from "../features/thresholds/api";
import { copyToClipboard } from "../lib/clipboard";
import { renderWithProviders } from "../test/render";
import type { Anomaly, Snapshot, Threshold } from "../types/api";
import { ProjectDetail } from "./ProjectDetail";

const live = vi.hoisted(() => ({
  handlers: null as null | {
    onSnapshot: (snapshot: Snapshot) => void;
    onAnomaly: (anomaly: Anomaly) => void;
    onStatus: (status: "connecting" | "open" | "closed") => void;
  },
  connect: vi.fn(),
  close: vi.fn(),
}));

vi.mock("../features/identity/api", () => ({ useMe: vi.fn() }));
vi.mock("../features/metrics/api", () => ({ getMetricHistory: vi.fn() }));
vi.mock("../features/projects/api", () => ({
  getProject: vi.fn(), listAnomalies: vi.fn(), assignOwner: vi.fn(),
  deleteProject: vi.fn(), rotateApiKey: vi.fn(), revokeApiKey: vi.fn(),
  listKnownUsers: vi.fn(), listViewers: vi.fn(), changeViewer: vi.fn(),
}));
vi.mock("../features/thresholds/api", () => ({ getThresholds: vi.fn(), updateThresholds: vi.fn() }));
vi.mock("../lib/clipboard", () => ({ copyToClipboard: vi.fn() }));
vi.mock("../components/charts/TimeSeries", () => ({ TimeSeries: () => <div role="img" aria-label="Chart" /> }));
vi.mock("../components/charts/ErrorRateChart", () => ({ ErrorRateChart: () => <div role="img" aria-label="Error rate chart" /> }));
vi.mock("../features/live/connection", () => ({
  LiveConnection: class {
    constructor(_projectId: string, handlers: NonNullable<typeof live.handlers>) { live.handlers = handlers; }
    connect() { live.connect(); }
    close() { live.close(); }
  },
}));

const threshold: Threshold = { p95_ms: 1000, error_rate: 5, rps_max: 100, req_per_ip_max: 30 };
const project = { project_id: "p1", name: "Shop", owner_email: "owner@example.com" };
const pending: Anomaly = {
  id: 7, type: "error_rate", severity: "high", message: "Error rate spiked",
  value: 20, threshold: 5, analysis_status: "pending", detected_at: "2025-01-01T00:00:00Z",
};

function identity(admin: boolean) {
  vi.mocked(useMe).mockReturnValue({ data: { email: "user@example.com", admin } } as ReturnType<typeof useMe>);
}
function showProject() {
  return renderWithProviders(<ProjectDetail />, { route: "/p/p1", path: "/p/:projectId" });
}

beforeEach(() => {
  vi.resetAllMocks();
  live.handlers = null;
  identity(true);
  vi.mocked(getMetricHistory).mockResolvedValue([]);
  vi.mocked(getProject).mockResolvedValue(project);
  vi.mocked(getThresholds).mockResolvedValue({ ...threshold });
  vi.mocked(listAnomalies).mockResolvedValue([]);
  vi.mocked(listKnownUsers).mockResolvedValue(["alice@example.com"]);
  vi.mocked(listViewers).mockResolvedValue([]);
  vi.mocked(changeViewer).mockResolvedValue(undefined);
  vi.mocked(updateThresholds).mockImplementation(async (_id, values) => values);
  vi.mocked(copyToClipboard).mockResolvedValue(true);
  vi.mocked(revokeApiKey).mockResolvedValue(undefined);
  vi.mocked(deleteProject).mockResolvedValue(undefined);
});

describe("ProjectDetail", () => {
  it("shows project loading errors and closes the live connection on unmount", async () => {
    vi.mocked(getProject).mockRejectedValue(new Error("Project unavailable"));
    const view = showProject();
    expect(await screen.findByRole("alert")).toHaveTextContent("Project unavailable");
    expect(live.connect).toHaveBeenCalledTimes(1);
    view.unmount();
    expect(live.close).toHaveBeenCalledTimes(1);
  });

  it("hides administrative controls for non-admins", async () => {
    identity(false);
    showProject();
    expect(await screen.findByRole("heading", { name: "Shop" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Administration" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Rotate key" })).not.toBeInTheDocument();
    const field = await screen.findByRole("spinbutton", { name: "Error rate (%)" });
    expect(field).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save thresholds" })).not.toBeInTheDocument();
    expect(screen.getByText("Only administrators can change thresholds.")).toBeInTheDocument();
    fireEvent.submit(field.closest("form")!);
    expect(updateThresholds).not.toHaveBeenCalled();
  });

  it("edits and saves threshold values", async () => {
    const { user } = showProject();
    const field = await screen.findByRole("spinbutton", { name: "Error rate (%)" });
    await user.clear(field);
    await user.type(field, "12.5");
    await user.click(screen.getByRole("button", { name: "Save thresholds" }));
    await waitFor(() => expect(updateThresholds).toHaveBeenCalledWith("p1", { ...threshold, error_rate: 12.5 }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });

  it("disables repeated saves while a threshold request is pending", async () => {
    let finish!: (value: Threshold) => void;
    vi.mocked(updateThresholds).mockReturnValue(new Promise((resolve) => { finish = resolve; }));
    const { user } = showProject();
    await user.click(await screen.findByRole("button", { name: "Save thresholds" }));
    const saving = await screen.findByRole("button", { name: "Saving…" });
    expect(saving).toBeDisabled();
    const field = screen.getByRole("spinbutton", { name: "Error rate (%)" });
    expect(field).toBeDisabled();
    await user.click(saving);
    fireEvent.submit(field.closest("form")!);
    expect(updateThresholds).toHaveBeenCalledTimes(1);
    await act(async () => finish({ ...threshold }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });

  it("reports threshold update failures", async () => {
    vi.mocked(updateThresholds).mockRejectedValue(new Error("Threshold save failed"));
    const { user } = showProject();
    await user.click(await screen.findByRole("button", { name: "Save thresholds" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Threshold save failed");
  });

  it("requires explicit confirmation before revoking and shows the revoked state", async () => {
    const { user } = showProject();
    await user.click(await screen.findByRole("button", { name: "API key" }));
    await user.click(await screen.findByRole("button", { name: "Revoke key" }));
    expect(revokeApiKey).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(revokeApiKey).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Revoke key" }));
    await user.click(screen.getByRole("button", { name: "Confirm revoke" }));
    await waitFor(() => expect(revokeApiKey).toHaveBeenCalledWith("p1"));
    expect(await screen.findByText("Ingest is disabled until you rotate a new key.")).toBeInTheDocument();
  });

  it("rotates a key and allows copying the new one", async () => {
    vi.mocked(rotateApiKey).mockResolvedValue({ ...project, api_key: "rotated-secret" });
    const { user } = showProject();
    await user.click(await screen.findByRole("button", { name: "API key" }));
    await user.click(await screen.findByRole("button", { name: "Rotate key" }));
    expect(await screen.findByText("New key — copy it now")).toBeInTheDocument();
    expect(screen.queryByText("rotated-secret")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show key" }));
    expect(screen.getByText("rotated-secret")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Copy key" }));
    expect(copyToClipboard).toHaveBeenCalledWith("rotated-secret");
    expect(await screen.findByRole("button", { name: "Copied" })).toBeInTheDocument();
  });

  it("adds only a known signed-in user as a viewer", async () => {
    const { user } = showProject();
    const input = await screen.findByLabelText("Find a signed-in user");
    await user.type(input, "unknown@example.com");
    expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
    await user.clear(input);
    await user.type(input, "alice@example.com");
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(changeViewer).toHaveBeenCalledWith("p1", "alice@example.com", "POST"));
  });

  it("paginates the loaded anomalies", async () => {
    vi.mocked(listAnomalies).mockResolvedValue(Array.from({ length: 7 }, (_, i) => ({ ...pending, id: i + 1, message: `Incident ${i + 1}` })));
    const { user } = showProject();
    expect(await screen.findByText("Incident 1")).toBeInTheDocument();
    expect(screen.queryByText("Incident 6")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByText("Incident 6")).toBeInTheDocument();
    expect(screen.queryByText("Incident 1")).not.toBeInTheDocument();
  });

  it("updates ownership and disables saving an unchanged owner", async () => {
    vi.mocked(assignOwner).mockResolvedValue({ ...project, owner_email: "alice@example.com" });
    const { user } = showProject();
    const owner = await screen.findByRole("textbox", { name: "Owner email" });
    expect(screen.getByRole("button", { name: "Save owner" })).toBeDisabled();
    await user.clear(owner);
    await user.type(owner, "alice@example.com");
    await user.click(screen.getByRole("button", { name: "Save owner" }));
    await waitFor(() => expect(assignOwner).toHaveBeenCalledWith("p1", "alice@example.com"));
    await waitFor(() => expect(screen.getByRole("button", { name: "Save owner" })).toBeDisabled());
  });

  it("requires the exact project name before deletion", async () => {
    const { user } = showProject();
    await user.click(await screen.findByRole("button", { name: "Delete project" }));
    const input = screen.getByRole("textbox", { name: "Type Shop to confirm" });
    const submit = screen.getByRole("button", { name: "Delete permanently" });
    await user.type(input, "shop");
    expect(submit).toBeDisabled();
    expect(deleteProject).not.toHaveBeenCalled();
    await user.clear(input);
    await user.type(input, "Shop");
    await user.click(submit);
    await waitFor(() => expect(deleteProject).toHaveBeenCalledWith("p1"));
    expect(await screen.findByText("Dashboard destination")).toBeInTheDocument();
  });

  it("does not report an empty anomaly history when loading fails", async () => {
    vi.mocked(listAnomalies).mockRejectedValue(new Error("Anomaly history unavailable"));
    showProject();
    expect(await screen.findByRole("alert")).toHaveTextContent("Anomaly history unavailable");
    expect(screen.queryByText("No anomalies detected.")).not.toBeInTheDocument();
  });

  it("shows an advisory with evidence and withholds insufficient recommendations", async () => {
    vi.mocked(listAnomalies).mockResolvedValue([
      { ...pending, analysis_status: "completed", analysis: "Investigate errors", diagnosis: {
        pattern: "error_spike", direction: "failing_endpoint", urgency: 1.5,
        pattern_confidence: 0.8, direction_confidence: 0.8, urgency_confidence: 0.8,
        evidence_probability: 0.9, model: "jev", input_tokens: 100, evidence_sufficient: true,
        evidence: { trigger_type: "error_rate", severity: "high", value: 20, threshold: 5,
          window_seconds: 60, request_count: 30, rps: 0.5, p50: 1, p95: 2, p99: 3,
          error_rate: 20, top_ip_rps: 0.1, unique_ips: 4 },
      } },
      { ...pending, id: 8, message: "Insufficient sample", diagnosis: {
        pattern: "unknown", direction: "unknown", urgency: 0,
        pattern_confidence: 0, direction_confidence: 0, urgency_confidence: 0,
        evidence_probability: 0, model: "jev", input_tokens: 20, evidence_sufficient: false,
      } },
    ]);
    showProject();
    expect(await screen.findByText(/Pattern: error spike; investigate: failing endpoint/)).toBeInTheDocument();
    expect(screen.getByText(/Evidence: 30 requests in 60s; baseline unavailable/)).toBeInTheDocument();
    expect(screen.getByText("Insufficient context for a suggested investigation.")).toBeInTheDocument();
    expect(screen.getAllByText(/Advisory only; confidence is not proof/)).toHaveLength(2);
  });

  it("displays live metric snapshots", async () => {
    showProject();
    await screen.findByRole("heading", { name: "Shop" });
    act(() => live.handlers!.onSnapshot({
      ts: "2025-01-01T00:00:00Z", window_s: 60, rps: 3.5, p50: 20, p95: 150, p99: 200,
      error_rate: 2.5, unique_ips: 9, top_ip_rps: 0.2, avg_payload_bytes: 100, endpoints: {},
    }));
    expect(await screen.findByText("3.5 RPS")).toBeInTheDocument();
    expect(screen.getByText("150 ms")).toBeInTheDocument();
    expect(screen.getByText("2.5%")).toBeInTheDocument();
    expect(screen.getByText("9")).toBeInTheDocument();
    expect(screen.getByText("1 sample")).toBeInTheDocument();
  });

  it("merges late history with newer live data, and fetches again on reconnect", async () => {
    let finish!: (snapshots: Snapshot[]) => void;
    vi.mocked(getMetricHistory).mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
    const old: Snapshot = { ts: "2025-01-01T00:00:01.123Z", window_s: 60, rps: 1,
      p50: 1, p95: 1, p99: 1, error_rate: 0, unique_ips: 1, top_ip_rps: 0, avg_payload_bytes: 0, endpoints: {} };
    const newer = { ...old, ts: "2025-01-01T00:00:02.123456Z", rps: 2 };
    showProject();
    await screen.findByRole("heading", { name: "Shop" });
    act(() => live.handlers!.onSnapshot(newer));
    await act(async () => finish([old, { ...newer, ts: "2025-01-01T00:00:02.123Z" }]));
    expect(await screen.findByText("2 samples")).toBeInTheDocument();
    expect(screen.getByText("2 RPS")).toBeInTheDocument();
    vi.mocked(getMetricHistory).mockResolvedValueOnce([old, newer, { ...old, ts: "2025-01-01T00:00:03Z", rps: 3 }]);
    act(() => { live.handlers!.onStatus("closed"); live.handlers!.onStatus("open"); });
    expect(await screen.findByText("3 samples")).toBeInTheDocument();
    expect(screen.getByText("3 RPS")).toBeInTheDocument();
    expect(getMetricHistory).toHaveBeenCalledTimes(2);
  });

  it("ignores an old history response after reconnect starts a newer request", async () => {
    let finishOld!: (snapshots: Snapshot[]) => void;
    const snap: Snapshot = { ts: "2025-01-01T00:00:00Z", window_s: 60, rps: 1,
      p50: 0, p95: 0, p99: 0, error_rate: 0, unique_ips: 1, top_ip_rps: 0, avg_payload_bytes: 0, endpoints: {} };
    vi.mocked(getMetricHistory).mockReturnValueOnce(new Promise((resolve) => { finishOld = resolve; }));
    vi.mocked(getMetricHistory).mockResolvedValueOnce([{ ...snap, ts: "2025-01-01T00:00:02Z", rps: 2 }]);
    showProject();
    await screen.findByRole("heading", { name: "Shop" });
    act(() => live.handlers!.onStatus("open"));
    expect(await screen.findByText("2 RPS")).toBeInTheDocument();
    await act(async () => finishOld([snap]));
    expect(screen.getByText("1 sample")).toBeInTheDocument();
    expect(screen.getByText("2 RPS")).toBeInTheDocument();
  });

  it("reports history failure while continuing live updates and retries", async () => {
    vi.mocked(getMetricHistory).mockRejectedValueOnce(new Error("Storage offline"));
    const { user } = showProject();
    expect(await screen.findByRole("alert")).toHaveTextContent("Metric history unavailable: Storage offline. Live updates will continue.");
    act(() => live.handlers!.onSnapshot({ ts: "2025-01-01T00:00:00Z", window_s: 60, rps: 5,
      p50: 0, p95: 0, p99: 0, error_rate: 0, unique_ips: 1, top_ip_rps: 0, avg_payload_bytes: 0, endpoints: {} }));
    expect(await screen.findByText("5 RPS")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry history" }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
  });

  it("deduplicates live incidents and reconciles when the connection reopens", async () => {
    vi.mocked(listAnomalies).mockResolvedValue([pending]);
    const view = showProject();
    expect(await screen.findByText("Error rate spiked")).toBeInTheDocument();
    expect(live.handlers).not.toBeNull();
    const completed = { ...pending, analysis_status: "completed", analysis: "Analysis completed" };
    act(() => { live.handlers!.onAnomaly(completed); live.handlers!.onStatus("closed"); });
    expect(screen.getByText("Reconnecting")).toBeInTheDocument();
    expect(screen.getByText("Analysis completed")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: "Anomaly history" })).getAllByRole("listitem")).toHaveLength(1);
    vi.mocked(listAnomalies).mockResolvedValue([completed]);
    act(() => live.handlers!.onStatus("open"));
    expect(screen.getByText("Live")).toBeInTheDocument();
    await waitFor(() => expect(vi.mocked(listAnomalies).mock.calls.length).toBeGreaterThanOrEqual(2));
    expect(await screen.findByText("Analysis completed")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: "Anomaly history" })).getAllByRole("listitem")).toHaveLength(1);
    view.unmount();
    expect(live.close).toHaveBeenCalledTimes(1);
  });
});
