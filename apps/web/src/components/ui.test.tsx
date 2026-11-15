import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { Badge, Button, ErrorNote, Input, Spinner, Stat } from "./ui";

describe("shared UI components", () => {
  it("forwards button interactions and prevents clicks when disabled", async () => {
    const onClick = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(<Button onClick={onClick}>Save</Button>);
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onClick).toHaveBeenCalledTimes(1);
    rerender(<Button disabled onClick={onClick}>Save</Button>);
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("associates forwarded input attributes with its label", async () => {
    const user = userEvent.setup();
    render(<><label htmlFor="name">Name</label><Input id="name" required /></>);
    const input = screen.getByRole("textbox", { name: "Name" });
    await user.type(input, "Metric");
    expect(input).toHaveValue("Metric");
    expect(input).toBeRequired();
  });

  it("exposes loading, error, badge and statistic content", () => {
    render(<><Spinner label="Loading projects" /><ErrorNote message="Failed" /><Badge tone="danger">critical</Badge><Stat label="Requests" value="42" hint="per second" /></>);
    expect(screen.getByRole("status")).toHaveTextContent("Loading projects");
    expect(screen.getByRole("alert")).toHaveTextContent("Failed");
    expect(screen.getByText("critical")).toBeInTheDocument();
    expect(screen.getByText("Requests")).toBeInTheDocument();
    expect(screen.getByText("42")).toBeInTheDocument();
    expect(screen.getByText("per second")).toBeInTheDocument();
  });
});
