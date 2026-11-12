import type { Catalog, Machine, Resources } from "@/lib/api/types";
import { formatMemory } from "@/lib/formatting/format";
import { classNames } from "@/lib/utilities/class-names";

/** What the caller's machines take, running or not: computed from the live list, so it never lags. */
export const usageOf = (machines: readonly Machine[]): Resources => ({
  vms: machines.length,
  cpu: machines.reduce((sum, machine) => sum + machine.cpu, 0),
  memoryMiB: machines.reduce((sum, machine) => sum + machine.memoryMiB, 0),
  diskGiB: machines.reduce((sum, machine) => sum + machine.diskGiB, 0),
});

const Meter = ({ label, used, limit, format }: {
  readonly label: string;
  readonly used: number;
  readonly limit: number;
  readonly format: (value: number) => string;
}) => {
  const share = limit > 0 ? Math.min(100, Math.round((used / limit) * 100)) : 100;
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-border bg-surface p-3">
      <div className="text-2xs font-medium uppercase tracking-wide text-foreground-quaternary">{label}</div>
      <div className="text-xs text-foreground-tertiary">
        <span className="text-base font-semibold tabular-nums text-foreground">{format(used)}</span> of {format(limit)}
      </div>
      <progress
        value={share}
        max={100}
        aria-label={`${label}: ${share}%`}
        className={classNames(
          "h-1 w-full overflow-hidden rounded-full [&::-webkit-progress-bar]:bg-background-tertiary",
          share >= 100
            ? "[&::-moz-progress-bar]:bg-danger [&::-webkit-progress-value]:bg-danger"
            : "[&::-moz-progress-bar]:bg-accent [&::-webkit-progress-value]:bg-accent",
        )}
      />
    </div>
  );
};

export const UsageSummary = ({ catalog, machines }: { readonly catalog: Catalog; readonly machines: readonly Machine[] }) => {
  const used = usageOf(machines);
  const { limits } = catalog;
  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
      <Meter label="Machines" used={used.vms} limit={limits.vms} format={String} />
      <Meter label="vCPU" used={used.cpu} limit={limits.cpu} format={String} />
      <Meter label="Memory" used={used.memoryMiB} limit={limits.memoryMiB} format={formatMemory} />
      <Meter label="Disk" used={used.diskGiB} limit={limits.diskGiB} format={(value) => `${value} GiB`} />
    </div>
  );
};
