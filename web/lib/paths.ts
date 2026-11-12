/** Pages of a static export take their object from the query string. */
export const machinePath = (id: string): string => `/machine/?id=${encodeURIComponent(id)}`;

export type ConsoleKind = "serial" | "vnc";

/** How a console kind is named in the UI. */
export const consoleKindLabels: Readonly<Record<ConsoleKind, string>> = { serial: "Serial", vnc: "VNC" };

/** The full-window console page a machine's console pops out into. */
export const consolePath = (id: string, kind: ConsoleKind): string =>
  `/console/?id=${encodeURIComponent(id)}&kind=${kind}`;

/** The SSH command that reaches a machine through the bastion. */
export const sshCommand = (ssh: { readonly host: string; readonly port: number }, user: string, id: string): string =>
  `ssh -J ${ssh.host}:${ssh.port} ${user}@${id}`;
