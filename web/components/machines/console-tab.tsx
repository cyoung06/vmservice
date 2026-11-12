"use client";

import { useState } from "react";

import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";
import type { Machine } from "@/lib/api/types";
import { consolePath, type ConsoleKind, consoleKindLabels } from "@/lib/paths";
import { classNames } from "@/lib/utilities/class-names";

import { SerialConsole } from "./serial-console";
import { VncConsole } from "./vnc-console";

type Placement = "embedded" | "popup";

const kindLabels = consoleKindLabels;

/** The member's choice per console kind, kept in this browser only; without storage it's embedded. */
const placementKey = (kind: ConsoleKind) => `vmservice.console.${kind}`;

const readPlacement = (kind: ConsoleKind): Placement => {
  try {
    return window.localStorage.getItem(placementKey(kind)) === "popup" ? "popup" : "embedded";
  } catch {
    return "embedded";
  }
};

const savePlacement = (kind: ConsoleKind, placement: Placement) => {
  try {
    window.localStorage.setItem(placementKey(kind), placement);
  } catch {
    // Private window or blocked storage: the choice lasts for this page only.
  }
};

/** Opens a machine's console in its own window (one window per machine and kind). */
export const openConsoleWindow = (id: string, kind: ConsoleKind) => {
  window.open(consolePath(id, kind), `vmservice-${id}-${kind}`, "popup,width=1024,height=720");
};

const Segment = <Value extends string>({
  label,
  value,
  options,
  onChange,
}: {
  readonly label: string;
  readonly value: Value;
  readonly options: readonly (readonly [Value, string])[];
  readonly onChange: (value: Value) => void;
}) => (
  <div role="tablist" aria-label={label} className="flex gap-0.5">
    {options.map(([option, text]) => (
      <button
        key={option}
        type="button"
        role="tab"
        aria-selected={value === option}
        onClick={() => onChange(option)}
        className={classNames(
          "h-7 rounded-md px-2.5 text-[13px] transition-colors",
          value === option
            ? "bg-background-quaternary/70 font-medium text-foreground"
            : "text-foreground-secondary hover:bg-background-tertiary hover:text-foreground",
        )}
      >
        {text}
      </button>
    ))}
  </div>
);

/**
 * The machine page's Console tab: Serial or VNC, each embedded here (filling the tab) or popped out
 * into its own window, as the member prefers.
 */
export const ConsoleTab = ({ machine }: { readonly machine: Machine }) => {
  const [kind, setKind] = useState<ConsoleKind>("serial");
  // This tab only renders in the browser (behind the session gate), where storage can be read.
  const [placements, setPlacements] = useState<Readonly<Record<ConsoleKind, Placement>>>(() => ({
    serial: readPlacement("serial"),
    vnc: readPlacement("vnc"),
  }));
  const placement = placements[kind];

  const choose = (next: Placement) => {
    setPlacements((previous) => ({ ...previous, [kind]: next }));
    savePlacement(kind, next);
    if (next === "popup") {
      openConsoleWindow(machine.id, kind);
    }
  };

  if (machine.state !== "Running") {
    return (
      <div className="flex flex-1 items-center justify-center px-4">
        <p className="text-xs text-foreground-tertiary">The consoles open once the machine is running.</p>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 px-4 pt-4 pb-4 md:px-8">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <Segment
          label="Console"
          value={kind}
          options={[
            ["serial", "Serial"],
            ["vnc", "VNC"],
          ]}
          onChange={setKind}
        />
        <Segment
          label="Placement"
          value={placement}
          options={[
            ["embedded", "Embedded"],
            ["popup", "Pop out"],
          ]}
          onChange={choose}
        />
        <span className="flex-1" />
        <span className="text-xs text-foreground-tertiary">
          Sign in as <span className="font-mono text-foreground-secondary">{machine.defaultUser}</span> with the password
          you set{kind === "serial" ? "; press Enter if there is no prompt" : ""}.
        </span>
      </div>
      {placement === "embedded" ? (
        <div className="flex min-h-[320px] flex-1 flex-col overflow-hidden rounded-lg border border-border">
          {kind === "serial" ? <SerialConsole fill machineId={machine.id} /> : <VncConsole fill machineId={machine.id} />}
        </div>
      ) : (
        <div className="flex flex-1 flex-col items-center justify-center gap-3 rounded-lg border border-border bg-background-secondary px-4 py-10 text-center">
          <p className="text-[13px] text-foreground-secondary">
            The {kindLabels[kind]} console opens in its own window.
          </p>
          <Button onClick={() => openConsoleWindow(machine.id, kind)} leadingIcon={<Icon name="external" size={12} />}>
            Open {kindLabels[kind]} window
          </Button>
        </div>
      )}
    </div>
  );
};
