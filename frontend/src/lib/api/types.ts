import type { components } from "@/lib/api/schema.generated";

export type ApiErrorBody = components["schemas"]["Error"];
export type PollState = components["schemas"]["PollState"];
export type PollOption = components["schemas"]["PollOption"];
export type PollPublic = components["schemas"]["PollPublic"];
export type PollAdmin = components["schemas"]["PollAdmin"];
export type PollCreateRequest = components["schemas"]["PollCreateRequest"];
export type PollTransitionRequest = components["schemas"]["PollTransitionRequest"];
export type VoteToken = components["schemas"]["VoteToken"];
export type VoteAccepted = components["schemas"]["VoteAccepted"];
export type VoteRejected = components["schemas"]["VoteRejected"];
export type PollResults = components["schemas"]["PollResults"];
export type ResultsSnapshotPoint = components["schemas"]["ResultsSnapshotPoint"];

export type PollListResponse = {
  items: PollAdmin[];
  total: number;
};

export type ResultsTimeseriesResponse = {
  points: ResultsSnapshotPoint[];
};
