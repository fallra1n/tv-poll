import { jsonBody, requestJson } from "@/lib/api/http";
import type {
  PollAdmin,
  PollCreateRequest,
  PollListResponse,
  PollResults,
  PollState,
  PollTransitionRequest,
  ResultsTimeseriesResponse,
} from "@/lib/api/types";

function adminHeaders(token: string) {
  return {
    Authorization: `Bearer ${token}`,
  };
}

export function listPolls(
  token: string,
  parameters: { state?: PollState; limit: number; offset: number },
  signal?: AbortSignal,
) {
  const query = new URLSearchParams({
    limit: String(parameters.limit),
    offset: String(parameters.offset),
  });

  if (parameters.state) {
    query.set("state", parameters.state);
  }

  return requestJson<PollListResponse>(`/v1/admin/polls?${query}`, {
    headers: adminHeaders(token),
    cache: "no-store",
    signal,
  });
}

export function createPoll(token: string, request: PollCreateRequest) {
  const json = jsonBody(request);
  return requestJson<PollAdmin>("/v1/admin/polls", {
    method: "POST",
    headers: { ...adminHeaders(token), ...json.headers },
    body: json.body,
  });
}

export function getAdminPoll(token: string, pollId: string, signal?: AbortSignal) {
  return requestJson<PollAdmin>(`/v1/admin/polls/${encodeURIComponent(pollId)}`, {
    headers: adminHeaders(token),
    cache: "no-store",
    signal,
  });
}

export function transitionPoll(token: string, pollId: string, request: PollTransitionRequest) {
  const json = jsonBody(request);
  return requestJson<PollAdmin>(`/v1/admin/polls/${encodeURIComponent(pollId)}/transitions`, {
    method: "POST",
    headers: { ...adminHeaders(token), ...json.headers },
    body: json.body,
  });
}

export function getPollResults(token: string, pollId: string, signal?: AbortSignal) {
  return requestJson<PollResults>(`/v1/admin/polls/${encodeURIComponent(pollId)}/results`, {
    headers: adminHeaders(token),
    cache: "no-store",
    signal,
  });
}

export function getPollResultsTimeseries(token: string, pollId: string, signal?: AbortSignal) {
  return requestJson<ResultsTimeseriesResponse>(
    `/v1/admin/polls/${encodeURIComponent(pollId)}/results/timeseries`,
    { headers: adminHeaders(token), cache: "no-store", signal },
  );
}
