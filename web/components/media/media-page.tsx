"use client";

import { useRef, useState } from "react";

import { Badge, type BadgeTone } from "@/components/data/badge";
import { FormField } from "@/components/form/form-field";
import { PageHeader } from "@/components/layout/page-header";
import { LiveNotice } from "@/components/machines/live-notice";
import { useMachines } from "@/components/machines/machines-context";
import { Button } from "@/components/ui/button";
import { DialogFrame } from "@/components/ui/dialog-frame";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { TextInput } from "@/components/ui/text-input";
import { post, remove } from "@/lib/api/http";
import { formatBytes, kindLabel, profileLabel, readHeader, uploadFile, type Upload } from "@/lib/api/media";
import type { Medium, NewMediumRequest } from "@/lib/api/types";
import { messageOf } from "@/lib/errors";
import { formatTime } from "@/lib/formatting/format";
import { useAction } from "@/lib/hooks/use-action";
import { classNames } from "@/lib/utilities/class-names";

const nameMaximum = 63;

const stateTones: Readonly<Record<Medium["state"], BadgeTone>> = {
  Uploading: "info",
  Importing: "info",
  Inspecting: "info",
  Ready: "success",
  Failed: "danger",
};

/** The name a file suggests: its name without the extension, cut to what a name may be. */
const nameFromFile = (file: File): string => [...file.name.replace(/\.[^.]+$/, "")].slice(0, nameMaximum).join("");

const nameValid = (name: string): boolean => {
  const length = [...name.trim()].length;
  return length > 0 && length <= nameMaximum;
};

type Progress = { readonly sent: number; readonly total: number };

/** Upload from this computer: the header goes up first (it says what the file is), then the file itself. */
const UploadForm = () => {
  const { putMedium } = useMachines();
  const [file, setFile] = useState<File | null>(null);
  const [name, setName] = useState("");
  const [progress, setProgress] = useState<Progress | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const upload = useRef<Upload | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const busy = progress !== null;
  const ready = file !== null && nameValid(name) && !busy;

  const reset = () => {
    setFile(null);
    setName("");
    if (input.current) {
      input.current.value = "";
    }
  };

  const start = async () => {
    if (!file || !ready) {
      return;
    }
    setError(null);
    setNotice(null);
    setProgress({ sent: 0, total: file.size });
    try {
      const body: NewMediumRequest = { name: name.trim(), source: "upload", sizeBytes: file.size, header: await readHeader(file) };
      const medium = await post<Medium>("/api/media", body);
      putMedium(medium);
      upload.current = uploadFile(medium.id, file, (sent) => setProgress({ sent, total: file.size }));
      await upload.current.done;
      setNotice(`${medium.name} is uploaded; it's being checked now.`);
      reset();
    } catch (failure) {
      const message = messageOf(failure);
      setError(message === "aborted" ? "Upload cancelled. The unfinished medium can be deleted from the list." : message);
    } finally {
      upload.current = null;
      setProgress(null);
    }
  };

  const percent = progress && progress.total > 0 ? Math.min(100, (progress.sent / progress.total) * 100) : 0;
  return (
    <form
      className="flex flex-col gap-4 rounded-lg border border-border bg-surface p-4"
      onSubmit={(event) => {
        event.preventDefault();
        void start();
      }}
    >
      <FormField
        label="File"
        htmlFor="medium-file"
        hint="An ISO installer (e.g. Windows, a Linux installer) or a disk image (qcow2, raw, vmdk). It's checked after the upload."
      >
        <input
          id="medium-file"
          ref={input}
          type="file"
          disabled={busy}
          accept=".iso,.img,.qcow2,.raw,.vmdk,application/x-iso9660-image"
          onChange={(event) => {
            const chosen = event.target.files?.[0] ?? null;
            setFile(chosen);
            if (chosen && name.trim() === "") {
              setName(nameFromFile(chosen));
            }
          }}
          className="block w-full text-[13px] text-foreground-secondary file:mr-3 file:h-8 file:cursor-pointer file:rounded-md file:border file:border-border file:bg-background file:px-3 file:text-[13px] file:text-foreground hover:file:border-border-strong"
        />
      </FormField>
      <FormField label="Name" htmlFor="medium-name" hint="Only shown to you; up to 63 characters.">
        <TextInput
          id="medium-name"
          value={name}
          disabled={busy}
          maxLength={nameMaximum * 2}
          onChange={(event) => setName(event.target.value)}
          placeholder="Windows 11"
          autoComplete="off"
        />
      </FormField>
      {progress ? (
        <div className="flex flex-col gap-1.5">
          <div className="h-1.5 overflow-hidden rounded-full bg-background-tertiary">
            <div className="h-full rounded-full bg-accent transition-[width]" style={{ width: `${percent}%` }} />
          </div>
          <span className="text-xs tabular-nums text-foreground-tertiary">
            {formatBytes(progress.sent)} of {formatBytes(progress.total)} ({percent.toFixed(1)}%). Keep this page open
            until it&apos;s done.
          </span>
        </div>
      ) : null}
      <div className="flex flex-wrap items-center gap-3">
        {busy ? (
          <Button variant="ghost" onClick={() => upload.current?.abort()} leadingIcon={<Icon name="close" size={12} />}>
            Cancel upload
          </Button>
        ) : (
          <Button type="submit" variant="primary" disabled={!ready} leadingIcon={<Icon name="upload" size={12} />}>
            Upload
          </Button>
        )}
        {file && !busy ? <span className="text-xs text-foreground-tertiary">{formatBytes(file.size)}</span> : null}
        <ErrorText message={error} />
        {notice ? <span className="text-xs text-success">{notice}</span> : null}
      </div>
    </form>
  );
};

