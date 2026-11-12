import { get } from "./http";
import type { SshKey } from "./types";

/** The caller's SSH keys, newest first. */
export const loadKeys = (): Promise<SshKey[]> => get<{ keys: SshKey[] }>("/api/keys").then((body) => body.keys);
