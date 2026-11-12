/**
 * Calls to vmservice's API, on its own origin with the session cookie. Every call sends
 * `X-Requested-With: XMLHttpRequest`, which writes require (a cross-origin page can't send it
 * without a CORS preflight the server never answers). A refusal is `{"detail": …}`, shown in the
 * server's words where the action was started.
 */

/** Dispatched on `window` when the server answers 401: the session is gone. */
export const signedOutEvent = "vmservice:signed-out";

export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

const refusalMessage = async (response: Response): Promise<string> => {
  const text = await response.text();
  try {
    const body = JSON.parse(text) as { detail?: unknown };
    if (typeof body.detail === "string" && body.detail) {
      return body.detail;
    }
  } catch {
    // Not JSON: fall through to the raw text.
  }
  const plain = text.replace(/<[^>]*>/g, " ").replace(/\s+/g, " ").trim();
  return plain.slice(0, 300) || `${response.status} ${response.statusText}`;
};

export type Method = "GET" | "POST" | "DELETE";

export const request = async <Result>(method: Method, path: string, json?: unknown): Promise<Result> => {
  const headers: Record<string, string> = {
    Accept: "application/json",
    "X-Requested-With": "XMLHttpRequest",
  };
  let body: string | undefined;
  if (json !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(json);
  }
  const response = await fetch(path, { method, headers, body, credentials: "same-origin", cache: "no-store" });
  if (response.status === 401) {
    window.dispatchEvent(new Event(signedOutEvent));
    throw new ApiError(401, "You are signed out. Sign in again.");
  }
  if (!response.ok) {
    throw new ApiError(response.status, await refusalMessage(response));
  }
  if (response.status === 204) {
    return undefined as Result;
  }
  const text = await response.text();
  return (text ? JSON.parse(text) : undefined) as Result;
};

export const get = <Result>(path: string): Promise<Result> => request<Result>("GET", path);
export const post = <Result>(path: string, json?: unknown): Promise<Result> => request<Result>("POST", path, json);
export const remove = <Result>(path: string): Promise<Result> => request<Result>("DELETE", path);

/** The websocket URL of a path on this origin. */
export const socketUrl = (path: string): string =>
  `${window.location.protocol === "https:" ? "wss:" : "ws:"}//${window.location.host}${path}`;
