"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { ApiError, get, signedOutEvent } from "@/lib/api/http";
import { loadMedia } from "@/lib/api/media";
import type { Machine, Medium } from "@/lib/api/types";
import { messageOf } from "@/lib/errors";

/**
 * The caller's machines, live: one EventSource on /api/vms/events for the whole app. The stream
 * starts with a snapshot and then sends each change; there is no polling. EventSource reconnects
 * by itself after a dropped connection. When the server refuses a reconnect (it closes the stream
 * when the session expires), the session is asked once: a 401 signs the user out, anything else
 * leaves a "stopped" notice with a button to reconnect.
 *
 * Media (ISOs and disk images) ride the same stream: the list is fetched once each time the stream
 * (re)starts, then kept current by its "medium" and "medium-deleted" events.
 */
type MachinesValue = {
  /** null until the first snapshot. Newest first. */
  readonly machines: readonly Machine[] | null;
  /** Live updates stopped and why; null while they run. */
  readonly stopped: string | null;
  readonly reconnect: () => void;
  /** Puts a machine the API just returned into the list, unless the event stream already has it (its state is newer). */
  readonly put: (machine: Machine) => void;
  /** The caller's and the shared media, newest first; null until loaded. */
  readonly media: readonly Medium[] | null;
  /** Why the media list couldn't be loaded, if it couldn't. */
  readonly mediaError: string | null;
  /** Puts a medium the API just returned into the list, unless the stream already has it. */
  readonly putMedium: (medium: Medium) => void;
};

const MachinesContext = createContext<MachinesValue | null>(null);

const newestFirst = <Item extends { readonly createdAt: string }>(items: Iterable<Item>): readonly Item[] =>
  [...items].sort((a, b) => b.createdAt.localeCompare(a.createdAt));

export const MachinesProvider = ({ children }: { readonly children: ReactNode }) => {
  const [byId, setById] = useState<ReadonlyMap<string, Machine> | null>(null);
  const [stopped, setStopped] = useState<string | null>(null);
  const [generation, setGeneration] = useState(0);
  const [mediaById, setMediaById] = useState<ReadonlyMap<string, Medium> | null>(null);
  const [mediaError, setMediaError] = useState<string | null>(null);

  useEffect(() => {
    const source = new EventSource("/api/vms/events");
    let closed = false;
    source.addEventListener("snapshot", (event) => {
      const { vms } = JSON.parse((event as MessageEvent<string>).data) as { vms: Machine[] };
      setById(new Map(vms.map((vm) => [vm.id, vm])));
      setStopped(null);
      // The stream is live now, so no medium change is missed after this read.
      void loadMedia().then(
        (media) => {
          if (!closed) {
            setMediaById(new Map(media.map((medium) => [medium.id, medium])));
            setMediaError(null);
          }
        },
        (error: unknown) => {
          if (!closed) {
            setMediaError(messageOf(error));
          }
        },
      );
    });
    source.addEventListener("medium", (event) => {
      const medium = JSON.parse((event as MessageEvent<string>).data) as Medium;
      setMediaById((previous) => new Map(previous ?? []).set(medium.id, medium));
    });
    source.addEventListener("medium-deleted", (event) => {
      const { id } = JSON.parse((event as MessageEvent<string>).data) as { id: string };
      setMediaById((previous) => {
        const next = new Map(previous ?? []);
        next.delete(id);
        return next;
      });
    });
    source.addEventListener("vm", (event) => {
      const vm = JSON.parse((event as MessageEvent<string>).data) as Machine;
      setById((previous) => new Map(previous ?? []).set(vm.id, vm));
    });
    source.addEventListener("deleted", (event) => {
      const { id } = JSON.parse((event as MessageEvent<string>).data) as { id: string };
      setById((previous) => {
        const next = new Map(previous ?? []);
        next.delete(id);
        return next;
      });
    });
    source.onerror = () => {
      if (source.readyState !== EventSource.CLOSED || closed) {
        return; // Reconnecting by itself.
      }
      void get("/api/session").then(
        () => setStopped("Live updates stopped: the server refused the event stream."),
        (error: unknown) => {
          if (!(error instanceof ApiError && error.status === 401)) {
            setStopped(`Live updates stopped: ${messageOf(error)}`);
          }
          // A 401 has already signed the user out (signedOutEvent).
        },
      );
    };
    const close = () => {
      closed = true;
      source.close();
    };
    window.addEventListener(signedOutEvent, close);
    return () => {
      window.removeEventListener(signedOutEvent, close);
      close();
    };
  }, [generation]);

  const reconnect = useCallback(() => {
    setStopped(null);
    setGeneration((value) => value + 1);
  }, []);

  const put = useCallback((machine: Machine) => {
    setById((previous) => (previous?.has(machine.id) ? previous : new Map(previous ?? []).set(machine.id, machine)));
  }, []);

  const putMedium = useCallback((medium: Medium) => {
    setMediaById((previous) => (previous?.has(medium.id) ? previous : new Map(previous ?? []).set(medium.id, medium)));
  }, []);

  const value = useMemo(
    (): MachinesValue => ({
      machines: byId ? newestFirst(byId.values()) : null,
      stopped,
      reconnect,
      put,
      media: mediaById ? newestFirst(mediaById.values()) : null,
      mediaError,
      putMedium,
    }),
    [byId, stopped, reconnect, put, mediaById, mediaError, putMedium],
  );
  return <MachinesContext.Provider value={value}>{children}</MachinesContext.Provider>;
};

export const useMachines = (): MachinesValue => {
  const value = useContext(MachinesContext);
  if (!value) {
    throw new Error("useMachines must be used inside MachinesProvider.");
  }
  return value;
};
