import { screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useMe } from "../../features/identity/api";
import { renderWithProviders } from "../../test/render";
import { AppShell } from "./AppShell";

vi.mock("../../features/identity/api", () => ({ useMe: vi.fn() }));

beforeEach(() => vi.resetAllMocks());

describe("AppShell", () => {
  it("shows administrative navigation and the authenticated identity to admins", () => {
    vi.mocked(useMe).mockReturnValue({ data: { email: "admin@example.com", admin: true } } as ReturnType<typeof useMe>);
    renderWithProviders(<AppShell />, { route: "/audit" });
    expect(screen.getByRole("link", { name: "Audit" })).toHaveAttribute("href", "/audit");
    expect(screen.getByText("admin@example.com")).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "Sign out" })).toHaveLength(2);
    expect(screen.getAllByRole("link", { name: "Sign out" })[0]).toHaveAttribute("href", "/oauth2/sign_out?rd=/");
    expect(screen.getByRole("main")).toHaveClass("app-main");
  });

  it("keeps project navigation marked as the current section on detail pages", () => {
    vi.mocked(useMe).mockReturnValue({ data: { email: "user@example.com", admin: false } } as ReturnType<typeof useMe>);
    renderWithProviders(<AppShell />, { route: "/p/project-1" });
    expect(screen.getAllByRole("link", { name: "Projects" })).toHaveLength(2);
    for (const link of screen.getAllByRole("link", { name: "Projects" })) {
      expect(link).toHaveAttribute("aria-current", "location");
    }
    expect(screen.getAllByRole("link", { name: "Product home" })).toHaveLength(2);
  });

  it("does not show the audit link to a non-admin", () => {
    vi.mocked(useMe).mockReturnValue({ data: { email: "user@example.com", admin: false } } as ReturnType<typeof useMe>);
    renderWithProviders(<AppShell />);
    expect(screen.queryByRole("link", { name: "Audit" })).not.toBeInTheDocument();
    expect(screen.getByText("user@example.com")).toBeInTheDocument();
  });
});
