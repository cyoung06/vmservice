"use client";

import { useState } from "react";

import { Cell, DataTable, Row } from "@/components/data/data-table";
import { FormField } from "@/components/form/form-field";
import { PageHeader } from "@/components/layout/page-header";
import { useSession } from "@/components/session/session-context";
import { Button } from "@/components/ui/button";
import { DialogFrame } from "@/components/ui/dialog-frame";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { TextInput } from "@/components/ui/text-input";
import { get, put } from "@/lib/api/http";
import type { AdminMember, Quota } from "@/lib/api/types";
import { formatMemory, formatTime } from "@/lib/formatting/format";
import { useAction } from "@/lib/hooks/use-action";
import { useResource } from "@/lib/hooks/use-resource";

const loadMembers = (): Promise<AdminMember[]> => get<{ members: AdminMember[] }>("/api/admin/members").then((body) => body.members);

const columns = [
  { heading: "Member" },
  { heading: "Machines", className: "w-[110px]" },
  { heading: "vCPU", className: "w-[110px]" },
  { heading: "Memory", className: "hidden md:table-cell w-[170px]" },
  { heading: "Disk", className: "hidden md:table-cell w-[150px]" },
  { heading: "Last sign-in", className: "hidden lg:table-cell w-[180px]" },
  { heading: "", className: "w-[90px]" },
] as const;

/** "used of limit", where a null limit is the catalog's default. */
const usageText = (used: number, limit: number | null, format: (value: number) => string = String): string =>
  `${format(used)} of ${limit === null ? "default" : format(limit)}`;

type QuotaField = keyof Quota;

const fields: readonly { readonly key: QuotaField; readonly label: string; readonly hint: string }[] = [
  { key: "vms", label: "Machines", hint: "How many machines, running or not." },
  { key: "cpu", label: "vCPU", hint: "vCPUs across all machines." },
  { key: "memoryMiB", label: "Memory (MiB)", hint: "Memory across all machines, in MiB (1024 = 1 GiB)." },
  { key: "diskGiB", label: "Disk (GiB)", hint: "Root disks across all machines, in GiB." },
];

/** Edit a member's quota: an empty field is the catalog's default. */
const QuotaDialog = ({ member, onClose, onSaved }: {
  readonly member: AdminMember;
  readonly onClose: () => void;
  readonly onSaved: () => Promise<void>;
}) => {
  const action = useAction();
  const [values, setValues] = useState<Record<QuotaField, string>>(() => ({
    vms: member.quota.vms === null ? "" : String(member.quota.vms),
    cpu: member.quota.cpu === null ? "" : String(member.quota.cpu),
    memoryMiB: member.quota.memoryMiB === null ? "" : String(member.quota.memoryMiB),
    diskGiB: member.quota.diskGiB === null ? "" : String(member.quota.diskGiB),
  }));
  const parse = (text: string): number | null | undefined => {
    const trimmed = text.trim();
    if (trimmed === "") {
      return null;
    }
    const number = Number(trimmed);
    return Number.isInteger(number) && number >= 0 ? number : undefined;
  };
  const parsed = Object.fromEntries(fields.map(({ key }) => [key, parse(values[key])])) as Record<QuotaField, number | null | undefined>;
  const valid = fields.every(({ key }) => parsed[key] !== undefined);
  return (
    <DialogFrame
      title={`Quota for ${member.username}`}
      onClose={onClose}
      submitLabel="Save"
      submitDisabled={!valid}
      pending={action.pending}
      error={action.error}
      onSubmit={() => {
        if (!valid) {
          return;
        }
        const quota: Quota = { vms: parsed.vms ?? null, cpu: parsed.cpu ?? null, memoryMiB: parsed.memoryMiB ?? null, diskGiB: parsed.diskGiB ?? null };
        void action
          .run(() => put(`/api/admin/members/${encodeURIComponent(member.owner)}/quota`, quota))
          .then(async (done) => {
            if (done) {
              await onSaved();
              onClose();
            }
          });
      }}
    >
      <div className="flex flex-col gap-4">
        <p className="text-[13px] text-foreground-secondary">
          Leave a field empty to use the default. Machines they already have stay; the quota applies to new ones.
        </p>
        {fields.map(({ key, label, hint }, index) => (
          <FormField key={key} label={label} htmlFor={`quota-${key}`} hint={hint}>
            <TextInput
              id={`quota-${key}`}
              {...(index === 0 ? { "data-autofocus": "" } : {})}
              type="number"
              inputMode="numeric"
              min={0}
              step={1}
              value={values[key]}
              placeholder="default"
              onChange={(event) => setValues((previous) => ({ ...previous, [key]: event.target.value }))}
              aria-invalid={parsed[key] === undefined || undefined}
            />
          </FormField>
        ))}
      </div>
    </DialogFrame>
  );
};

/** Every member with their usage and quota (admins only). */
export const AdminMembers = () => {
  const { user } = useSession();
  const members = useResource(loadMembers);
  const [editing, setEditing] = useState<AdminMember | null>(null);

  if (!user.admin) {
    return (
      <>
        <PageHeader title="Members" icon={<Icon name="users" size={15} />} />
        <EmptyState fill icon={<Icon name="lock" size={18} />} title="Admins only" description="This page is for administrators." />
      </>
    );
  }

  const list = [...(members.data ?? [])].sort((a, b) => a.username.localeCompare(b.username));
  return (
    <>
      <PageHeader title="Members" icon={<Icon name="users" size={15} />} breadcrumbs={["Admin"]} />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="flex flex-col gap-4 px-4 py-4 md:px-6">
          <p className="text-xs text-foreground-tertiary">
            What each member&apos;s machines take, running or not, against their quota. &ldquo;default&rdquo; is the
            catalog&apos;s limit; set a quota to give someone more (or less).
          </p>
          <ErrorText message={members.error} />
        </div>
        {members.data === null ? (
          members.error ? null : <div className="px-6 py-12 text-center text-xs text-foreground-tertiary">Loading…</div>
        ) : list.length === 0 ? (
          <EmptyState icon={<Icon name="users" size={18} />} title="No members" description="Nobody has signed in yet." />
        ) : (
          <div className="overflow-x-auto">
            <DataTable columns={columns} minWidth="min-w-0">
              {list.map((member) => (
                <Row key={member.owner}>
                  <Cell>
                    <span className="font-medium text-foreground">{member.username}</span>
                  </Cell>
                  <Cell muted>{usageText(member.usage.vms, member.quota.vms)}</Cell>
                  <Cell muted>{usageText(member.usage.cpu, member.quota.cpu)}</Cell>
                  <Cell className="hidden md:table-cell" muted>
                    {usageText(member.usage.memoryMiB, member.quota.memoryMiB, formatMemory)}
                  </Cell>
                  <Cell className="hidden md:table-cell" muted>
                    {usageText(member.usage.diskGiB, member.quota.diskGiB, (value) => `${value} GiB`)}
                  </Cell>
                  <Cell className="hidden lg:table-cell" muted>
                    {member.signedInAt ? formatTime(member.signedInAt) : "—"}
                  </Cell>
                  <Cell>
                    <Button variant="ghost" size="small" onClick={() => setEditing(member)} leadingIcon={<Icon name="sliders" size={12} />}>
                      Quota
                    </Button>
                  </Cell>
                </Row>
              ))}
            </DataTable>
          </div>
        )}
      </div>
      {editing ? <QuotaDialog member={editing} onClose={() => setEditing(null)} onSaved={members.reload} /> : null}
    </>
  );
};
