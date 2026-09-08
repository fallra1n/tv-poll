import { expect, test, type Page, type Route } from "@playwright/test";

const pollId = "123e4567-e89b-12d3-a456-426614174100";
const poll = {
  id: pollId,
  question: "Какой формат передачи вам ближе?",
  options: [
    { id: 1, label: "Интервью" },
    { id: 2, label: "Репортаж" },
  ],
  opens_at: "2020-01-01T00:00:00Z",
  closes_at: "2099-01-01T00:00:00Z",
};

const draftPoll = {
  ...poll,
  state: "draft",
  scheduled_at: null,
  opens_at: null,
  closes_at: null,
  voting_window_seconds: 300,
  created_at: "2026-09-09T10:00:00Z",
  updated_at: "2026-09-09T10:00:00Z",
};

const closedPoll = {
  ...poll,
  state: "closed",
  scheduled_at: "2026-09-09T10:00:00Z",
  voting_window_seconds: 300,
  created_at: "2026-09-09T09:00:00Z",
  updated_at: "2026-09-09T10:05:00Z",
};

async function mockApi(page: Page, handler: (route: Route) => Promise<boolean>) {
  await page.route("http://localhost:8080/**", async (route) => {
    const request = route.request();

    if (request.method() === "OPTIONS") {
      await route.fulfill({
        status: 204,
        headers: {
          "Access-Control-Allow-Origin": "http://localhost:3000",
          "Access-Control-Allow-Credentials": "true",
          "Access-Control-Allow-Headers": "authorization,content-type,x-vote-token",
          "Access-Control-Allow-Methods": "GET,POST,OPTIONS",
        },
      });
      return;
    }

    if (await handler(route)) {
      return;
    }

    await route.fulfill({ status: 404, json: { error: { code: "not_found", message: "Not found" } } });
  });
}

test("public viewer receives a token and casts one anonymous vote", async ({ page }) => {
  let voteTokenHeader: string | undefined;

  await mockApi(page, async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    const corsHeaders = {
      "Access-Control-Allow-Origin": "http://localhost:3000",
      "Access-Control-Allow-Credentials": "true",
    };

    if (pathname === `/v1/polls/${pollId}` && request.method() === "GET") {
      await route.fulfill({ status: 200, json: poll, headers: corsHeaders });
      return true;
    }

    if (pathname.endsWith("/token") && request.method() === "POST") {
      await route.fulfill({
        status: 201,
        json: { token: "e2e-signed-token", expires_at: poll.closes_at },
        headers: corsHeaders,
      });
      return true;
    }

    if (pathname.endsWith("/votes") && request.method() === "POST") {
      voteTokenHeader = request.headers()["x-vote-token"];
      expect(request.postDataJSON()).toEqual({ option_id: 2 });
      await route.fulfill({ status: 201, json: { status: "accepted", option_id: 2 }, headers: corsHeaders });
      return true;
    }

    return false;
  });

  await page.goto(`/polls/${pollId}`);
  await page.getByLabel("Репортаж").check();
  await page.getByRole("button", { name: "Отправить ответ" }).click();

  await expect(page.getByRole("heading", { name: "Ответ принят" })).toBeVisible();
  expect(voteTokenHeader).toBe("e2e-signed-token");
});

test("duplicate response is presented as an already counted vote", async ({ page }) => {
  await mockApi(page, async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    const corsHeaders = {
      "Access-Control-Allow-Origin": "http://localhost:3000",
      "Access-Control-Allow-Credentials": "true",
    };

    if (pathname === `/v1/polls/${pollId}` && request.method() === "GET") {
      await route.fulfill({ status: 200, json: poll, headers: corsHeaders });
      return true;
    }

    if (pathname.endsWith("/token")) {
      await route.fulfill({ status: 201, json: { token: "used-token", expires_at: poll.closes_at }, headers: corsHeaders });
      return true;
    }

    if (pathname.endsWith("/votes")) {
      await route.fulfill({ status: 409, json: { status: "rejected", reason: "duplicate" }, headers: corsHeaders });
      return true;
    }

    return false;
  });

  await page.goto(`/polls/${pollId}`);
  await page.getByLabel("Интервью").check();
  await page.getByRole("button", { name: "Отправить ответ" }).click();

  await expect(page.getByRole("heading", { name: "Ваш голос уже учтён" })).toBeVisible();
});

