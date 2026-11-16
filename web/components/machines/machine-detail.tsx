"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useState } from "react";

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
import { ApiError, get, post, remove } from "@/lib/api/http";
import type { Machine } from "@/lib/api/types";
import { formatCpu, formatMemory, formatTime } from "@/lib/formatting/format";
import { profileLabel } from "@/lib/api/images";
import { useAction } from "@/lib/hooks/use-action";
import { messageOf } from "@/lib/errors";
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

const EjectDialog = ({ machine, onClose }: { readonly machine: Machine; readonly onClose: () => void }) => {
  const action = useAction();
  return (
    <DialogFrame
      title="Eject the install media?"
      onClose={onClose}
      submitLabel="Eject"
      pending={action.pending}
      error={action.error}
      onSubmit={() =>
        void action.run(() => post(`/api/vms/${encodeURIComponent(machine.id)}/eject`)).then((done) => {
          if (done) {
            onClose();
          }
        })
      }
    >
      <p className="text-[13px] text-foreground-secondary">
        The ISO{machine.profile === "windows" ? " and the driver CD are" : " is"} removed, so the machine boots from its
        disk.{" "}
        {machine.state === "Running" || machine.state === "Starting"
          ? "The machine restarts now: finish or shut down the installer first."
          : "It takes effect the next time the machine starts."}
      </p>
    </DialogFrame>
  );
};

