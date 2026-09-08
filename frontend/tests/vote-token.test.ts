import { afterEach, describe, expect, mock, test } from "bun:test";

import { getOrIssueVoteToken, getStoredVoteOutcome, markVoteOutcome } from "@/public/vote-token";

const originalFetch = globalThis.fetch;

function installFetchMock(implementation: (input: string | URL | Request, init?: RequestInit) => Promise<Response>) {
  const fetchMock = mock(implementation);
  globalThis.fetch = Object.assign(fetchMock, { preconnect: originalFetch.preconnect });
  return fetchMock;
}

afterEach(() => {
  globalThis.fetch = originalFetch;
  window.localStorage.clear();
});

describe("vote token storage", () => {
  test("coalesces concurrent token requests and stores the result per poll", async () => {
    const pollId = "123e4567-e89b-12d3-a456-426614174010";
    const fetchMock = installFetchMock(async () => Response.json({
      token: "signed-token",
      expires_at: "2099-01-01T00:00:00Z",
    }, { status: 201 }));

    const [first, second] = await Promise.all([
      getOrIssueVoteToken(pollId),
      getOrIssueVoteToken(pollId),
    ]);

    expect(first).toBe("signed-token");
    expect(second).toBe("signed-token");
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(window.localStorage.getItem(`tv-poll:vote:${pollId}`)).toContain("signed-token");
  });

  test("stores only a generic outcome, not the selected option", () => {
    const pollId = "123e4567-e89b-12d3-a456-426614174011";
    markVoteOutcome(pollId, "accepted", "2099-01-01T00:00:00Z");

    expect(getStoredVoteOutcome(pollId)).toBe("accepted");
    expect(window.localStorage.getItem(`tv-poll:vote:${pollId}`)).not.toContain("option");
  });
});
