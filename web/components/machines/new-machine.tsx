"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";

import { FormField } from "@/components/form/form-field";
import { SelectField } from "@/components/form/select-field";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { TextInput } from "@/components/ui/text-input";
import { loadCatalog } from "@/lib/api/catalog";
import { loadKeys } from "@/lib/api/keys";
import { post } from "@/lib/api/http";
import { formatBytes, kindLabel, profileLabel, profileLabels } from "@/lib/api/media";
import type { Catalog, Machine, Medium, NewMachineRequest } from "@/lib/api/types";
import { classNames } from "@/lib/utilities/class-names";
import { formatCpu, formatMemory } from "@/lib/formatting/format";
import { useAction } from "@/lib/hooks/use-action";
import { useResource } from "@/lib/hooks/use-resource";
import { machinePath } from "@/lib/paths";

import { useMachines } from "./machines-context";
import { UsageSummary, usageOf } from "./usage";

const passwordMinimum = 8;
const passwordMaximum = 128;
const nameMaximum = 63;

/** Why the chosen machine doesn't fit within the limits, or null when it does. */
const overLimit = (catalog: Catalog, machines: readonly Machine[], cpu: number, memoryMiB: number, diskGiB: number): string | null => {
  const used = usageOf(machines);
  const { limits } = catalog;
  const reasons: string[] = [];
  if (used.vms + 1 > limits.vms) {
    reasons.push(`you already have ${used.vms} of ${limits.vms} machines`);
  }
  if (used.cpu + cpu > limits.cpu) {
    reasons.push(`it needs ${cpu} vCPU and ${Math.max(0, limits.cpu - used.cpu)} are left`);
  }
  if (used.memoryMiB + memoryMiB > limits.memoryMiB) {
    reasons.push(
      `it needs ${formatMemory(memoryMiB)} of memory and ${formatMemory(Math.max(0, limits.memoryMiB - used.memoryMiB))} is left`,
    );
  }
  if (used.diskGiB + diskGiB > limits.diskGiB) {
    reasons.push(`it needs ${diskGiB} GiB of disk and ${Math.max(0, limits.diskGiB - used.diskGiB)} GiB is left`);
  }
  return reasons.length > 0 ? `Over your limits: ${reasons.join("; ")}. Delete a machine or pick a smaller size.` : null;
};

type Source = "image" | "media";

const gibibyte = 1024 ** 3;

/** The smallest root disk a medium needs: a disk image's virtual size, rounded up to whole GiB. */
const minimumDiskFor = (medium: Medium | undefined): number =>
  medium?.kind === "disk" && medium.virtualBytes > 0 ? Math.ceil(medium.virtualBytes / gibibyte) : 0;

const SourceToggle = ({ value, onChange }: { readonly value: Source; readonly onChange: (value: Source) => void }) => (
  <div role="radiogroup" aria-label="Start from" className="flex gap-1 rounded-md border border-border bg-background p-0.5">
    {(
      [
        ["image", "Image"],
        ["media", "Media (ISO or disk image)"],
      ] as const
    ).map(([option, label]) => (
      <button
        key={option}
        type="button"
        role="radio"
        aria-checked={value === option}
        onClick={() => onChange(option)}
        className={classNames(
          "h-7 flex-1 rounded px-2.5 text-[13px] transition-colors",
          value === option ? "bg-background-tertiary font-medium text-foreground" : "text-foreground-secondary hover:text-foreground",
        )}
      >
        {label}
      </button>
    ))}
  </div>
);

