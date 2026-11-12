"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { useState } from "react";

import { Details } from "@/components/data/details";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { DialogFrame } from "@/components/ui/dialog-frame";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { post, remove } from "@/lib/api/http";
import type { Machine } from "@/lib/api/types";
import { formatCpu, formatMemory, formatTime } from "@/lib/formatting/format";
import { useAction } from "@/lib/hooks/use-action";
import { classNames } from "@/lib/utilities/class-names";

import { LiveNotice } from "./live-notice";
import { MachineStateBadge } from "./machine-state";
import { useMachines } from "./machines-context";
import { SerialConsole } from "./serial-console";
import { VncConsole } from "./vnc-console";

type ConsoleKind = "serial" | "vnc";

const Section = ({ title, children }: { readonly title: string; readonly children: React.ReactNode }) => (
  <section className="flex flex-col gap-3">
    <h2 className="text-[13px] font-medium text-foreground">{title}</h2>
    {children}
  </section>
);

const DeleteDialog = ({ machine, onClose }: { readonly machine: Machine; readonly onClose: () => void }) => {
  const router = useRouter();
  const action = useAction();
  return (
    <DialogFrame
      title={`Delete ${machine.name}?`}
      onClose={onClose}
      submitLabel="Delete machine"
      danger
      pending={action.pending}
      error={action.error}
      onSubmit={() =>
        void action.run(() => remove(`/api/vms/${encodeURIComponent(machine.id)}`)).then((done) => {
          if (done) {
            router.push("/");
          }
        })
      }
    >
      <p className="text-[13px] text-foreground-secondary">
        The machine and its {machine.diskGiB} GiB disk are deleted, with everything on it. This can&apos;t be undone.
      </p>
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

const ConsoleTabs = ({ machine }: { readonly machine: Machine }) => {
  const [kind, setKind] = useState<ConsoleKind>("serial");
  const tab = (value: ConsoleKind, label: string) => (
    <button
      type="button"
      role="tab"
      aria-selected={kind === value}
      onClick={() => setKind(value)}
      className={classNames(
        "h-7 rounded-md px-2.5 text-[13px] transition-colors",
        kind === value
          ? "bg-background-quaternary/70 font-medium text-foreground"
          : "text-foreground-secondary hover:bg-background-tertiary hover:text-foreground",
      )}
    >
      {label}
    </button>
  );
  if (machine.state !== "Running") {
    return (
      <p className="rounded-lg border border-border bg-background-secondary px-4 py-6 text-center text-xs text-foreground-tertiary">
        The consoles open once the machine is running.
      </p>
    );
  }
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <div role="tablist" aria-label="Console" className="flex gap-0.5">
          {tab("serial", "Serial")}
          {tab("vnc", "VNC")}
        </div>
        <span className="flex-1" />
        <span className="text-xs text-foreground-tertiary">
          Sign in as <span className="font-mono text-foreground-secondary">{machine.defaultUser}</span> with the password
          you set{kind === "serial" ? "; press Enter if there is no prompt" : ""}.
        </span>
      </div>
      {kind === "serial" ? <SerialConsole machineId={machine.id} /> : <VncConsole machineId={machine.id} />}
    </div>
  );
};

export const MachineDetail = () => {
  const id = useSearchParams().get("id") ?? "";
  const { machines } = useMachines();
  const machine = machines?.find((item) => item.id === id) ?? null;

  return (
    <>
      <PageHeader
        title={machine?.name ?? id}
        icon={<Icon name="server" size={15} />}
        breadcrumbs={["Machines"]}
      />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        {machines === null ? (
          <div className="mx-auto flex max-w-[1080px] flex-col gap-4 px-4 py-5 md:px-8 md:py-8">
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
          <div className="mx-auto flex max-w-[1080px] flex-col gap-8 px-4 py-5 md:px-8 md:py-8">
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
            <Section title="Console">
              <ConsoleTabs machine={machine} />
            </Section>
          </div>
        )}
      </div>
    </>
  );
};
