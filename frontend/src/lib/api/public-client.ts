import { isRecord, requestJson } from "@/lib/api/http";
import type { PollPublic, VoteAccepted, VoteRejected, VoteToken } from "@/lib/api/types";

function pollPath(pollId: string) {
  return `/v1/polls/${encodeURIComponent(pollId)}`;
}

function isPublicPoll(value: unknown, pollId: string): value is PollPublic {
  if (!isRecord(value)
    || value.id !== pollId
    || typeof value.question !== "string"
    || typeof value.opens_at !== "string"
    || typeof value.closes_at !== "string"
    || !Array.isArray(value.options)) {
    return false;
  }

  return value.options.every((option) => isRecord(option)
    && Number.isInteger(option.id)
    && Number(option.id) >= 1
    && typeof option.label === "string");
}

function isVoteToken(value: unknown): value is VoteToken {
  return isRecord(value)
    && typeof value.token === "string"
    && value.token.length > 0
    && typeof value.expires_at === "string"
    && Number.isFinite(Date.parse(value.expires_at));
}

function isVoteAccepted(value: unknown, optionId: number): value is VoteAccepted {
  return isRecord(value)
    && value.status === "accepted"
    && value.option_id === optionId;
}

export function isVoteRejected(value: unknown): value is VoteRejected {
  if (!isRecord(value) || value.status !== "rejected") {
    return false;
  }

  return value.reason === "duplicate" || value.reason === "closed" || value.reason === "rate_limited";
}

export async function getPublicPoll(pollId: string, signal?: AbortSignal) {
  const response = await requestJson<unknown>(pollPath(pollId), {
    credentials: "omit",
    signal,
  });

  if (!isPublicPoll(response, pollId)) {
    throw new Error("API returned an invalid public poll");
  }

  return response;
}

export async function issueVoteToken(pollId: string) {
  const response = await requestJson<unknown>(`${pollPath(pollId)}/token`, {
    method: "POST",
    credentials: "include",
  });

  if (!isVoteToken(response)) {
    throw new Error("API returned an invalid vote token");
  }

  return response;
}

export async function castVote(pollId: string, optionId: number, token: string | null) {
  const headers = new Headers({ "Content-Type": "application/json" });

  if (token) {
    headers.set("X-Vote-Token", token);
  }

  const response = await requestJson<unknown>(`${pollPath(pollId)}/votes`, {
    method: "POST",
    credentials: "include",
    headers,
    body: JSON.stringify({ option_id: optionId }),
  });

  if (!isVoteAccepted(response, optionId)) {
    throw new Error("API returned an invalid vote confirmation");
  }

  return response;
}
