import { describe, expect, it } from "vitest";

import { formatBytes, formatNumber, formatPercent } from "./format";

describe("format helpers", () => {
  it("formats finite numbers with bounded precision", () => {
    expect(formatNumber(12.345, 1)).toBe("12.3");
    expect(formatNumber(Number.NaN)).toBe("—");
  });

  it("formats percentages", () => {
    expect(formatPercent(0.5, 1)).toBe("0.5%");
  });

  it("formats byte sizes", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(2048)).toBe("2 KB");
  });
});
