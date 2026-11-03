import { describe, expect, it } from "vitest";

import { AUDIT_PAGE_SIZE, buildAuditQuery } from "./api";

function parse(filters: Parameters<typeof buildAuditQuery>[0]) {
  return Object.fromEntries(new URLSearchParams(buildAuditQuery(filters)));
}

describe("buildAuditQuery", () => {
  it("always sends a page limit", () => {
    expect(buildAuditQuery()).toBe(`limit=${AUDIT_PAGE_SIZE}`);
  });

  it("carries action, project, and cursor filters", () => {
    expect(
      parse({
        action: "access.denied",
        projectId: "p1",
        before: "2026-01-01T00:00:00.000Z",
        beforeId: 42,
        limit: 5,
      }),
    ).toEqual({
      action: "access.denied",
      project_id: "p1",
      before: "2026-01-01T00:00:00.000Z",
      before_id: "42",
      limit: "5",
    });
  });

  it("omits empty filters", () => {
    const query = parse({ action: "", projectId: "" });
    expect(query).not.toHaveProperty("action");
    expect(query).not.toHaveProperty("project_id");
    expect(query).not.toHaveProperty("before");
    expect(query).not.toHaveProperty("before_id");
  });

  it("only sends before_id alongside before", () => {
    expect(parse({ beforeId: 7 })).not.toHaveProperty("before_id");
  });
});
