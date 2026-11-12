"use client";

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
import { post } from "@/lib/api/http";
import type { Catalog, Machine, NewMachineRequest } from "@/lib/api/types";
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

const Form = ({ catalog }: { readonly catalog: Catalog }) => {
  const router = useRouter();
  const { machines, put } = useMachines();
  const action = useAction();
  const [name, setName] = useState("");
  const [image, setImage] = useState(catalog.images[0]?.id ?? "");
  const [size, setSize] = useState(catalog.sizes[0]?.id ?? "");
  const [disk, setDisk] = useState(String(catalog.disk.defaultGiB));
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");

  const chosenImage = catalog.images.find((item) => item.id === image);
  const chosenSize = catalog.sizes.find((item) => item.id === size);
  const diskGiB = Number(disk);
  const diskValid = Number.isInteger(diskGiB) && diskGiB >= catalog.disk.minGiB && diskGiB <= catalog.disk.maxGiB;
  // Counted in characters (code points) after trimming, as the server counts them.
  const nameLength = [...name.trim()].length;
  const nameValid = nameLength > 0 && nameLength <= nameMaximum;
  const passwordLength = [...password].length;
  const passwordValid = passwordLength >= passwordMinimum && passwordLength <= passwordMaximum;
  const matches = password === confirmation;
  const limitProblem =
    machines && chosenSize ? overLimit(catalog, machines, chosenSize.cpu, chosenSize.memoryMiB, diskValid ? diskGiB : 0) : null;
  const ready = nameValid && chosenImage && chosenSize && diskValid && passwordValid && matches && !limitProblem;

  const submit = () => {
    if (!ready) {
      return;
    }
    const body: NewMachineRequest = { name: name.trim(), image, size, diskGiB, password };
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
        <FormField label="Image">
          <SelectField
            label="Image"
            items={catalog.images.map((item) => ({ value: item.id, label: item.name }))}
            value={image}
            onChange={setImage}
          />
        </FormField>
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
          hint={`Between ${catalog.disk.minGiB} and ${catalog.disk.maxGiB} GiB.`}
        >
          <TextInput
            id="machine-disk"
            type="number"
            inputMode="numeric"
            min={catalog.disk.minGiB}
            max={catalog.disk.maxGiB}
            step={1}
            value={disk}
            onChange={(event) => setDisk(event.target.value)}
            aria-invalid={!diskValid || undefined}
          />
        </FormField>
      </section>
      <section className="flex flex-col gap-4 rounded-lg border border-border bg-surface p-4">
        <div className="flex flex-col gap-1">
          <h2 className="text-[13px] font-medium text-foreground">Console login</h2>
          <p className="text-xs text-foreground-tertiary">
            You sign in at the machine&apos;s console as{" "}
            <span className="font-mono text-foreground-secondary">{chosenImage?.defaultUser ?? "the default user"}</span> with
            this password. It is stored only as a hash and can&apos;t be shown again.
          </p>
        </div>
        <FormField label="Password" htmlFor="machine-password" hint={`${passwordMinimum}–${passwordMaximum} characters.`}>
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
