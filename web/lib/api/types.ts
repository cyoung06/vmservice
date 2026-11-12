/** The shapes of vmservice's API (docs/api.md). */

export type MachineState =
  | "Provisioning"
  | "Starting"
  | "Running"
  | "Stopping"
  | "Stopped"
  | "Error"
  | "Deleting";

export type Machine = {
  readonly id: string;
  readonly name: string;
  readonly image: string;
  readonly imageName: string;
  readonly defaultUser: string;
  readonly size: string;
  readonly cpu: number;
  readonly memoryMiB: number;
  readonly diskGiB: number;
  readonly state: MachineState;
  readonly detail: string;
  readonly createdAt: string;
};

export type CatalogImage = { readonly id: string; readonly name: string; readonly defaultUser: string };
export type CatalogSize = { readonly id: string; readonly name: string; readonly cpu: number; readonly memoryMiB: number };

export type Resources = {
  readonly vms: number;
  readonly cpu: number;
  readonly memoryMiB: number;
  readonly diskGiB: number;
};

export type Catalog = {
  readonly images: readonly CatalogImage[];
  readonly sizes: readonly CatalogSize[];
  readonly disk: { readonly minGiB: number; readonly maxGiB: number; readonly defaultGiB: number };
  readonly limits: Resources;
};

export type SessionInfo = { readonly username: string; readonly expires: string };

export type NewMachineRequest = {
  readonly name: string;
  readonly image: string;
  readonly size: string;
  readonly diskGiB: number;
  readonly password: string;
};
