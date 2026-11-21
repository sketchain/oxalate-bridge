import { describe, expect, it } from "vitest";
import { formatDuration, formatRelative, formatTokens, formatUSD, formatWhen, fromDuration, microToInput, pageCode, parseUSD, toDuration } from "./format";

describe("formatUSD", () => {
  it("uses two decimals from $0.01 and four below", () => {
    expect(formatUSD(10_000)).toBe("$0.01");
    expect(formatUSD(9_999)).toBe("$0.0100");
    expect(formatUSD(2_100)).toBe("$0.0021");
    expect(formatUSD(1_234_567_890)).toBe("$1,234.57");
    expect(formatUSD(0)).toBe("$0.00");
  });
  it("keeps the sign and uses the absolute value for the threshold", () => {
    expect(formatUSD(-5_000_000)).toBe("-$5.00");
    expect(formatUSD(-50)).toBe("-$0.0001");
  });
});

describe("formatTokens", () => {
  it("shows plain, k and M", () => {
    expect(formatTokens(999)).toBe("999");
    expect(formatTokens(1_000)).toBe("1k");
    expect(formatTokens(1_380)).toBe("1.38k");
    expect(formatTokens(45_600)).toBe("45.6k");
    expect(formatTokens(1_250_000)).toBe("1.25M");
    expect(formatTokens(12_500_000)).toBe("12.5M");
  });
});

describe("times", () => {
  const now = new Date(2026, 10, 21, 12, 0, 0);
  it("describes future times relative to now", () => {
    expect(formatRelative(new Date(2026, 10, 24, 12, 0, 0), now, "zh-CN")).toBe("3天后");
    expect(formatRelative(new Date(2026, 10, 24, 12, 0, 0), now, "en")).toBe("in 3 days");
    expect(formatRelative(new Date(2026, 10, 21, 11, 55, 0), now, "en")).toBe("5 minutes ago");
  });
  it("adds the relative hint only to upcoming times", () => {
    expect(formatWhen(new Date(2026, 10, 21, 14, 0).toISOString(), now, "en")).toBe("2026-11-21 14:00（in 2 hours）");
    expect(formatWhen(new Date(2026, 10, 21, 10, 0).toISOString(), now, "en")).toBe("2026-11-21 10:00");
    expect(formatWhen(null, now, "en")).toBe("—");
  });
  it("renders the page code line", () => {
    expect(pageCode("OB-01", now)).toBe("OB-01 · 2026-11-21 SAT");
  });
});

describe("duration and amount inputs", () => {
  it("picks the largest whole unit and converts back", () => {
    expect(toDuration(180)).toEqual({ value: 3, unit: "min" });
    expect(toDuration(86_400)).toEqual({ value: 1, unit: "d" });
    expect(toDuration(90)).toEqual({ value: 90, unit: "s" });
    expect(fromDuration(1.5, "h")).toBe(5400);
    expect(formatDuration(5400)).toBe("1h 30min");
  });
  it("parses typed USD into micro-USD", () => {
    expect(parseUSD("$1,234.5")).toBe(1_234_500_000);
    expect(parseUSD("-2.5")).toBe(-2_500_000);
    expect(parseUSD("abc")).toBeNaN();
    expect(microToInput(1_500_000)).toBe("1.5");
    expect(microToInput(60_000_000)).toBe("60");
  });
});
