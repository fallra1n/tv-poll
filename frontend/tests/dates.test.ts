import { describe, expect, test } from "bun:test";

import { formatCountdown, getPollPhase, localDateTimeToIso } from "@/lib/dates";

const poll = {
  opens_at: "2026-09-09T10:00:00.000Z",
  closes_at: "2026-09-09T10:05:00.000Z",
};

describe("poll dates", () => {
  test("derives all public phases at exact boundaries", () => {
    expect(getPollPhase(poll, Date.parse("2026-09-09T09:59:59.999Z"))).toBe("scheduled");
    expect(getPollPhase(poll, Date.parse(poll.opens_at))).toBe("open");
    expect(getPollPhase(poll, Date.parse("2026-09-09T10:04:59.999Z"))).toBe("open");
    expect(getPollPhase(poll, Date.parse(poll.closes_at))).toBe("closed");
  });

  test("rejects malformed voting windows", () => {
    expect(() => getPollPhase({ opens_at: poll.closes_at, closes_at: poll.opens_at })).toThrow();
    expect(() => getPollPhase({ opens_at: "invalid", closes_at: poll.closes_at })).toThrow();
  });

  test("formats short countdowns without negative values", () => {
    const now = Date.parse("2026-09-09T10:00:00.000Z");
    expect(formatCountdown("2026-09-09T10:01:05.000Z", now)).toBe("1:05");
    expect(formatCountdown("2026-09-09T09:59:00.000Z", now)).toBe("0 сек");
  });

  test("converts valid local values and rejects empty values", () => {
    expect(localDateTimeToIso("")).toBeNull();
    expect(localDateTimeToIso("2026-09-09T12:30")).toMatch(/^2026-09-09T/);
  });
});
