"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useState } from "react";

import { Details } from "@/components/data/details";
import { PageHeader } from "@/components/layout/page-header";
import { useSession } from "@/components/session/session-context";
import { Button } from "@/components/ui/button";
import { CopyLine } from "@/components/ui/copy-line";
import { DialogFrame } from "@/components/ui/dialog-frame";
import { TextInput } from "@/components/ui/text-input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { post, remove } from "@/lib/api/http";
import type { Machine } from "@/lib/api/types";
import { formatCpu, formatMemory, formatTime } from "@/lib/formatting/format";
import { useAction } from "@/lib/hooks/use-action";
import { sshCommand } from "@/lib/paths";
import { classNames } from "@/lib/utilities/class-names";

import { LiveNotice } from "./live-notice";
import { MachineStateBadge } from "./machine-state";
import { useMachines } from "./machines-context";
import { ConsoleTab } from "./console-tab";

const Section = ({ title, children }: { readonly title: string; readonly children: React.ReactNode }) => (
  <section className="flex flex-col gap-3">
    <h2 className="text-[13px] font-medium text-foreground">{title}</h2>
    {children}
  </section>
);

const DeleteDialog = ({ machine, onClose }: { readonly machine: Machine; readonly onClose: () => void }) => {
  const router = useRouter();
  const action = useAction();
  const [typed, setTyped] = useState("");
  // The member types the machine's name to confirm: deleting can't be undone.
  const confirmed = typed === machine.name;
  return (
    <DialogFrame
      title={`Delete ${machine.name}?`}
      onClose={onClose}
      submitLabel="Delete machine"
      danger
      submitDisabled={!confirmed}
      pending={action.pending}
      error={action.error}
      onSubmit={() => {
        if (!confirmed) {
          return;
        }
        void action.run(() => remove(`/api/vms/${encodeURIComponent(machine.id)}`)).then((done) => {
          if (done) {
            router.push("/");
          }
        });
      }}
    >
      <p className="text-[13px] text-foreground-secondary">
        The machine and its {machine.diskGiB} GiB disk are deleted, with everything on it. This can&apos;t be undone.
      </p>
      <label className="mt-3 flex flex-col gap-1.5 text-[13px] text-foreground-secondary" htmlFor="delete-confirm">
        <span>
          Type <span className="font-mono text-foreground">{machine.name}</span> to confirm.
        </span>
        <TextInput
          id="delete-confirm"
          data-autofocus=""
          value={typed}
          onChange={(event) => setTyped(event.target.value)}
          autoComplete="off"
          spellCheck={false}
        />
      </label>
    </DialogFrame>
  );
};

const Actions = ({ machine }: { readonly machine: Machine }) => {
  const action = useAction();
  const [deleting, setDeleting] = useState(false);
  const run = (verb: "start" | "stop" | "restart") =>
    void action.run(() => post(`/api/vms/${encodeURIComponent(machine.id)}/${verb}`));
  const { state } = machine;
  return (
    <div className="flex flex-col items-end gap-2">
      <div className="flex flex-wrap items-center justify-end gap-1">
        {state === "Stopped" ? (
          <Button pending={action.pending} onClick={() => run("start")} leadingIcon={<Icon name="play" size={12} />}>
            Start
          </Button>
        ) : null}
        {state === "Running" || state === "Starting" || state === "Error" ? (
          <Button pending={action.pending} onClick={() => run("stop")} leadingIcon={<Icon name="square" size={12} />}>
            Stop
          </Button>
        ) : null}
        {state === "Running" ? (
          <Button pending={action.pending} onClick={() => run("restart")} leadingIcon={<Icon name="refresh" size={12} />}>
            Restart
          </Button>
        ) : null}
        <Button
          variant="ghost"
          disabled={state === "Deleting"}
          onClick={() => setDeleting(true)}
          leadingIcon={<Icon name="trash" size={12} />}
          className="hover:text-danger"
        >
          Delete
        </Button>
      </div>
      <ErrorText message={action.error} />
      {deleting ? <DeleteDialog machine={machine} onClose={() => setDeleting(false)} /> : null}
    </div>
  );
};

type Tab = "overview" | "console";

