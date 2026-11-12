"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { useSession } from "@/components/session/session-context";
import { ApiError, get, signedOutEvent } from "@/lib/api/http";
import { loadImages } from "@/lib/api/images";
import type { Machine, VmImage } from "@/lib/api/types";
import { messageOf } from "@/lib/errors";

/**
 * Machines and images, live: one EventSource on /api/vms/events for the whole app. The stream
 * starts with a snapshot of the caller's machines and then sends each change; there is no polling.
 * EventSource reconnects by itself after a dropped connection. When the server refuses a reconnect
 * (it closes the stream when the session expires), the session is asked once: a 401 signs the user
 * out, anything else leaves a "stopped" notice with a button to reconnect.
 *
 * Admins also get other members' machines' events (those carry "owner"): they're kept apart, in
 * `allMachines`, so the member's own list and usage stay theirs.
 *
 * Images ride the same stream: the list is fetched once each time the stream (re)starts, then kept
 * current by its "image" and "image-deleted" events.
 */
type MachinesValue = {
  /** The caller's own machines; null until the first snapshot. Newest first. */
  readonly machines: readonly Machine[] | null;
  /** Every machine seen: the caller's plus, for admins, other members'. Newest first. */
  readonly allMachines: readonly Machine[];
  /** Live updates stopped and why; null while they run. */
  readonly stopped: string | null;
  readonly reconnect: () => void;
  /** Puts a machine the API just returned into the list, unless the event stream already has it (its state is newer). */
  readonly put: (machine: Machine) => void;
  /** Puts many (an admin's list of every machine), keeping what the stream already has. */
  readonly putAll: (machines: readonly Machine[]) => void;
  /** The caller's and the public images (admins: all), newest first; null until loaded. */
  readonly images: readonly VmImage[] | null;
  /** Why the image list couldn't be loaded, if it couldn't. */
  readonly imagesError: string | null;
  /** Puts an image the API just returned into the list (replacing what's there: it's newer). */
  readonly putImage: (image: VmImage) => void;
};

const MachinesContext = createContext<MachinesValue | null>(null);

const newestFirst = <Item extends { readonly createdAt: string }>(items: Iterable<Item>): readonly Item[] =>
  [...items].sort((a, b) => b.createdAt.localeCompare(a.createdAt));

export const MachinesProvider = ({ children }: { readonly children: ReactNode }) => {
  const { user } = useSession();
  const [byId, setById] = useState<ReadonlyMap<string, Machine> | null>(null);
  const [stopped, setStopped] = useState<string | null>(null);
  const [generation, setGeneration] = useState(0);
  const [imagesById, setImagesById] = useState<ReadonlyMap<string, VmImage> | null>(null);
  const [imagesError, setImagesError] = useState<string | null>(null);

  useEffect(() => {
    const source = new EventSource("/api/vms/events");
    let closed = false;
    source.addEventListener("snapshot", (event) => {
      const { vms } = JSON.parse((event as MessageEvent<string>).data) as { vms: Machine[] };
      setById((previous) => {
        // Keep other members' machines an admin loaded; the snapshot is the caller's own.
        const next = new Map<string, Machine>();
        for (const machine of previous?.values() ?? []) {
          if (machine.owner && machine.owner !== user.username) {
            next.set(machine.id, machine);
          }
        }
        for (const vm of vms) {
          next.set(vm.id, vm);
        }
        return next;
      });
      setStopped(null);
      // The stream is live now, so no image change is missed after this read.
      void loadImages().then(
        (images) => {
          if (!closed) {
            setImagesById(new Map(images.map((image) => [image.id, image])));
            setImagesError(null);
          }
        },
        (error: unknown) => {
          if (!closed) {
            setImagesError(messageOf(error));
          }
        },
      );
    });
    source.addEventListener("image", (event) => {
      const image = JSON.parse((event as MessageEvent<string>).data) as VmImage;
      setImagesById((previous) => new Map(previous ?? []).set(image.id, image));
    });
    source.addEventListener("image-deleted", (event) => {
      const { id } = JSON.parse((event as MessageEvent<string>).data) as { id: string };
      setImagesById((previous) => {
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
  }, [generation, user.username]);

  const reconnect = useCallback(() => {
    setStopped(null);
    setGeneration((value) => value + 1);
  }, []);

  const put = useCallback((machine: Machine) => {
    setById((previous) => (previous?.has(machine.id) ? previous : new Map(previous ?? []).set(machine.id, machine)));
  }, []);

  const putAll = useCallback((machines: readonly Machine[]) => {
    setById((previous) => {
      const next = new Map(previous ?? []);
      for (const machine of machines) {
        if (!next.has(machine.id)) {
          next.set(machine.id, machine);
        }
      }
      return next;
    });
  }, []);

  const putImage = useCallback((image: VmImage) => {
    setImagesById((previous) => new Map(previous ?? []).set(image.id, image));
  }, []);

  const value = useMemo((): MachinesValue => {
    const all = byId ? newestFirst(byId.values()) : [];
    return {
      machines: byId ? all.filter((machine) => !machine.owner || machine.owner === user.username) : null,
      allMachines: all,
      stopped,
      reconnect,
      put,
      putAll,
      images: imagesById ? newestFirst(imagesById.values()) : null,
      imagesError,
      putImage,
    };
  }, [byId, user.username, stopped, reconnect, put, putAll, imagesById, imagesError, putImage]);
  return <MachinesContext.Provider value={value}>{children}</MachinesContext.Provider>;
};

export const useMachines = (): MachinesValue => {
  const value = useContext(MachinesContext);
  if (!value) {
    throw new Error("useMachines must be used inside MachinesProvider.");
  }
  return value;
};
