import { expect, test } from "@playwright/test";

// The rest of this suite (app.spec.ts) mocks http://localhost:8080 so it's
// fast and deterministic without Docker. This file is the deliberate
// exception: it hits a real `make up` backend to verify the properties a
// mock can't — real CORS headers, the real cookie/token exchange, and the
// dedup guarantee the whole design in docs/ai/03-deduplication.md rests on.
// It skips itself (not fails) when the backend isn't running, so
// `bun run test:e2e` without Docker still passes.
const backendBaseUrl = "http://localhost:8080";
const adminToken = process.env.ADMIN_TOKEN ?? "local-dev-admin-token";
const adminHeaders = { Authorization: `Bearer ${adminToken}`, "Content-Type": "application/json" };

let backendAvailable = false;

test.beforeAll(async () => {
  try {
    const response = await fetch(`${backendBaseUrl}/healthz`, { signal: AbortSignal.timeout(2000) });
    backendAvailable = response.ok;
  } catch {
    backendAvailable = false;
  }
});

test.describe("live backend smoke", () => {
  test.beforeEach(() => {
    test.skip(!backendAvailable, `backend is not reachable at ${backendBaseUrl} — run "make up" first to include this test`);
  });

  test("public vote round-trips through a real backend and rejects a replayed token", async ({ page }) => {
    test.setTimeout(60_000);

    const created = await fetch(`${backendBaseUrl}/v1/admin/polls`, {
      method: "POST",
      headers: adminHeaders,
      body: JSON.stringify({
        question: "Smoke test: любимый формат?",
        options: [{ label: "Вариант A" }, { label: "Вариант B" }],
        voting_window_seconds: 30,
      }),
    });
    expect(created.ok).toBe(true);
    const poll = await created.json() as { id: string };

    const scheduledAt = new Date(Date.now() + 2000).toISOString();
    const scheduled = await fetch(`${backendBaseUrl}/v1/admin/polls/${poll.id}/transitions`, {
      method: "POST",
      headers: adminHeaders,
      body: JSON.stringify({ to: "scheduled", scheduled_at: scheduledAt }),
    });
    expect(scheduled.ok).toBe(true);

    // internal/autoopen flips scheduled -> open on its own once
    // scheduled_at passes — no manual "open" transition needed.
    await expect(async () => {
      const detail = await fetch(`${backendBaseUrl}/v1/admin/polls/${poll.id}`, { headers: adminHeaders });
      const body = await detail.json() as { state: string };
      expect(body.state).toBe("open");
    }).toPass({ timeout: 15_000, intervals: [500] });

    let voteToken: string | undefined;
    page.on("request", (request) => {
      if (request.method() === "POST" && request.url().endsWith("/votes")) {
        voteToken = request.headers()["x-vote-token"];
      }
    });

    await page.goto(`/polls/${poll.id}`);
    await page.getByLabel("Вариант A").check();
    await page.getByRole("button", { name: "Отправить ответ" }).click();
    await expect(page.getByRole("heading", { name: "Ответ принят" })).toBeVisible();
    expect(voteToken).toBeTruthy();

    // Replaying the token the browser just used must be rejected as a
    // duplicate carrying the option that was actually counted, not
    // accepted a second time and not the generic Error envelope.
    const replay = await fetch(`${backendBaseUrl}/v1/polls/${poll.id}/votes`, {
      method: "POST",
      headers: { "X-Vote-Token": voteToken ?? "", "Content-Type": "application/json" },
      body: JSON.stringify({ option_id: 2 }),
    });
    expect(replay.status).toBe(409);
    expect(await replay.json()).toEqual({ status: "rejected", reason: "duplicate", option_id: 1 });

    // The same elected scheduler that opened the poll must close it once
    // closes_at arrives; otherwise the admin UI and snapshotter would keep
    // treating an expired poll as open indefinitely.
    await expect(async () => {
      const detail = await fetch(`${backendBaseUrl}/v1/admin/polls/${poll.id}`, { headers: adminHeaders });
      const body = await detail.json() as { state: string };
      expect(body.state).toBe("closed");
    }).toPass({ timeout: 45_000, intervals: [1000] });

    await expect(async () => {
      const results = await fetch(`${backendBaseUrl}/v1/admin/polls/${poll.id}/results`, { headers: adminHeaders });
      const body = await results.json() as { state: string; total_accepted: number };
      expect(body).toMatchObject({ state: "closed", total_accepted: 1 });
    }).toPass({ timeout: 10_000, intervals: [500] });
  });
});
