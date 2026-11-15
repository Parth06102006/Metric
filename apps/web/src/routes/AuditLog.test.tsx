import { screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AUDIT_PAGE_SIZE, listAudit } from "../features/audit/api";
import { useMe } from "../features/identity/api";
import { renderWithProviders } from "../test/render";
import type { AuditEvent } from "../types/api";
import { AuditLog } from "./AuditLog";

vi.mock("../features/identity/api", () => ({ useMe: vi.fn() }));
vi.mock("../features/audit/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("../features/audit/api")>(),
  listAudit: vi.fn(),
}));

const event: AuditEvent = {
  id: 1, ts: "2025-01-01T00:00:00Z", actor_email: "admin@example.com",
  action: "project.create", target_project_id: "p1", outcome: "success",
  request_id: "req-1", details: { reason: "test" },
};

function identity(admin: boolean) {
  vi.mocked(useMe).mockReturnValue({
    data: { email: "person@example.com", admin }, isLoading: false,
  } as ReturnType<typeof useMe>);
}

beforeEach(() => {
  vi.resetAllMocks();
  identity(true);
  vi.mocked(listAudit).mockResolvedValue([]);
});

describe("AuditLog", () => {
  it("does not fetch audit events for non-admins", () => {
    identity(false);
    renderWithProviders(<AuditLog />);
    expect(screen.getByText("Administrators only")).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Action" })).not.toBeInTheDocument();
    expect(listAudit).not.toHaveBeenCalled();
  });

  it("shows the loading and empty states", async () => {
    let finish!: (events: AuditEvent[]) => void;
    vi.mocked(listAudit).mockReturnValue(new Promise((resolve) => { finish = resolve; }));
    renderWithProviders(<AuditLog />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading audit events");
    finish([]);
    expect(await screen.findByText("No matching events")).toBeInTheDocument();
  });

  it("reports audit loading errors", async () => {
    vi.mocked(listAudit).mockRejectedValue(new Error("Audit unavailable"));
    renderWithProviders(<AuditLog />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Audit unavailable");
  });

  it("filters events by action", async () => {
    vi.mocked(listAudit).mockImplementation(async (filter) => filter?.action ? [] : [event]);
    const { user } = renderWithProviders(<AuditLog />);
    expect(await screen.findByText("1 event loaded")).toBeInTheDocument();
    expect(screen.getAllByText("Project created").length).toBeGreaterThan(0);
    await user.selectOptions(screen.getByRole("combobox", { name: "Action" }), "auth.denied");
    await waitFor(() => expect(listAudit).toHaveBeenCalledWith({ action: "auth.denied", before: undefined, beforeId: undefined }));
    expect(await screen.findByText("Nothing has been recorded for this filter yet.")).toBeInTheDocument();
  });

  it("loads older events using the last event's exclusive cursor", async () => {
    const page = Array.from({ length: AUDIT_PAGE_SIZE }, (_, index) => ({ ...event, id: 200 - index }));
    vi.mocked(listAudit)
      .mockResolvedValueOnce(page)
      .mockResolvedValueOnce([{ ...event, id: 100, action: "auth.denied" }]);
    const { user } = renderWithProviders(<AuditLog />);
    await user.click(await screen.findByRole("button", { name: "Load older" }));
    await waitFor(() => expect(listAudit).toHaveBeenLastCalledWith({ action: undefined, before: event.ts, beforeId: 101 }));
    expect(await screen.findByText("101 events loaded")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load older" })).not.toBeInTheDocument();
  });
});
