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
  /** Made from media: the medium's id, else absent or "". */
  readonly media?: string;
  /** Made from media: the OS profile ("windows", "linux", "uefi", "bios"). */
  readonly profile?: string;
  /** The install CD-ROMs (ISO, drivers) are still attached; "eject" removes them. */
  readonly installMedia?: boolean;
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

export type NewMachineRequest =
  | {
      readonly name: string;
      readonly image: string;
      readonly size: string;
      readonly diskGiB: number;
      readonly password: string;
    }
  | {
      readonly name: string;
      readonly media: string;
      readonly profile: string;
      readonly size: string;
      readonly diskGiB: number;
      readonly password: string;
    };

export type MediumKind = "iso" | "disk" | "";
export type MediumProfile = "windows" | "linux" | "uefi" | "bios" | "";
export type MediumState = "Uploading" | "Importing" | "Inspecting" | "Ready" | "Failed";

/** An ISO or disk image a member brought, or a shared one from the catalog (GET /api/media). */
export type Medium = {
  readonly id: string;
  readonly name: string;
  readonly shared: boolean;
  readonly source: "upload" | "url" | "shared";
  readonly kind: MediumKind;
  readonly profile: MediumProfile;
  readonly sizeBytes: number;
  readonly virtualBytes: number;
  readonly state: MediumState;
  readonly progress: string;
  readonly detail: string;
  readonly createdAt: string;
};

export type NewMediumRequest =
  | { readonly name: string; readonly source: "upload"; readonly sizeBytes: number; readonly header: string }
  | { readonly name: string; readonly source: "url"; readonly url: string };

/** A member's SSH public key (GET /api/keys). */
export type SshKey = {
  readonly id: string;
  readonly fingerprint: string;
  readonly type: string;
  readonly label: string;
  readonly createdAt: string;
};

export type NewKeyRequest = { readonly publicKey: string; readonly label: string };