/** Import from a URL: the cluster downloads it, so nothing passes through this computer. */
const UrlForm = () => {
  const { putMedium } = useMachines();
  const action = useAction();
  const [url, setUrl] = useState("");
  const [name, setName] = useState("");
  const urlValid = /^https?:\/\/\S+$/i.test(url.trim());
  const ready = urlValid && nameValid(name);

  return (
    <form
      className="flex flex-col gap-4 rounded-lg border border-border bg-surface p-4"
      onSubmit={(event) => {
        event.preventDefault();
        if (!ready) {
          return;
        }
        const body: NewMediumRequest = { name: name.trim(), source: "url", url: url.trim() };
        void action
          .run(async () => {
            putMedium(await post<Medium>("/api/media", body));
          })
          .then((done) => {
            if (done) {
              setUrl("");
              setName("");
            }
          });
      }}
    >
      <FormField label="URL" htmlFor="medium-url" hint="http or https. The cluster downloads it; you can close this page.">
        <TextInput
          id="medium-url"
          value={url}
          onChange={(event) => setUrl(event.target.value)}
          placeholder="https://example.org/installer.iso"
          autoComplete="off"
          spellCheck={false}
          aria-invalid={(url !== "" && !urlValid) || undefined}
        />
      </FormField>
      <FormField label="Name" htmlFor="medium-url-name" hint="Only shown to you; up to 63 characters.">
        <TextInput
          id="medium-url-name"
          value={name}
          maxLength={nameMaximum * 2}
          onChange={(event) => setName(event.target.value)}
          placeholder="Debian installer"
          autoComplete="off"
        />
      </FormField>
      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" variant="primary" disabled={!ready} pending={action.pending} leadingIcon={<Icon name="download" size={12} />}>
          Import
        </Button>
        <ErrorText message={action.error} />
      </div>
    </form>
  );
};

