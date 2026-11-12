"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";

import { Cell, DataTable, Row } from "@/components/data/data-table";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { loadCatalog } from "@/lib/api/catalog";
import { formatCpu, formatMemory, formatTime } from "@/lib/formatting/format";
import { useResource } from "@/lib/hooks/use-resource";
import { machinePath } from "@/lib/paths";

import { LiveNotice } from "./live-notice";
import { MachineStateBadge } from "./machine-state";
import { useMachines } from "./machines-context";
import { UsageSummary } from "./usage";

const columns = [
  { heading: "Name" },
  { heading: "State", className: "w-[130px]" },
  { heading: "Image", className: "hidden md:table-cell" },
  { heading: "Size", className: "hidden sm:table-cell" },
  { heading: "Disk", className: "hidden sm:table-cell w-[90px]" },
  { heading: "Created", className: "hidden lg:table-cell w-[180px]" },
] as const;

export const MachineList = () => {
  const { machines } = useMachines();
  const catalog = useResource(loadCatalog);
  const router = useRouter();

  const newButton = (
    <Button variant="primary" leadingIcon={<Icon name="plus" size={13} />} onClick={() => router.push("/new")}>
      New machine
    </Button>
  );

  return (
    <>
      <PageHeader title="Machines" icon={<Icon name="server" size={15} />} actions={newButton} />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="flex flex-col gap-4 px-4 py-4 md:px-6">
          <LiveNotice />
          <ErrorText message={catalog.error} />
          {catalog.data && machines ? <UsageSummary catalog={catalog.data} machines={machines} /> : null}
        </div>
        {machines === null ? (
          <div className="px-6 py-12 text-center text-xs text-foreground-tertiary">Loading…</div>
        ) : machines.length === 0 ? (
          <EmptyState
            icon={<Icon name="server" size={18} />}
            title="No machines yet"
            description="Create one from an image; it starts as soon as its disk is ready."
            action={<div className="mt-2">{newButton}</div>}
          />
        ) : (
          <div className="overflow-x-auto">
            <DataTable columns={columns} minWidth="min-w-0">
              {machines.map((machine) => (
                <Row key={machine.id} onClick={() => router.push(machinePath(machine.id))}>
                  <Cell>
                    <Link
                      href={machinePath(machine.id)}
                      onClick={(event) => event.stopPropagation()}
                      className="font-medium text-foreground hover:underline [overflow-wrap:anywhere]"
                    >
                      {machine.name}
                    </Link>
                    <div className="font-mono text-2xs text-foreground-quaternary">{machine.id}</div>
                  </Cell>
                  <Cell>
                    <MachineStateBadge state={machine.state} />
                  </Cell>
                  <Cell className="hidden md:table-cell" muted>
                    {machine.imageName}
                  </Cell>
                  <Cell className="hidden sm:table-cell" muted>
                    {formatCpu(machine.cpu)} · {formatMemory(machine.memoryMiB)}
                  </Cell>
                  <Cell className="hidden sm:table-cell" muted>
                    {machine.diskGiB} GiB
                  </Cell>
                  <Cell className="hidden lg:table-cell" muted>
                    {formatTime(machine.createdAt)}
                  </Cell>
                </Row>
              ))}
            </DataTable>
          </div>
        )}
      </div>
    </>
  );
};