const Actions = ({ machine }: { readonly machine: Machine }) => {
  const action = useAction();
  const [deleting, setDeleting] = useState(false);
  const [ejecting, setEjecting] = useState(false);
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
        {machine.installMedia && state !== "Deleting" ? (
          <Button onClick={() => setEjecting(true)} leadingIcon={<Icon name="arrow-up" size={12} />}>
            Eject install media
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
      {ejecting ? <EjectDialog machine={machine} onClose={() => setEjecting(false)} /> : null}
    </div>
  );
};

type Tab = "overview" | "console";

/** The account to sign in as: the owner's username on cloud-init machines. */
const userOf = (machine: Machine): string => machine.user || machine.defaultUser || "";

const Overview = ({ machine }: { readonly machine: Machine }) => {
  const { configuration } = useSession();
  // A machine's name is its host name, unique across everyone: ssh -J … user@<name>.
  const { machines: mine } = useMachines();
  const own = !machine.owner || (mine ?? []).some((other) => other.id === machine.id);
  const user = userOf(machine);
  const windows = machine.profile === "windows";
  return (
    <div className="scrollbar-thin min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex max-w-[1080px] flex-col gap-8 px-4 py-5 md:px-8 md:py-6">
        {windows && machine.installMedia ? (
          <Section title="Installing Windows">
            <ol className="flex list-decimal flex-col gap-1.5 pl-5 text-xs text-foreground-tertiary">
              <li>
                Open the Console tab with VNC <em>before</em> the machine starts (or restart it with VNC open), and press a
                key when it says &ldquo;Press any key to boot from CD or DVD&rdquo;. Otherwise it falls back to the
                firmware menu.
              </li>
              <li>
                When Setup says a media driver is missing, or lists no disk: choose{" "}
                <span className="text-foreground-secondary">Load driver</span>, browse to the virtio CD and pick{" "}
                <span className="font-mono text-foreground-secondary">vioscsi</span> (for your Windows version, amd64).
                That one driver serves both the install CD and the disk; load nothing else.
              </li>
              <li>Finish Setup; it sets the account and password. Install the network driver from the same CD afterwards.</li>
              <li>
                Then <span className="text-foreground-secondary">Eject install media</span> (above), so the machine boots
                from its disk.
              </li>
            </ol>
          </Section>
        ) : null}
        <Section title="Details">
          <Details
            items={[
              ...(!own && machine.owner ? ([["Owner", machine.owner]] as [string, React.ReactNode][]) : []),
              ["Image", machine.imageName || machine.media || "—"],
              ...(machine.profile ? ([["OS profile", profileLabel(machine.profile)]] as [string, React.ReactNode][]) : []),
              ["Size", `${formatCpu(machine.cpu)} · ${formatMemory(machine.memoryMiB)}`],
              ["Disk", `${machine.diskGiB} GiB`],
              ...(user && !windows
                ? ([["User", <span key="user" className="font-mono text-xs">{user}</span>]] as [string, React.ReactNode][])
                : []),
              ["Created", formatTime(machine.createdAt)],
              ["ID", <span key="id" className="font-mono text-xs">{machine.id}</span>],
            ]}
          />
        </Section>
        <Section title="Connect">
          {windows ? (
            <div className="flex flex-col gap-3 text-xs text-foreground-tertiary">
              <p>Sign in at the VNC console with the account you made during Windows Setup.</p>
              {configuration.ssh && own ? (
                <div className="flex flex-col gap-1.5">
                  <p>
                    For Remote Desktop: enable <span className="text-foreground-secondary">OpenSSH Server</span> in
                    Windows (Settings → System → Optional features), then tunnel RDP through the jump host and connect
                    your RDP client to <span className="font-mono text-foreground-secondary">localhost</span>:
                  </p>
                  <CopyLine
                    text={`${sshCommand(configuration.ssh, "<windows user>", machine.name)} -L 3389:localhost:3389`}
                  />
                </div>
              ) : null}
            </div>
          ) : (
          <div className="flex flex-col gap-3 text-xs text-foreground-tertiary">
            <p>
              On the console, sign in as{" "}
              <span className="font-mono text-foreground-secondary">{user || "the image's account"}</span> with the password
              set when the machine was made, if one was (only if the image runs cloud-init; an installed system uses the
              account made during installation).
            </p>
            {configuration.ssh && own ? (
              <div className="flex flex-col gap-1.5">
                <p>
                  With SSH, through the jump host to the machine&apos;s name; the machine signs you in with a key you
                  registered under{" "}
                  <Link href="/keys/" className="text-foreground-secondary underline-offset-2 hover:underline">
                    Account
                  </Link>{" "}
                  before it was made (or the password):
                </p>
                <CopyLine text={sshCommand(configuration.ssh, user || "<user>", machine.name)} />
              </div>
            ) : null}
          </div>
          )}
        </Section>
      </div>
    </div>
  );
};

/**
 * The machine: from the live list (own machines, and other members' an admin has seen). An admin
 * opening another member's machine directly has it fetched once (GET /api/vms/{id}); its events
 * keep it current from then on.
 */
const useMachine = (id: string): { readonly machine: Machine | null; readonly loading: boolean; readonly error: string | null } => {
  const { user } = useSession();
  const { machines, allMachines, put } = useMachines();
  const known = allMachines.find((item) => item.id === id) ?? null;
  const [fetched, setFetched] = useState<{ readonly id: string; readonly error: string | null } | null>(null);
  const admin = user.admin === true;
  const wanted = admin && machines !== null && !known && id !== "" && fetched?.id !== id;
  useEffect(() => {
    if (!wanted) {
      return;
    }
    let cancelled = false;
    void get<Machine>(`/api/vms/${encodeURIComponent(id)}`).then(
      (machine) => {
        if (!cancelled) {
          put(machine);
          setFetched({ id, error: null });
        }
      },
      (failure: unknown) => {
        if (!cancelled) {
          setFetched({ id, error: failure instanceof ApiError && failure.status === 404 ? null : messageOf(failure) });
        }
      },
    );
    return () => {
      cancelled = true;
    };
  }, [wanted, id, put]);
  const loading = machines === null || (admin && !known && fetched?.id !== id);
  return { machine: known, loading, error: fetched?.id === id ? fetched.error : null };
};

export const MachineDetail = () => {
  const { user } = useSession();
  const id = useSearchParams().get("id") ?? "";
  const { machine, loading, error } = useMachine(id);
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
      {loading ? (
        <div className="mx-auto flex w-full max-w-[1080px] flex-col gap-4 px-4 py-5 md:px-8 md:py-8">
          <LiveNotice />
          <div className="py-12 text-center text-xs text-foreground-tertiary">Loading…</div>
        </div>
      ) : error && !machine ? (
        <div className="mx-auto w-full max-w-[1080px] px-4 py-5 md:px-8 md:py-8">
          <ErrorText message={error} />
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
                {machine.owner && machine.owner !== user.username ? (
                  <p className="text-xs text-foreground-tertiary">Owned by {machine.owner}</p>
                ) : null}
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
