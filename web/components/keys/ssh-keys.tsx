"use client";

import { loadKeys } from "@/lib/api/keys";
import { sshCommand } from "@/lib/paths";
import { useState } from "react";

import { FormField } from "@/components/form/form-field";
import { PageHeader } from "@/components/layout/page-header";
import { useSession } from "@/components/session/session-context";
import { Button } from "@/components/ui/button";
import { CopyLine } from "@/components/ui/copy-line";
import { DialogFrame } from "@/components/ui/dialog-frame";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { TextInput } from "@/components/ui/text-input";
import { post, remove } from "@/lib/api/http";
import type { NewKeyRequest, SshKey } from "@/lib/api/types";
import { formatTime } from "@/lib/formatting/format";
import { useAction } from "@/lib/hooks/use-action";
import { useResource } from "@/lib/hooks/use-resource";


/** The most keys a member may register (docs/api.md). */
const keyMaximum = 20;

const AddKey = ({ count, onAdded }: { readonly count: number; readonly onAdded: () => Promise<void> }) => {
  const action = useAction();
  const [publicKey, setPublicKey] = useState("");
  const [label, setLabel] = useState("");
  const full = count >= keyMaximum;
  const ready = publicKey.trim() !== "" && [...label.trim()].length <= 63 && !full;

  const submit = () => {
    if (!ready) {
      return;
    }
    const body: NewKeyRequest = { publicKey: publicKey.trim(), label: label.trim() };
    void action.run(() => post<SshKey>("/api/keys", body)).then(async (done) => {
      if (done) {
        setPublicKey("");
        setLabel("");
        await onAdded();
      }
    });
  };

  return (
    <form
      className="flex flex-col gap-4 rounded-lg border border-border bg-surface p-4"
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      <FormField
        label="Public key"
        htmlFor="key-public"
        hint="One OpenSSH public key line, e.g. the contents of ~/.ssh/id_ed25519.pub (ed25519, ECDSA, or RSA of 2048 bits or more)."
      >
        <textarea
          id="key-public"
          value={publicKey}
          onChange={(event) => setPublicKey(event.target.value)}
          rows={3}
          spellCheck={false}
          autoComplete="off"
          placeholder="ssh-ed25519 AAAA… you@laptop"
          className="w-full resize-y rounded-md border border-border bg-background px-2.5 py-2 font-mono text-xs text-foreground outline-none placeholder:text-foreground-quaternary focus:border-accent"
        />
      </FormField>
      <FormField label="Label" htmlFor="key-label" hint="Only shown to you; empty uses the key's comment.">
        <TextInput
          id="key-label"
          value={label}
          maxLength={126}
          onChange={(event) => setLabel(event.target.value)}
          placeholder="laptop"
          autoComplete="off"
        />
      </FormField>
      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" variant="primary" disabled={!ready} pending={action.pending} leadingIcon={<Icon name="plus" size={12} />}>
          Add key
        </Button>
        {full ? <span className="text-xs text-foreground-tertiary">You have {keyMaximum} keys, the most allowed.</span> : null}
        <ErrorText message={action.error} />
      </div>
    </form>
  );
};

const DeleteKey = ({
  sshKey,
  onClose,
  onDeleted,
}: {
  readonly sshKey: SshKey;
  readonly onClose: () => void;
  readonly onDeleted: () => Promise<void>;
}) => {
  const action = useAction();
  return (
    <DialogFrame
      title={`Remove ${sshKey.label || "this key"}?`}
      onClose={onClose}
      submitLabel="Remove key"
      danger
      pending={action.pending}
      error={action.error}
      onSubmit={() =>
        void action.run(() => remove(`/api/keys/${encodeURIComponent(sshKey.id)}`)).then(async (done) => {
          if (done) {
            onClose();
            await onDeleted();
          }
        })
      }
    >
      <p className="text-[13px] text-foreground-secondary">
        The jump host stops accepting it for new connections right away; connections already open
        stay until they close. Machines you already made keep it in their authorized keys; remove it
        there yourself if you need to.
      </p>
    </DialogFrame>
  );
};

/** The member's SSH keys: added to machines made from now on, and accepted by the jump host. */
export const SshKeys = () => {
  const { configuration } = useSession();
  const keys = useResource(loadKeys);
  const [deleting, setDeleting] = useState<SshKey | null>(null);

  return (
    <>
      <PageHeader title="SSH keys" icon={<Icon name="key" size={15} />} />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-[880px] flex-col gap-6 px-4 py-5 md:px-8 md:py-8">
          <div className="flex flex-col gap-2 text-xs text-foreground-tertiary">
            <p>
              Keys you add here go into every machine you create afterwards; machines you already made keep the keys they
              had. They also let you through the jump host, which only forwards to your own machines.
            </p>
            {configuration.ssh ? <CopyLine text={sshCommand(configuration.ssh, "<user>", "<machine id>")} /> : null}
          </div>

          <section className="flex flex-col gap-3">
            <h2 className="text-[13px] font-medium text-foreground">Your keys</h2>
            {keys.error ? <ErrorText message={keys.error} /> : null}
            {keys.data === null ? (
              keys.error ? null : <div className="py-6 text-center text-xs text-foreground-tertiary">Loading…</div>
            ) : keys.data.length === 0 ? (
              <p className="rounded-lg border border-border bg-background-secondary px-4 py-6 text-center text-xs text-foreground-tertiary">
                No keys yet.
              </p>
            ) : (
              <ul className="flex flex-col divide-y divide-border rounded-lg border border-border">
                {keys.data.map((key) => (
                  <li key={key.id} className="flex items-center gap-3 px-4 py-3">
                    <Icon name="key" size={14} className="shrink-0 text-foreground-tertiary" />
                    <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                      <span className="truncate text-[13px] text-foreground">{key.label || "Unlabeled key"}</span>
                      <span className="truncate font-mono text-2xs text-foreground-tertiary">
                        {key.type} · {key.fingerprint}
                      </span>
                    </div>
                    <span className="hidden shrink-0 text-xs text-foreground-tertiary sm:inline">{formatTime(key.createdAt)}</span>
                    <Button
                      variant="ghost"
                      size="small"
                      onClick={() => setDeleting(key)}
                      leadingIcon={<Icon name="trash" size={12} />}
                      className="hover:text-danger"
                    >
                      Remove
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="flex flex-col gap-3">
            <h2 className="text-[13px] font-medium text-foreground">Add a key</h2>
            <AddKey count={keys.data?.length ?? 0} onAdded={keys.reload} />
          </section>
        </div>
      </div>
      {deleting ? <DeleteKey sshKey={deleting} onClose={() => setDeleting(null)} onDeleted={keys.reload} /> : null}
    </>
  );
};
