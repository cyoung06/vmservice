"use client";

import { useSearchParams } from "next/navigation";
import { useCallback, useEffect } from "react";

import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";
import { get } from "@/lib/api/http";
import type { Machine } from "@/lib/api/types";
import { useResource } from "@/lib/hooks/use-resource";
import { consoleKindLabels, type ConsoleKind } from "@/lib/paths";

import { MachineStateBadge } from "./machine-state";
import { SerialConsole } from "./serial-console";
import { VncConsole } from "./vnc-console";

const kindLabels = consoleKindLabels;

/**
 * The popped-out console. The machine is read once (and again when the window regains focus, as
 * useResource does): enough for its name and whether it's running; the console's own connection
 * says the rest, and its Reconnect button starts over.
 */
export const ConsoleWindow = () => {
  const params = useSearchParams();
  const id = params.get("id") ?? "";
  const kind: ConsoleKind = params.get("kind") === "vnc" ? "vnc" : "serial";
  const load = useCallback(() => get<Machine>(`/api/vms/${encodeURIComponent(id)}`), [id]);
  const machine = useResource(load);

  useEffect(() => {
    if (machine.data) {
      document.title = `${machine.data.name} — ${kindLabels[kind]}`;
    }
  }, [machine.data, kind]);

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col bg-background">
      <div className="flex h-10 shrink-0 items-center gap-2 border-b border-border px-3 text-[13px]">
        <Icon name={kind === "vnc" ? "monitor" : "terminal"} size={14} className="text-foreground-tertiary" />
        <span className="truncate font-medium text-foreground">{machine.data?.name ?? id}</span>
        <span className="text-foreground-tertiary">{kindLabels[kind]}</span>
        {machine.data ? <MachineStateBadge state={machine.data.state} /> : null}
        <span className="flex-1" />
        {machine.data ? (
          <span className="hidden text-xs text-foreground-tertiary sm:inline">
            Sign in as <span className="font-mono text-foreground-secondary">{machine.data.defaultUser}</span>
          </span>
        ) : null}
      </div>
      {machine.error && !machine.data ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-3 px-4 text-center">
          <p className="text-[13px] text-danger">{machine.error}</p>
          <Button onClick={() => void machine.reload()} leadingIcon={<Icon name="refresh" size={12} />}>
            Try again
          </Button>
        </div>
      ) : !machine.data ? (
        <div className="flex flex-1 items-center justify-center text-xs text-foreground-tertiary">Loading…</div>
      ) : machine.data.state !== "Running" ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-3 px-4 text-center">
          <p className="text-[13px] text-foreground-secondary">The machine isn&apos;t running.</p>
          <Button onClick={() => void machine.reload()} leadingIcon={<Icon name="refresh" size={12} />}>
            Check again
          </Button>
        </div>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col">
          {kind === "serial" ? <SerialConsole fill machineId={id} /> : <VncConsole fill machineId={id} />}
        </div>
      )}
    </div>
  );
};
