declare const __TV_POLL_API_URL__: string | undefined;

const compileTimeApiUrl = typeof __TV_POLL_API_URL__ === "undefined" ? undefined : __TV_POLL_API_URL__;
const configuredApiUrl = compileTimeApiUrl ?? process.env.BUN_PUBLIC_API_URL?.trim();

if (!configuredApiUrl) {
  throw new Error("BUN_PUBLIC_API_URL is required");
}

const parsedApiUrl = new URL(configuredApiUrl);

if (parsedApiUrl.protocol !== "http:" && parsedApiUrl.protocol !== "https:") {
  throw new Error("BUN_PUBLIC_API_URL must use http or https");
}

export const apiBaseUrl = configuredApiUrl.replace(/\/$/, "");

export function getApiUrl(path: string) {
  if (!path.startsWith("/")) {
    throw new Error("API path must start with /");
  }

  return `${apiBaseUrl}${path}`;
}