const Form = ({ catalog }: { readonly catalog: Catalog }) => {
  const router = useRouter();
  const { machines, put, media } = useMachines();
  const action = useAction();
  const [name, setName] = useState("");
  const [source, setSource] = useState<Source>("image");
  const [image, setImage] = useState(catalog.images[0]?.id ?? "");
  const readyMedia = (media ?? []).filter((medium) => medium.state === "Ready" && medium.kind !== "");
  const [mediumId, setMediumId] = useState("");
  const chosenMedium = readyMedia.find((medium) => medium.id === mediumId) ?? readyMedia[0];
  const [profileOverride, setProfileOverride] = useState<string | null>(null);
  const profile = profileOverride ?? chosenMedium?.profile ?? "";
  const fromMedia = source === "media";
  // A Windows install sets its own password; there's no cloud-init to take one (or keys).
  const installerSignsIn = fromMedia && chosenMedium?.kind === "iso" && profile === "windows";
  const [size, setSize] = useState(catalog.sizes[0]?.id ?? "");
  const [disk, setDisk] = useState(String(catalog.disk.defaultGiB));
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");

  const chosenImage = catalog.images.find((item) => item.id === image);
  const chosenSize = catalog.sizes.find((item) => item.id === size);
  const diskGiB = Number(disk);
  const diskMinimum = Math.max(catalog.disk.minGiB, fromMedia ? minimumDiskFor(chosenMedium) : 0);
  const diskValid = Number.isInteger(diskGiB) && diskGiB >= diskMinimum && diskGiB <= catalog.disk.maxGiB;
  // Counted in characters (code points) after trimming, as the server counts them.
  const nameLength = [...name.trim()].length;
  const nameValid = nameLength > 0 && nameLength <= nameMaximum;
  // The password is optional when the member has an SSH key: then SSH is the way in, and the
  // console's login prompt can't be passed. Without keys it's the only way in (the server says so too).
  const keys = useResource(loadKeys);
  const hasKeys = (keys.data?.length ?? 0) > 0;
  const passwordLength = [...password].length;
  // Machines from media take no password: an installer sets one (ISO), and a disk image's default
  // user is the image's own, reached with SSH keys, so a disk image needs one (the server says so too).
  const keyMissing = fromMedia && chosenMedium?.kind === "disk" && !hasKeys;
  const passwordValid =
    (fromMedia && !keyMissing) ||
    (!fromMedia && password === "" && hasKeys) ||
    (!fromMedia && passwordLength >= passwordMinimum && passwordLength <= passwordMaximum);
  const matches = fromMedia || password === confirmation;
  const sourceValid = fromMedia ? chosenMedium !== undefined : chosenImage !== undefined;
  const limitProblem =
    machines && chosenSize ? overLimit(catalog, machines, chosenSize.cpu, chosenSize.memoryMiB, diskValid ? diskGiB : 0) : null;
  const ready = nameValid && sourceValid && chosenSize && diskValid && passwordValid && matches && !limitProblem;

  const submit = () => {
    if (!ready) {
      return;
    }
    const body: NewMachineRequest =
      fromMedia && chosenMedium
        ? { name: name.trim(), media: chosenMedium.id, profile, size, diskGiB, password: installerSignsIn ? "" : password }
        : { name: name.trim(), image, size, diskGiB, password };
    const result: { machine: Machine | null } = { machine: null };
    void action
      .run(async () => {
        result.machine = await post<Machine>("/api/vms", body);
      })
      .then((done) => {
        if (done && result.machine) {
          put(result.machine);
          router.push(machinePath(result.machine.id));
        }
      });
  };

  return (
    <form
      className="flex flex-col gap-5"
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      {machines ? <UsageSummary catalog={catalog} machines={machines} /> : null}
      <section className="flex flex-col gap-4 rounded-lg border border-border bg-surface p-4">
        <FormField label="Name" htmlFor="machine-name" hint="Only shown to you; any text up to 63 characters.">
          <TextInput
            id="machine-name"
            data-autofocus=""
            value={name}
            maxLength={nameMaximum * 2}
            onChange={(event) => setName(event.target.value)}
            placeholder="my machine"
            autoComplete="off"
          />
        </FormField>
        <FormField label="Start from">
          <SourceToggle value={source} onChange={setSource} />
        </FormField>
        {fromMedia ? (
          readyMedia.length === 0 ? (
            <p className="rounded-md border border-border bg-background-secondary px-3 py-3 text-xs text-foreground-tertiary">
              {media === null ? "Loading media…" : "No media is ready yet."} Upload an ISO or a disk image, or import
              one from a URL, on the{" "}
              <Link href="/media/" className="text-foreground-secondary underline-offset-2 hover:underline">
                Media
              </Link>{" "}
              page.
            </p>
          ) : (
            <>
              <FormField label="Medium">
                <SelectField
                  label="Medium"
                  items={readyMedia.map((medium) => ({
                    value: medium.id,
                    label: medium.shared ? `${medium.name} (shared)` : medium.name,
                    description: `${kindLabel(medium.kind)} · ${profileLabel(medium.profile)} · ${formatBytes(
                      medium.kind === "disk" && medium.virtualBytes > 0 ? medium.virtualBytes : medium.sizeBytes,
                    )}`,
                  }))}
                  value={chosenMedium?.id ?? ""}
                  onChange={(next) => {
                    setMediumId(next);
                    setProfileOverride(null);
                  }}
                />
              </FormField>
              <FormField
                label="OS profile"
                hint={
                  chosenMedium?.profile
                    ? `Detected: ${profileLabel(chosenMedium.profile)}. Change it only if the detection got it wrong.`
                    : "Not detected; pick the one that fits."
                }
              >
                <SelectField
                  label="OS profile"
                  items={Object.entries(profileLabels).map(([value, label]) => ({
                    value,
                    label,
                    description:
                      value === "windows"
                        ? "UEFI, Secure Boot, TPM, Hyper-V; virtio driver CD"
                        : value === "linux"
                          ? "UEFI, virtio; cloud-init disk attached"
                          : value === "uefi"
                            ? "UEFI firmware, virtio devices"
                            : "Legacy BIOS firmware, virtio devices",
                  }))}
                  value={profile || "uefi"}
                  onChange={setProfileOverride}
                />
              </FormField>
              {chosenMedium?.kind === "iso" ? (
                <p className="text-xs text-foreground-tertiary">
                  The machine gets a blank disk of the size below and the ISO as a CD-ROM to install from, over the VNC
                  console. Eject the install media when you&apos;re done.
                </p>
              ) : (
                <p className="text-xs text-foreground-tertiary">
                  The disk image is copied into the machine&apos;s disk and booted. Your keys and password apply if the
                  image runs cloud-init.
                </p>
              )}
            </>
          )
        ) : (
          <FormField label="Image">
            <SelectField
              label="Image"
              items={catalog.images.map((item) => ({ value: item.id, label: item.name }))}
              value={image}
              onChange={setImage}
            />
          </FormField>
        )}
        <FormField label="Size">
          <SelectField
            label="Size"
            items={catalog.sizes.map((item) => ({
              value: item.id,
              label: item.name,
              description: `${formatCpu(item.cpu)} · ${formatMemory(item.memoryMiB)}`,
            }))}
            value={size}
            onChange={setSize}
          />
        </FormField>
        <FormField
          label="Disk (GiB)"
          htmlFor="machine-disk"
          hint={`Between ${diskMinimum} and ${catalog.disk.maxGiB} GiB${
            diskMinimum > catalog.disk.minGiB ? " (the disk image needs at least that much)" : ""
          }.`}
        >
          <TextInput
            id="machine-disk"
            type="number"
            inputMode="numeric"
            min={diskMinimum}
            max={catalog.disk.maxGiB}
            step={1}
            value={disk}
            onChange={(event) => setDisk(event.target.value)}
            aria-invalid={!diskValid || undefined}
          />
        </FormField>
      </section>
      {fromMedia ? (
        <section className="flex flex-col gap-1 rounded-lg border border-border bg-surface p-4">
          <h2 className="text-[13px] font-medium text-foreground">Signing in</h2>
          {installerSignsIn ? (
            <p className="text-xs text-foreground-tertiary">
              Windows Setup asks for the account and password while it installs; there&apos;s nothing to set here, and your
              SSH keys aren&apos;t added. Install over the VNC console on the machine&apos;s page.
            </p>
          ) : chosenMedium?.kind === "iso" ? (
            <p className="text-xs text-foreground-tertiary">
              The installer asks for the account and password; install over the VNC console on the machine&apos;s page.
              Your SSH keys are offered through cloud-init too, which some installers (like Ubuntu&apos;s autoinstall) use.
            </p>
          ) : keyMissing ? (
            <p className="text-xs text-danger">
              Add an{" "}
              <Link href="/keys/" className="underline underline-offset-2">
                SSH key
              </Link>{" "}
              first: it&apos;s how you sign in to a machine made from a disk image (its default user is the image&apos;s own,
              and no password is set).
            </p>
          ) : (
            <p className="text-xs text-foreground-tertiary">
              Your SSH keys are added for the image&apos;s default user, if the image runs cloud-init; no password is set.
            </p>
          )}
        </section>
      ) : (
      <section className="flex flex-col gap-4 rounded-lg border border-border bg-surface p-4">
        <div className="flex flex-col gap-1">
          <h2 className="text-[13px] font-medium text-foreground">Signing in</h2>
          {hasKeys ? (
            <p className="text-xs text-foreground-tertiary">
              Your {keys.data?.length === 1 ? "SSH key is" : `${keys.data?.length} SSH keys are`} added to the machine, for
              SSH through the jump host as{" "}
              <span className="font-mono text-foreground-secondary">{fromMedia ? "the image\u2019s default user" : chosenImage?.defaultUser ?? "the default user"}</span>. A
              password is optional: set one only if you want to sign in at the browser console too (e.g. if
              you break the machine&apos;s network).
            </p>
          ) : (
            <p className="text-xs text-foreground-tertiary">
              You have no{" "}
              <Link href="/keys/" className="text-foreground-secondary underline-offset-2 hover:underline">
                SSH keys
              </Link>{" "}
              yet, so the password is the only way in: you sign in at the machine&apos;s console as{" "}
              <span className="font-mono text-foreground-secondary">{fromMedia ? "the image\u2019s default user" : chosenImage?.defaultUser ?? "the default user"}</span>.
              Add a key to use SSH instead.
            </p>
          )}
          <p className="text-xs text-foreground-tertiary">A password is stored only as a hash and can&apos;t be shown again.</p>
        </div>
        <FormField
          label={hasKeys ? "Password (optional)" : "Password"}
          htmlFor="machine-password"
          hint={`${passwordMinimum}–${passwordMaximum} characters${hasKeys ? ", or leave empty" : ""}.`}
        >
          <TextInput
            id="machine-password"
            type="password"
            value={password}
            maxLength={passwordMaximum * 2}
            onChange={(event) => setPassword(event.target.value)}
            autoComplete="new-password"
          />
        </FormField>
        <FormField label="Repeat password" htmlFor="machine-password-again">
          <TextInput
            id="machine-password-again"
            type="password"
            value={confirmation}
            maxLength={passwordMaximum * 2}
            onChange={(event) => setConfirmation(event.target.value)}
            autoComplete="new-password"
            aria-invalid={(confirmation.length > 0 && !matches) || undefined}
          />
        </FormField>
        {confirmation.length > 0 && !matches ? (
          <p className="text-xs text-danger">The passwords don&apos;t match.</p>
        ) : null}
      </section>
      )}
      <ErrorText message={limitProblem} />
      <ErrorText message={action.error} />
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={() => router.push("/")}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" pending={action.pending} disabled={!ready}>
          Create machine
        </Button>
      </div>
    </form>
  );
};

export const NewMachine = () => {
  const catalog = useResource(loadCatalog);
  return (
    <>
      <PageHeader
        title="New machine"
        icon={<Icon name="plus" size={15} />}
        breadcrumbs={["Machines"]}
      />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-[760px] flex-col gap-5 px-4 py-5 md:px-8 md:py-8">
          <ErrorText message={catalog.error} />
          {catalog.data ? (
            catalog.data.images.length === 0 || catalog.data.sizes.length === 0 ? (
              <ErrorText message="The catalog has no images or no sizes; ask the administrator." />
            ) : (
              <Form catalog={catalog.data} />
            )
          ) : catalog.error ? null : (
            <div className="py-12 text-center text-xs text-foreground-tertiary">Loading…</div>
          )}
        </div>
      </div>
    </>
  );
};
