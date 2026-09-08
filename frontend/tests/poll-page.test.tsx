import { afterEach, describe, expect, test } from "bun:test";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { PollPage } from "@/public/poll-page";

const originalFetch = globalThis.fetch;

function setPollPath(pollId: string) {
  // happy-dom's default document origin is about:blank, where
  // history.pushState silently fails to update the path — an absolute
  // navigation is required for window.location.pathname to change.
  window.location.href = `http://localhost/polls/${pollId}`;
}

function pollBody(pollId: string, overrides: Partial<Record<"opens_at" | "closes_at", string>> = {}) {
  return {
    id: pollId,
    question: "Кто победит?",
    options: [{ id: 1, label: "Команда А" }, { id: 2, label: "Команда Б" }],
    opens_at: "2020-01-01T00:00:00Z",
    closes_at: "2099-01-01T00:00:00Z",
    ...overrides,
  };
}

type FetchHandlers = {
  getPoll?: () => Response | Promise<Response>;
  token?: () => Response | Promise<Response>;
  vote?: (body: { option_id: number }) => Response | Promise<Response>;
};

function installFetchMock(pollId: string, handlers: FetchHandlers) {
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(input instanceof Request ? input.url : String(input));

    if (init?.method === "POST" && url.pathname.endsWith("/token")) {
      return handlers.token
        ? handlers.token()
        : Response.json({ token: "signed-token", expires_at: "2099-01-01T00:00:00Z" }, { status: 201 });
    }

    if (init?.method === "POST" && url.pathname.endsWith("/votes")) {
      const body = JSON.parse(String(init.body)) as { option_id: number };
      return handlers.vote
        ? handlers.vote(body)
        : Response.json({ status: "accepted", option_id: body.option_id }, { status: 201 });
    }

    return handlers.getPoll ? handlers.getPoll() : Response.json(pollBody(pollId));
  }) as typeof fetch;
}

afterEach(() => {
  cleanup();
  globalThis.fetch = originalFetch;
  window.localStorage.clear();
});

describe("PollPage", () => {
  test("accepted vote shows the confirmation screen", async () => {
    const pollId = "123e4567-e89b-12d3-a456-426614174101";
    setPollPath(pollId);
    installFetchMock(pollId, {});

    render(<PollPage />);
    await userEvent.click(await screen.findByRole("radio", { name: "Команда А" }));
    await userEvent.click(screen.getByRole("button", { name: "Отправить ответ" }));

    expect(await screen.findByRole("heading", { name: "Ответ принят" })).toBeTruthy();
  });

  test("duplicate vote names the option counted by the first request", async () => {
    const pollId = "123e4567-e89b-12d3-a456-426614174102";
    setPollPath(pollId);
    installFetchMock(pollId, {
      vote: () => Response.json({ status: "rejected", reason: "duplicate", option_id: 2 }, { status: 409 }),
    });

    render(<PollPage />);
    await userEvent.click(await screen.findByRole("radio", { name: "Команда А" }));
    await userEvent.click(screen.getByRole("button", { name: "Отправить ответ" }));

    expect(await screen.findByText(/Учтён вариант «Команда Б»/)).toBeTruthy();
  });

  test("closed poll shows the closed screen and never requests a vote token", async () => {
    const pollId = "123e4567-e89b-12d3-a456-426614174103";
    setPollPath(pollId);
    let tokenRequested = false;
    installFetchMock(pollId, {
      getPoll: () => Response.json(pollBody(pollId, { closes_at: "2020-01-01T00:05:00Z" })),
      token: () => {
        tokenRequested = true;
        return Response.json({ token: "unused", expires_at: "2099-01-01T00:00:00Z" }, { status: 201 });
      },
    });

    render(<PollPage />);

    expect(await screen.findByText("Голосование завершено")).toBeTruthy();
    expect(tokenRequested).toBe(false);
  });

  test("rate-limit warning clears once the server's Retry-After window elapses", async () => {
    const pollId = "123e4567-e89b-12d3-a456-426614174104";
    setPollPath(pollId);
    let voteAttempts = 0;
    installFetchMock(pollId, {
      vote: () => {
        voteAttempts += 1;
        return voteAttempts === 1
          ? Response.json({ status: "rejected", reason: "rate_limited" }, { status: 429, headers: { "Retry-After": "1" } })
          : Response.json({ status: "accepted", option_id: 1 }, { status: 201 });
      },
    });

    render(<PollPage />);
    await userEvent.click(await screen.findByRole("radio", { name: "Команда А" }));
    await userEvent.click(screen.getByRole("button", { name: "Отправить ответ" }));

    expect(await screen.findByText(/Повтор станет доступен через/)).toBeTruthy();

    await waitFor(() => {
      expect(screen.queryByText(/Повтор станет доступен через/)).toBeNull();
    }, { timeout: 3000, interval: 100 });
  });
});
