"use client";

import { createContext, useContext } from "react";

import type { SessionInfo } from "@/lib/api/types";
import type { Session } from "@/lib/session/session";

export type SignedInSession = Session & { readonly user: SessionInfo };

export const SessionContext = createContext<SignedInSession | null>(null);

/** The signed-in session. Only components below the session gate may ask. */
export const useSession = (): SignedInSession => {
  const session = useContext(SessionContext);
  if (!session) {
    throw new Error("useSession must be used inside SessionGate.");
  }
  return session;
};
