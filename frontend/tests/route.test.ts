import { describe, expect, test } from "bun:test";

import { getPollIdFromPath } from "@/public/route";

const pollId = "123e4567-e89b-12d3-a456-426614174000";

describe("public poll route", () => {
  test("extracts a UUID with an optional trailing slash", () => {
    expect(getPollIdFromPath(`/polls/${pollId}`)).toBe(pollId);
    expect(getPollIdFromPath(`/polls/${pollId}/`)).toBe(pollId);
  });

  test("rejects malformed and nested routes", () => {
    expect(getPollIdFromPath("/polls/not-a-uuid")).toBeNull();
    expect(getPollIdFromPath(`/polls/${pollId}/extra`)).toBeNull();
    expect(getPollIdFromPath("/admin")).toBeNull();
    expect(getPollIdFromPath("/polls/%E0%A4%A")).toBeNull();
  });
});
