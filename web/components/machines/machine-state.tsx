import { Badge, type BadgeTone } from "@/components/data/badge";
import { Icon } from "@/components/ui/icon";
import type { MachineState } from "@/lib/api/types";

const tones: Readonly<Record<MachineState, BadgeTone>> = {
  Provisioning: "info",
  Starting: "info",
  Running: "success",
  Stopping: "warning",
  Stopped: "neutral",
  Error: "danger",
  Deleting: "warning",
};

const moving: ReadonlySet<MachineState> = new Set(["Provisioning", "Starting", "Stopping", "Deleting"]);

export const MachineStateBadge = ({ state }: { readonly state: MachineState }) => (
  <Badge tone={tones[state] ?? "neutral"}>
    {moving.has(state) ? <Icon name="spinner" size={10} className="animate-spin" /> : null}
    {state}
  </Badge>
);
