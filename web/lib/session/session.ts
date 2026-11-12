/**
 * Sign-in is vmservice's own OpenID Connect login: "Sign in" goes to /auth/login, which sends the
 * browser to the provider and back, and leaves an encrypted session cookie. `GET /api/session`
 * says who that is; `POST /auth/logout` clears it.
 */

import { ApiError, get, post } from "@/lib/api/http";
import type { SessionInfo } from "@/lib/api/types";

/** `/config.json`, as vmservice serves it. */
export type UiConfiguration = {
  /** Shown in the sidebar and the tab title. */
  readonly title: string;
  /** The provider's name on the sign-in button. */
  readonly loginProvider: string;
};

export type Session = {
  readonly configuration: UiConfiguration;
  /** null: signed out. */
  readonly user: SessionInfo | null;
};

const loadConfiguration = async (): Promise<UiConfiguration> => {
  const response = await fetch("/config.json", { cache: "no-store" });
  if (!response.ok) {
    throw new Error(`Could not load /config.json (${response.status}).`);
  }
  return (await response.json()) as UiConfiguration;
};

export const startSession = async (): Promise<Session> => {
  const configuration = await loadConfiguration();
  const user = await get<SessionInfo>("/api/session").catch((error: unknown) => {
    if (error instanceof ApiError && error.status === 401) {
      return null;
    }
    throw error;
  });
  return { configuration, user };
};

/** Goes to the provider; the server brings the browser back to the page it left. */
export const signIn = (): void => {
  const here = window.location.pathname + window.location.search;
  // /auth/login is the server's, not a page of this export: a full navigation is meant.
  // eslint-disable-next-line @next/next/no-location-assign-relative-destination
  window.location.assign(`/auth/login?next=${encodeURIComponent(here)}`);
};

export const signOut = async (): Promise<void> => {
  await post("/auth/logout");
};
