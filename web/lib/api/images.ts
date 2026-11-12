import { ApiError, get, signedOutEvent } from "./http";
import type { VmImage } from "./types";

/** The caller's images and everyone's public ones (admins: all), newest first. */
export const loadImages = (): Promise<VmImage[]> => get<{ images: VmImage[] }>("/api/images").then((body) => body.images);

/** How many bytes of a file the server wants up front: they say what the file is (docs/api.md). */
export const headerBytes = 64 * 1024;

/** The first 64 KiB of a file, base64, read locally. */
export const readHeader = async (file: File): Promise<string> => {
  const bytes = new Uint8Array(await file.slice(0, headerBytes).arrayBuffer());
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(binary);
};

export type Upload = {
  /** Resolves when the server has the whole file; rejects with an ApiError or an "aborted" Error. */
  readonly done: Promise<void>;
  readonly abort: () => void;
};

/**
 * Streams file as the raw body of PUT /api/images/{id}/upload. XMLHttpRequest rather than fetch:
 * only it reports upload progress. onProgress gets the bytes sent so far.
 */
export const uploadFile = (id: string, file: File, onProgress: (sent: number) => void): Upload => {
  const xhr = new XMLHttpRequest();
  const done = new Promise<void>((resolve, reject) => {
    xhr.open("PUT", `/api/images/${encodeURIComponent(id)}/upload`);
    xhr.setRequestHeader("X-Requested-With", "XMLHttpRequest");
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    xhr.upload.onprogress = (event) => onProgress(event.loaded);
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve();
        return;
      }
      if (xhr.status === 401) {
        window.dispatchEvent(new Event(signedOutEvent));
        reject(new ApiError(401, "You are signed out. Sign in again."));
        return;
      }
      let message = `${xhr.status} ${xhr.statusText}`;
      try {
        const body = JSON.parse(xhr.responseText) as { detail?: unknown };
        if (typeof body.detail === "string" && body.detail) {
          message = body.detail;
        }
      } catch {
        // Not JSON: keep the status line.
      }
      reject(new ApiError(xhr.status, message));
    };
    xhr.onerror = () => reject(new Error("The upload was interrupted (network error)."));
    xhr.onabort = () => reject(new Error("aborted"));
    xhr.send(file);
  });
  return { done, abort: () => xhr.abort() };
};

const units = ["B", "KiB", "MiB", "GiB", "TiB"] as const;

export const formatBytes = (bytes: number): string => {
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return "—";
  }
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value >= 100 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`;
};

export const kindLabel = (kind: VmImage["kind"]): string =>
  kind === "iso" ? "ISO installer" : kind === "disk" ? "Disk image" : "Unknown";

export const profileLabels: Readonly<Record<string, string>> = {
  windows: "Windows",
  linux: "Linux",
  uefi: "Generic (UEFI)",
  bios: "Generic (BIOS)",
};

export const profileLabel = (profile: string): string => profileLabels[profile] ?? (profile || "—");
