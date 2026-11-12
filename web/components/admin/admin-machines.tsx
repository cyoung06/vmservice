"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";

import { Cell, DataTable, Row } from "@/components/data/data-table";
import { PageHeader } from "@/components/layout/page-header";
import { LiveNotice } from "@/components/machines/live-notice";
import { MachineStateBadge } from "@/components/machines/machine-state";
import { useMachines } from "@/components/machines/machines-context";
import { useSession } from "@/components/session/session-context";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { get } from "@/lib/api/http";
import type { Machine } from "@/lib/api/types";
import { messageOf } from "@/lib/errors";
import { formatCpu, formatMemory, formatTime } from "@/lib/formatting/format";
import { machinePath } from "@/lib/paths";

const columns = [
  { heading: "Name" },
  { heading: "Owner", className: "w-[160px]" },
  { heading: "State", className: "w-[130px]" },
  { heading: "Size", className: "hidden sm:table-cell" },
  { heading: "Disk", className: "hidden sm:table-cell w-[90px]" },
  { heading: "Created", className: "hidden lg:table-cell w-[180px]" },
] as const;

/**
 * Every member's machines (admins only). The list is read once from /api/admin/vms; the event
 * stream, which sends admins every machine's changes, keeps it current. Each row opens the normal
 * machine page, where an admin has all the usual actions.
 */
export const AdminMachines = () => {
  const { user } = useSession();
  const { allMachines, putAll } = useMachines();
  const router = useRouter();
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!user.admin) {
      return;
    }
    let cancelled = false;
    void get<{ vms: Machine[] }>("/api/admin/vms").then(
      (body) => {
        if (!cancelled) {
          putAll(body.vms);
          setLoaded(true);
        }
      },
      (failure: unknown) => {
        if (!cancelled) {
          setError(messageOf(failure));
        }
      },
    );
    return () => {
      cancelled = true;
    };
  }, [user.admin, putAll]);

  if (!user.admin) {
    return (
      <>
        <PageHeader title="All machines" icon={<Icon name="layers" size={15} />} />
        <EmptyState fill icon={<Icon name="lock" size={18} />} title="Admins only" description="This page is for administrators." />
      </>
    );
  }

  const ownerOf = (machine: Machine): string => machine.owner || user.username;
  const machines = [...allMachines].sort((a, b) => ownerOf(a).localeCompare(ownerOf(b)) || b.createdAt.localeCompare(a.createdAt));
  return (
    <>
      <PageHeader title="All machines" icon={<Icon name="layers" size={15} />} breadcrumbs={["Admin"]} />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="flex flex-col gap-4 px-4 py-4 md:px-6">
          <LiveNotice />
          <ErrorText message={error} />
        </div>
        {!loaded && !error ? (
          <div className="px-6 py-12 text-center text-xs text-foreground-tertiary">Loading…</div>
        ) : machines.length === 0 ? (
          <EmptyState icon={<Icon name="server" size={18} />} title="No machines" description="Nobody has a machine yet." />
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
                  <Cell muted>{ownerOf(machine)}</Cell>
                  <Cell>
                    <MachineStateBadge state={machine.state} />
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
