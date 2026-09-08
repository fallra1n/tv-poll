import { isUuid } from "@/lib/uuid";

export function getPollIdFromPath(pathname: string) {
  const match = /^\/polls\/([^/]+)\/?$/.exec(pathname);

  if (!match?.[1]) {
    return null;
  }

  let pollId: string;

  try {
    pollId = decodeURIComponent(match[1]);
  } catch {
    return null;
  }

  return isUuid(pollId) ? pollId : null;
}