const Overview = ({ machine }: { readonly machine: Machine }) => {
  const { configuration } = useSession();
  // ssh -J … user@<name> works when the name can be a host name and no other of the member's
  // machines has it (the bastion's rule).
  const { machines: mine } = useMachines();
  const sshName =
    /^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/.test(machine.name) &&
    (mine ?? []).filter((other) => other.name.toLowerCase() === machine.name.toLowerCase()).length === 1
      ? machine.name
      : null;
  return (
    <div className="scrollbar-thin min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex max-w-[1080px] flex-col gap-8 px-4 py-5 md:px-8 md:py-6">
        <Section title="Details">
          <Details
            items={[
              ["Image", machine.imageName],
              ["Size", `${formatCpu(machine.cpu)} · ${formatMemory(machine.memoryMiB)}`],
              ["Disk", `${machine.diskGiB} GiB`],
              ["User", <span key="user" className="font-mono text-xs">{machine.defaultUser}</span>],
              ["Created", formatTime(machine.createdAt)],
              ["ID", <span key="id" className="font-mono text-xs">{machine.id}</span>],
            ]}
          />
        </Section>
        <Section title="Connect">
          <div className="flex flex-col gap-3 text-xs text-foreground-tertiary">
            <p>
              On the console, sign in as <span className="font-mono text-foreground-secondary">{machine.defaultUser}</span>{" "}
              with the password you set when you made the machine, if you set one.
            </p>
            {configuration.ssh ? (
              <div className="flex flex-col gap-1.5">
                <p>
                  With SSH, through the jump host, using a key you registered under{" "}
                  <Link href="/keys/" className="text-foreground-secondary underline-offset-2 hover:underline">
                    SSH keys
                  </Link>{" "}
                  before the machine was made:
                </p>
                <CopyLine text={sshCommand(configuration.ssh, machine.defaultUser, machine.id)} />
                {sshName ? <CopyLine text={sshCommand(configuration.ssh, machine.defaultUser, sshName)} /> : null}
              </div>
            ) : null}
          </div>
        </Section>
      </div>
    </div>
  );
};

export const MachineDetail = () => {
  const id = useSearchParams().get("id") ?? "";
  const { machines } = useMachines();
  const machine = machines?.find((item) => item.id === id) ?? null;
  const [tab, setTab] = useState<Tab>("overview");

  const tabButton = (value: Tab, label: string) => (
    <button
      type="button"
      role="tab"
      aria-selected={tab === value}
      onClick={() => setTab(value)}
      className={classNames(
        "-mb-px h-9 border-b-2 px-1 text-[13px] transition-colors",
        tab === value
          ? "border-foreground font-medium text-foreground"
          : "border-transparent text-foreground-secondary hover:text-foreground",
      )}
    >
      {label}
    </button>
  );

  return (
    <>
      <PageHeader
        title={machine?.name ?? id}
        icon={<Icon name="server" size={15} />}
        breadcrumbs={["Machines"]}
      />
      {machines === null ? (
        <div className="mx-auto flex w-full max-w-[1080px] flex-col gap-4 px-4 py-5 md:px-8 md:py-8">
          <LiveNotice />
          <div className="py-12 text-center text-xs text-foreground-tertiary">Loading…</div>
        </div>
      ) : !machine ? (
        <EmptyState
          fill
          icon={<Icon name="server" size={18} />}
          title="No such machine"
          description="It was deleted, or it isn't yours."
        />
      ) : (
        <div className="flex min-h-0 flex-1 flex-col">
          <div className="flex flex-col gap-3 border-b border-border px-4 pt-4 md:px-8">
            <LiveNotice />
            <div className="flex flex-wrap items-start gap-4">
              <div className="flex min-w-0 flex-1 flex-col gap-1">
                <div className="flex items-center gap-2">
                  <h2 className="text-base font-semibold text-foreground [overflow-wrap:anywhere]">{machine.name}</h2>
                  <MachineStateBadge state={machine.state} />
                </div>
                {machine.detail ? (
                  <p className={classNames("text-xs", machine.state === "Error" ? "text-danger" : "text-foreground-tertiary")}>
                    {machine.detail}
                  </p>
                ) : null}
              </div>
              <Actions machine={machine} />
            </div>
            <div role="tablist" aria-label="Machine" className="flex gap-4">
              {tabButton("overview", "Overview")}
              {tabButton("console", "Console")}
            </div>
          </div>
          {tab === "overview" ? <Overview machine={machine} /> : <ConsoleTab machine={machine} />}
        </div>
      )}
    </>
  );
};
