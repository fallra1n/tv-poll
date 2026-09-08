import { getApiUrl } from "@/lib/config";
import type { ApiErrorBody } from "@/lib/api/types";

const requestTimeoutMilliseconds = 10_000;

export class ApiError extends Error {
  readonly status: number;
  readonly body: unknown;
  readonly retryAfterSeconds: number | null;

  constructor(status: number, message: string, body: unknown, retryAfterSeconds: number | null) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

export class ApiTimeoutError extends Error {
  constructor() {
    super("API request timed out");
    this.name = "ApiTimeoutError";
  }
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isApiErrorBody(value: unknown): value is ApiErrorBody {
  if (!isRecord(value) || !isRecord(value.error)) {
    return false;
  }

  return typeof value.error.code === "string" && typeof value.error.message === "string";
}

function parseRetryAfter(response: Response) {
  const value = response.headers.get("Retry-After");

  if (value === null) {
    return null;
  }

  const seconds = Number(value);
  return Number.isFinite(seconds) && seconds >= 0 ? Math.ceil(seconds) : null;
}

export async function requestJson<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");

  const externalSignal = init.signal;
  const controller = new AbortController();
  let timedOut = false;

  const forwardAbort = () => {
    controller.abort(externalSignal?.reason);
  };

  if (externalSignal?.aborted) {
    forwardAbort();
  } else {
    externalSignal?.addEventListener("abort", forwardAbort, { once: true });
  }

  const timeout = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, requestTimeoutMilliseconds);

  let response: Response;
  try {
    response = await fetch(getApiUrl(path), { ...init, headers, signal: controller.signal });
  } catch (error: unknown) {
    if (timedOut) {
      throw new ApiTimeoutError();
    }
    throw error;
  } finally {
    clearTimeout(timeout);
    externalSignal?.removeEventListener("abort", forwardAbort);
  }

  const text = await response.text();
  let body: unknown = null;

  if (text) {
    try {
      body = JSON.parse(text) as unknown;
    } catch {
      if (response.ok) {
        throw new Error("API returned invalid JSON");
      }
    }
  }

  if (!response.ok) {
    const message = isApiErrorBody(body) ? body.error.message : `API request failed with status ${response.status}`;
    throw new ApiError(response.status, message, body, parseRetryAfter(response));
  }

  if (body === null) {
    throw new Error("API returned an empty response");
  }

  return body as T;
}

export function jsonBody(value: unknown) {
  return {
    body: JSON.stringify(value),
    headers: {
      "Content-Type": "application/json",
    },
  };
}
