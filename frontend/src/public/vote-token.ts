import { issueVoteToken } from "@/lib/api/public-client";

export type VoteOutcome = "accepted" | "duplicate";

type VoteRecord = {
  token?: string;
  expiresAt?: string;
  outcome?: VoteOutcome;
};

const storagePrefix = "tv-poll:vote:";
const memoryRecords = new Map<string, VoteRecord>();
const pendingTokens = new Map<string, Promise<string>>();

function storageKey(pollId: string) {
  return `${storagePrefix}${pollId}`;
}

function readStoredRecord(pollId: string): VoteRecord {
  try {
    const value = window.localStorage.getItem(storageKey(pollId));
    if (!value) {
      return {};
    }

    const parsed = JSON.parse(value) as unknown;
    if (typeof parsed !== "object" || parsed === null) {
      return {};
    }

    const record = parsed as Record<string, unknown>;
    return {
      token: typeof record.token === "string" ? record.token : undefined,
      expiresAt: typeof record.expiresAt === "string" ? record.expiresAt : undefined,
      outcome: record.outcome === "accepted" || record.outcome === "duplicate" ? record.outcome : undefined,
    };
  } catch {
    return {};
  }
}

function readRecord(pollId: string) {
  return { ...readStoredRecord(pollId), ...memoryRecords.get(pollId) };
}

function writeRecord(pollId: string, record: VoteRecord) {
  memoryRecords.set(pollId, record);

  try {
    window.localStorage.setItem(storageKey(pollId), JSON.stringify(record));
  } catch {
    // The in-memory copy and cookie remain available when storage is blocked.
  }
}

function removeRecord(pollId: string) {
  memoryRecords.delete(pollId);

  try {
    window.localStorage.removeItem(storageKey(pollId));
  } catch {
    // Storage can be unavailable in privacy modes.
  }
}

function validToken(record: VoteRecord, now = Date.now()) {
  if (!record.token || !record.expiresAt) {
    return null;
  }

  return Date.parse(record.expiresAt) > now ? record.token : null;
}

export function getStoredVoteOutcome(pollId: string) {
  const record = readRecord(pollId);

  if (!record.outcome) {
    return null;
  }

  if (!record.expiresAt || Date.parse(record.expiresAt) <= Date.now()) {
    removeRecord(pollId);
    return null;
  }

  return record.outcome;
}

export function getOrIssueVoteToken(pollId: string) {
  const record = readRecord(pollId);
  const existingToken = validToken(record);

  if (existingToken) {
    return Promise.resolve(existingToken);
  }

  const pending = pendingTokens.get(pollId);
  if (pending) {
    return pending;
  }

  const request = issueVoteToken(pollId)
    .then((response) => {
      const tokenFromAnotherTab = validToken(readStoredRecord(pollId));
      if (tokenFromAnotherTab) {
        return tokenFromAnotherTab;
      }

      const nextRecord = {
        ...readRecord(pollId),
        token: response.token,
        expiresAt: response.expires_at,
      };
      writeRecord(pollId, nextRecord);
      return response.token;
    })
    .finally(() => {
      pendingTokens.delete(pollId);
    });

  pendingTokens.set(pollId, request);
  return request;
}

export function resetVoteToken(pollId: string) {
  const record = readRecord(pollId);
  const nextRecord = { outcome: record.outcome };
  writeRecord(pollId, nextRecord);
}

export function markVoteOutcome(pollId: string, outcome: VoteOutcome, closesAt: string) {
  const expiresAt = new Date(Date.parse(closesAt) + 60_000).toISOString();
  writeRecord(pollId, { outcome, expiresAt });
}