test("administrator signs in and creates a draft poll", async ({ page }) => {
  let createBody: unknown;

  await mockApi(page, async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const corsHeaders = { "Access-Control-Allow-Origin": "http://localhost:3000" };

    expect(request.headers().authorization).toBe("Bearer admin-token");

    if (url.pathname === "/v1/admin/polls" && request.method() === "GET") {
      await route.fulfill({ status: 200, json: { items: [], total: 0 }, headers: corsHeaders });
      return true;
    }

    if (url.pathname === "/v1/admin/polls" && request.method() === "POST") {
      createBody = request.postDataJSON();
      await route.fulfill({ status: 201, json: draftPoll, headers: corsHeaders });
      return true;
    }

    if (url.pathname === `/v1/admin/polls/${pollId}` && request.method() === "GET") {
      await route.fulfill({ status: 200, json: draftPoll, headers: corsHeaders });
      return true;
    }

    return false;
  });

  await page.goto("/admin/polls/new");
  await page.getByLabel("Токен администратора").fill("admin-token");
  await page.getByRole("button", { name: "Продолжить" }).click();

  await page.getByLabel("Вопрос").fill(poll.question);
  await page.getByRole("textbox", { name: "Вариант 1", exact: true }).fill("Интервью");
  await page.getByRole("textbox", { name: "Вариант 2", exact: true }).fill("Репортаж");
  await page.getByRole("button", { name: "Создать опрос" }).click();

  await expect(page.getByRole("heading", { name: poll.question })).toBeVisible();
  expect(createBody).toEqual({
    question: poll.question,
    options: [{ label: "Интервью" }, { label: "Репортаж" }],
    voting_window_seconds: 300,
  });
});

test("administrator sees final anonymized results and sampling disclosure", async ({ page }) => {
  await mockApi(page, async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    const corsHeaders = { "Access-Control-Allow-Origin": "http://localhost:3000" };

    expect(request.headers().authorization).toBe("Bearer admin-token");

    if (pathname === `/v1/admin/polls/${pollId}`) {
      await route.fulfill({ status: 200, json: closedPoll, headers: corsHeaders });
      return true;
    }

    if (pathname.endsWith("/results/timeseries")) {
      await route.fulfill({
        status: 200,
        json: {
          points: [
            { at: "2026-09-09T10:00:01Z", total_accepted: 10, options: [{ option_id: 1, count: 6 }, { option_id: 2, count: 4 }] },
            { at: "2026-09-09T10:00:02Z", total_accepted: 25, options: [{ option_id: 1, count: 15 }, { option_id: 2, count: 10 }] },
          ],
        },
        headers: corsHeaders,
      });
      return true;
    }

    if (pathname.endsWith("/results")) {
      await route.fulfill({
        status: 200,
        json: {
          poll_id: pollId,
          state: "closed",
          total_accepted: 25,
          rejected: { duplicate: 3, rate_limited: 1 },
          options: [
            { option_id: 1, label: "Интервью", count: 15, share: 0.6 },
            { option_id: 2, label: "Репортаж", count: 10, share: 0.4 },
          ],
          snapshot_at: "2026-09-09T10:05:00Z",
          sampling: { enabled: true, rate: 10 },
        },
        headers: corsHeaders,
      });
      return true;
    }

    return false;
  });

  await page.goto(`/admin/polls/${pollId}`);
  await page.getByLabel("Токен администратора").fill("admin-token");
  await page.getByRole("button", { name: "Продолжить" }).click();

  await expect(page.getByRole("heading", { name: "Результаты" })).toBeVisible();
  await expect(page.getByText("25", { exact: true })).toBeVisible();
  await expect(page.getByText(/сэмплирование 1 из 10/)).toBeVisible();
  await expect(page.getByLabel("График общего числа принятых голосов по времени")).toBeVisible();
});