const DeleteMedium = ({ medium, onClose }: { readonly medium: Medium; readonly onClose: () => void }) => {
  const action = useAction();
  return (
    <DialogFrame
      title={`Delete ${medium.name}?`}
      onClose={onClose}
      submitLabel="Delete"
      danger
      pending={action.pending}
      error={action.error}
      onSubmit={() =>
        void action.run(() => remove(`/api/media/${encodeURIComponent(medium.id)}`)).then((done) => {
          if (done) {
            onClose();
          }
        })
      }
    >
      <p className="text-[13px] text-foreground-secondary">
        The file is deleted. Machines you made from it keep their own copy.
      </p>
    </DialogFrame>
  );
};

const MediumRow = ({ medium, onDelete }: { readonly medium: Medium; readonly onDelete: () => void }) => {
  const size = medium.kind === "disk" && medium.virtualBytes > 0 ? `${formatBytes(medium.virtualBytes)} disk` : formatBytes(medium.sizeBytes);
  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <Icon name={medium.kind === "disk" ? "hard-drive" : "database"} size={14} className="shrink-0 text-foreground-tertiary" />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-[13px] text-foreground">{medium.name}</span>
          {medium.shared ? <Badge tone="accent">Shared</Badge> : null}
          <Badge tone={stateTones[medium.state]}>
            {medium.state}
            {medium.progress ? ` ${medium.progress}` : ""}
          </Badge>
        </div>
        <span className="truncate text-2xs text-foreground-tertiary">
          {medium.kind ? kindLabel(medium.kind) : "Not checked yet"}
          {medium.profile ? ` · ${profileLabel(medium.profile)}` : ""} · {size}
          {medium.source === "url" ? " · imported from a URL" : ""}
        </span>
        {medium.detail ? (
          <span className={classNames("text-2xs", medium.state === "Failed" ? "text-danger" : "text-foreground-tertiary")}>
            {medium.detail}
          </span>
        ) : null}
      </div>
      <span className="hidden shrink-0 text-xs text-foreground-tertiary sm:inline">{formatTime(medium.createdAt)}</span>
      {medium.shared ? null : (
        <Button variant="ghost" size="small" onClick={onDelete} leadingIcon={<Icon name="trash" size={12} />} className="hover:text-danger">
          Delete
        </Button>
      )}
    </li>
  );
};

/** ISOs and disk images: the member's own and the shared ones. */
export const MediaPage = () => {
  const { media, mediaError } = useMachines();
  const [deleting, setDeleting] = useState<Medium | null>(null);
  return (
    <>
      <PageHeader title="Media" icon={<Icon name="hard-drive" size={15} />} />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-[880px] flex-col gap-6 px-4 py-5 md:px-8 md:py-8">
          <LiveNotice />
          <p className="text-xs text-foreground-tertiary">
            Bring an ISO to install an OS from (Windows, a Linux installer, …) or a ready disk image (e.g. a cloud
            image with cloud-init). Each one is checked to tell which it is and which OS it holds; then you can make
            machines from it on the New machine page.
          </p>
          <section className="flex flex-col gap-3">
            <h2 className="text-[13px] font-medium text-foreground">Media</h2>
            <ErrorText message={mediaError} />
            {media === null ? (
              mediaError ? null : <div className="py-6 text-center text-xs text-foreground-tertiary">Loading…</div>
            ) : media.length === 0 ? (
              <p className="rounded-lg border border-border bg-background-secondary px-4 py-6 text-center text-xs text-foreground-tertiary">
                No media yet.
              </p>
            ) : (
              <ul className="flex flex-col divide-y divide-border rounded-lg border border-border">
                {media.map((medium) => (
                  <MediumRow key={medium.id} medium={medium} onDelete={() => setDeleting(medium)} />
                ))}
              </ul>
            )}
          </section>
          <section className="flex flex-col gap-3">
            <h2 className="text-[13px] font-medium text-foreground">Upload from this computer</h2>
            <UploadForm />
          </section>
          <section className="flex flex-col gap-3">
            <h2 className="text-[13px] font-medium text-foreground">Import from a URL</h2>
            <UrlForm />
          </section>
        </div>
      </div>
      {deleting ? <DeleteMedium medium={deleting} onClose={() => setDeleting(null)} /> : null}
    </>
  );
};
