import { afterEach, describe, expect, mock, test } from "bun:test";

import { listPolls } from "@/lib/api/admin-client";
import { ApiError } from "@/lib/api/http";
import { castVote, getPublicPoll, isVoteRejected } from "@/lib/api/public-client";

const originalFetch = globalThis.fetch;
const pollId = "123e4567-e89b-12d3-a456-426614174001";

function installFetchMock(implementation: (input: string | URL | Request, init?: RequestInit) => Promise<Response>) {
  const fetchMock = mock(implementation);
  globalThis.fetch = Object.assign(fetchMock, { preconnect: originalFetch.preconnect });
  return fetchMock;
}

afterEach(() => {
  globalThis.fetch = originalFetch;
});

describe("API client", () => {
  test("does not attach credentials to the cacheable poll GET", async () => {
    const fetchMock = installFetchMock(async (_input, init) => {
      expect(init?.credentials).toBe("omit");
      return Response.json({
        id: pollId,
        question: "Вопрос?",
        options: [{ id: 1, label: "Да" }, { id: 2, label: "Нет" }],
        opens_at: "2020-01-01T00:00:00Z",
        closes_at: "2099-01-01T00:00:00Z",
      });
    });
    await getPublicPoll(pollId);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  test("sends both credential mode and vote token header", async () => {
    installFetchMock(async (_input, init) => {
      const headers = new Headers(init?.headers);
      expect(init?.credentials).toBe("include");
      expect(headers.get("X-Vote-Token")).toBe("signed-token");
      expect(JSON.parse(String(init?.body))).toEqual({ option_id: 2 });
      return Response.json({ status: "accepted", option_id: 2 }, { status: 201 });
    });
    await castVote(pollId, 2, "signed-token");
  });

  test("adds bearer auth and list query parameters", async () => {
    installFetchMock(async (input, init) => {
      const url = new URL(String(input));
      const headers = new Headers(init?.headers);
      expect(headers.get("Authorization")).toBe("Bearer admin-secret");
      expect(url.searchParams.get("state")).toBe("open");
      expect(url.searchParams.get("limit")).toBe("20");
      expect(url.searchParams.get("offset")).toBe("40");
      return Response.json({ items: [], total: 0 });
    });
    await listPolls("admin-secret", { state: "open", limit: 20, offset: 40 });
  });

  test("preserves structured errors and Retry-After", async () => {
    installFetchMock(async () => Response.json(
      { status: "rejected", reason: "rate_limited" },
      { status: 429, headers: { "Retry-After": "7" } },
    ));

    try {
      await castVote(pollId, 1, "signed-token");
      throw new Error("Expected castVote to reject");
    } catch (error: unknown) {
      expect(error).toBeInstanceOf(ApiError);
      expect((error as ApiError).status).toBe(429);
      expect((error as ApiError).retryAfterSeconds).toBe(7);
      expect((error as ApiError).body).toEqual({ status: "rejected", reason: "rate_limited" });
    }
  });

  test("rejects a malformed success response instead of showing false acceptance", async () => {
    installFetchMock(async () => Response.json({ status: "accepted", option_id: 1 }, { status: 201 }));

    await expect(castVote(pollId, 2, "signed-token")).rejects.toThrow("invalid vote confirmation");
  });

  test("carries the previously counted option on a duplicate vote", async () => {
    installFetchMock(async () => Response.json(
      { status: "rejected", reason: "duplicate", option_id: 1 },
      { status: 409 },
    ));

    try {
      await castVote(pollId, 2, "signed-token");
      throw new Error("Expected castVote to reject");
    } catch (error: unknown) {
      expect(error).toBeInstanceOf(ApiError);
      const body = (error as ApiError).body;
      expect(isVoteRejected(body)).toBe(true);
      if (isVoteRejected(body) && body.reason === "duplicate") {
        expect(body.option_id).toBe(1);
      }
    }
  });
});
