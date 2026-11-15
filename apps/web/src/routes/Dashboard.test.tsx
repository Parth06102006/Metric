import { screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useMe } from "../features/identity/api";
import { createProject, listProjects } from "../features/projects/api";
import { copyToClipboard } from "../lib/clipboard";
import { renderWithProviders } from "../test/render";
import { Dashboard } from "./Dashboard";

vi.mock("../features/identity/api", () => ({ useMe: vi.fn() }));
vi.mock("../features/projects/api", () => ({ listProjects: vi.fn(), createProject: vi.fn() }));
vi.mock("../lib/clipboard", () => ({ copyToClipboard: vi.fn() }));

function identity(admin: boolean) {
  vi.mocked(useMe).mockReturnValue({
    data: { email: "person@example.com", admin },
    isLoading: false,
  } as ReturnType<typeof useMe>);
}

beforeEach(() => {
  vi.resetAllMocks();
  identity(true);
  vi.mocked(listProjects).mockResolvedValue([]);
  vi.mocked(copyToClipboard).mockResolvedValue(true);
});

describe("Dashboard", () => {
  it("shows a loading indicator and then the empty state", async () => {
    let finish!: (projects: Awaited<ReturnType<typeof listProjects>>) => void;
    vi.mocked(listProjects).mockReturnValue(new Promise((resolve) => { finish = resolve; }));
    renderWithProviders(<Dashboard />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading projects");
    finish([]);
    expect(await screen.findByText("No projects yet")).toBeInTheDocument();
  });

  it("shows a request error instead of an empty state", async () => {
    vi.mocked(listProjects).mockRejectedValue(new Error("Projects unavailable"));
    renderWithProviders(<Dashboard />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Projects unavailable");
    expect(screen.queryByText("No projects yet")).not.toBeInTheDocument();
  });

  it("hides creation and owner details from non-admins", async () => {
    identity(false);
    vi.mocked(listProjects).mockResolvedValue([
      { project_id: "project-1", name: "Store", owner_email: "owner@example.com" },
    ]);
    renderWithProviders(<Dashboard />);
    expect(await screen.findByRole("link", { name: /Store/ })).toHaveAttribute("href", "/p/project-1");
    expect(screen.queryByRole("button", { name: "New project" })).not.toBeInTheDocument();
    expect(screen.queryByText(/Owner:/)).not.toBeInTheDocument();
  });

  it("filters real projects by name, id, and admin-visible owner without hiding an empty result", async () => {
    vi.mocked(listProjects).mockResolvedValue([
      { project_id: "project-1", name: "Store", owner_email: "owner@example.com" },
      { project_id: "project-2", name: "Payments", owner_email: "other@example.com" },
    ]);
    const { user } = renderWithProviders(<Dashboard />);
    const search = await screen.findByRole("searchbox", { name: "Search projects" });
    await user.type(search, "OWNER@example");
    expect(screen.getByRole("link", { name: /Store/ })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Payments/ })).not.toBeInTheDocument();
    await user.clear(search);
    await user.type(search, "missing");
    expect(screen.getByText("No projects match your search.")).toBeInTheDocument();
    expect(screen.queryByText("No projects yet")).not.toBeInTheDocument();
  });

  it("provides the non-admin empty state", async () => {
    identity(false);
    renderWithProviders(<Dashboard />);
    expect(await screen.findByText("No projects assigned to you")).toBeInTheDocument();
    expect(screen.getByText(/Ask an administrator/)).toBeInTheDocument();
  });

  it("does not submit blank names and resets a cancelled form", async () => {
    const { user } = renderWithProviders(<Dashboard />);
    await user.click(screen.getByRole("button", { name: "New project" }));
    const input = screen.getByRole("textbox", { name: "Project name" });
    await user.type(input, "   ");
    expect(screen.getByRole("button", { name: "Create" })).toBeDisabled();
    expect(createProject).not.toHaveBeenCalled();
    await user.clear(input);
    await user.type(input, "Discard me");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await user.click(screen.getByRole("button", { name: "New project" }));
    expect(screen.getByRole("textbox", { name: "Project name" })).toHaveValue("");
  });

  it("creates a project, shows its one-time key and copies it", async () => {
    vi.mocked(createProject).mockResolvedValue({ project_id: "new-id", name: "Store", api_key: "one-time-key" });
    const { user } = renderWithProviders(<Dashboard />);
    await screen.findByText("No projects yet");
    await user.click(screen.getByRole("button", { name: "New project" }));
    await user.type(screen.getByRole("textbox", { name: "Project name" }), "Store");
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByText("Project created: Store")).toBeInTheDocument();
    expect(vi.mocked(createProject).mock.calls[0]?.[0]).toBe("Store");
    expect(screen.getByText("one-time-key")).toBeInTheDocument();
    expect(screen.getByText("POST /ingest/new-id")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Copy key" }));
    expect(copyToClipboard).toHaveBeenCalledWith("one-time-key");
    expect(await screen.findByRole("button", { name: "Copied" })).toBeInTheDocument();
    await waitFor(() => expect(listProjects).toHaveBeenCalledTimes(2));
  });

  it("shows creation failures without exposing a key", async () => {
    vi.mocked(createProject).mockRejectedValue(new Error("Creation failed"));
    const { user } = renderWithProviders(<Dashboard />);
    await user.click(screen.getByRole("button", { name: "New project" }));
    await user.type(screen.getByRole("textbox", { name: "Project name" }), "Store");
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Creation failed");
    expect(screen.queryByRole("button", { name: "Copy key" })).not.toBeInTheDocument();
  });
});
