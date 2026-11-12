"use client";

import { useEffect, useRef, useState } from "react";
import { parse as parseYaml, stringify as stringifyYaml } from "yaml";

import { Badge, type BadgeTone } from "@/components/data/badge";
import { FormField } from "@/components/form/form-field";
import { PageHeader } from "@/components/layout/page-header";
import { LiveNotice } from "@/components/machines/live-notice";
import { useMachines } from "@/components/machines/machines-context";
import { useSession } from "@/components/session/session-context";
import { Button } from "@/components/ui/button";
import { DialogFrame } from "@/components/ui/dialog-frame";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { TextInput } from "@/components/ui/text-input";
import { patch, post, remove } from "@/lib/api/http";
import { formatBytes, kindLabel, profileLabel, readHeader, uploadFile, type Upload } from "@/lib/api/images";
import type { ImageChange, NewImageRequest, VmImage } from "@/lib/api/types";
import { messageOf } from "@/lib/errors";
import { formatTime } from "@/lib/formatting/format";
import { useAction } from "@/lib/hooks/use-action";
import { classNames } from "@/lib/utilities/class-names";

const nameMaximum = 63;

const stateTones: Readonly<Record<VmImage["state"], BadgeTone>> = {
  Preparing: "info",
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

/** How long the server may take to get an upload ready (CDI starts an upload server). */
const preparingTimeout = 3 * 60 * 1000;

/** An upload in flight: registered with the server, then (once it's ready) sent. */
type Pending = { readonly id: string; readonly name: string; readonly file: File };

/**
 * Upload from this computer. The header goes up first (it says what the file is), which registers
 * the image; the server then prepares (state "Preparing") and only once it's ready ("Uploading")
 * is the file sent, so the browser never streams into a server that isn't listening yet. That
 * moment comes over the event stream: nothing polls, and a timeout gives up after a few minutes.
 * Cancel stops the transfer and deletes the half-made image.
 */
const UploadForm = () => {
  const { putImage, images } = useMachines();
  const [file, setFile] = useState<File | null>(null);
  const [name, setName] = useState("");
  const [registering, setRegistering] = useState(false);
  const [pending, setPending] = useState<Pending | null>(null);
  const [progress, setProgress] = useState<Progress | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const upload = useRef<Upload | null>(null);
  const startedFor = useRef<string | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const busy = registering || pending !== null;
  const ready = file !== null && nameValid(name) && !busy;
  const serverState = pending ? images?.find((image) => image.id === pending.id)?.state : undefined;
  const serverReady = serverState === "Uploading";

  const resetForm = () => {
    setFile(null);
    setName("");
    if (input.current) {
      input.current.value = "";
    }
  };

  const finish = (outcome: { readonly error?: string; readonly notice?: string }) => {
    upload.current = null;
    startedFor.current = null;
    setPending(null);
    setProgress(null);
    setError(outcome.error ?? null);
    setNotice(outcome.notice ?? null);
  };

  /** Stops the transfer (if any) and deletes the half-made image. */
  const abandon = async (current: Pending, outcome: { readonly error?: string; readonly notice?: string }) => {
    startedFor.current = current.id; // nothing may start it now
    upload.current?.abort();
    await remove(`/api/images/${encodeURIComponent(current.id)}`).catch(() => undefined);
    finish(outcome);
    resetForm();
  };

  // Once the server is ready, send the file: started once per image, not stopped by later state changes.
  useEffect(() => {
    if (!pending || !serverReady || startedFor.current === pending.id) {
      return;
    }
    startedFor.current = pending.id;
    const current = pending;
    const total = current.file.size;
    upload.current = uploadFile(current.id, current.file, (sent) => setProgress({ sent, total }));
    void Promise.resolve().then(() => setProgress({ sent: 0, total }));
    upload.current.done.then(
      () => {
        finish({ notice: `${current.name} is uploaded; it's being checked now.` });
        resetForm();
      },
      (failure: unknown) => {
        if (messageOf(failure) !== "aborted") {
          void abandon(current, { error: `The upload failed: ${messageOf(failure)}` });
        }
      },
    );
  }, [pending, serverReady]); // eslint-disable-line react-hooks/exhaustive-deps

  // Waiting for the server: give up after a while, or if the image failed.
  useEffect(() => {
    if (!pending || startedFor.current === pending.id) {
      return;
    }
    if (serverState === "Failed") {
      const failed = pending;
      void Promise.resolve().then(() => abandon(failed, { error: "The server couldn't prepare the upload; try again." }));
      return;
    }
    const current = pending;
    const timer = window.setTimeout(() => {
      if (startedFor.current !== current.id) {
        void abandon(current, { error: "The server didn't get ready for the upload in time; try again." });
      }
    }, preparingTimeout);
    return () => window.clearTimeout(timer);
  }, [pending, serverState]); // eslint-disable-line react-hooks/exhaustive-deps

  const start = async () => {
    if (!file || !ready) {
      return;
    }
    setError(null);
    setNotice(null);
    setRegistering(true);
    try {
      const body: NewImageRequest = { name: name.trim(), source: "upload", sizeBytes: file.size, header: await readHeader(file) };
      const image = await post<VmImage>("/api/images", body);
      putImage(image);
      setPending({ id: image.id, name: image.name, file });
    } catch (failure) {
      setError(messageOf(failure));
    } finally {
      setRegistering(false);
    }
  };

  const cancel = () => {
    if (pending) {
      void abandon(pending, { notice: "Upload cancelled." });
    }
  };

  // Progress is only set once the file is being sent.
  const sending = pending !== null && progress !== null;
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
        htmlFor="image-file"
        hint="An ISO installer (e.g. Windows, a Linux installer) or a disk image (qcow2, raw, vmdk). It's checked after the upload."
      >
        <input
          id="image-file"
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
      <FormField label="Name" htmlFor="image-name" hint="Up to 63 characters; others see it if you make the image public.">
        <TextInput
          id="image-name"
          value={name}
          disabled={busy}
          maxLength={nameMaximum * 2}
          onChange={(event) => setName(event.target.value)}
          placeholder="Windows 11"
          autoComplete="off"
        />
      </FormField>
      {pending && !sending ? (
        <div className="flex items-center gap-2 text-xs text-foreground-tertiary">
          <Icon name="spinner" size={12} className="animate-spin" />
          Preparing the upload… Keep this page open.
        </div>
      ) : null}
      {sending && progress ? (
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
        {pending ? (
          <Button variant="ghost" onClick={cancel} leadingIcon={<Icon name="close" size={12} />}>
            Cancel upload
          </Button>
        ) : (
          <Button type="submit" variant="primary" disabled={!ready} pending={registering} leadingIcon={<Icon name="upload" size={12} />}>
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
  const { putImage } = useMachines();
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
        const body: NewImageRequest = { name: name.trim(), source: "url", url: url.trim() };
        void action
          .run(async () => {
            putImage(await post<VmImage>("/api/images", body));
          })
          .then((done) => {
            if (done) {
              setUrl("");
              setName("");
            }
          });
      }}
    >
      <FormField label="URL" htmlFor="image-url" hint="http or https. The cluster downloads it; you can close this page.">
        <TextInput
          id="image-url"
          value={url}
          onChange={(event) => setUrl(event.target.value)}
          placeholder="https://example.org/installer.iso"
          autoComplete="off"
          spellCheck={false}
          aria-invalid={(url !== "" && !urlValid) || undefined}
        />
      </FormField>
      <FormField label="Name" htmlFor="image-url-name" hint="Up to 63 characters; others see it if you make the image public.">
        <TextInput
          id="image-url-name"
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

const DeleteImage = ({ image, onClose }: { readonly image: VmImage; readonly onClose: () => void }) => {
  const action = useAction();
  return (
    <DialogFrame
      title={`Delete ${image.name}?`}
      onClose={onClose}
      submitLabel="Delete"
      danger
      pending={action.pending}
      error={action.error}
      onSubmit={() =>
        void action.run(() => remove(`/api/images/${encodeURIComponent(image.id)}`)).then((done) => {
          if (done) {
            onClose();
          }
        })
      }
    >
      <p className="text-[13px] text-foreground-secondary">
        The file is deleted{image.public ? ", and nobody can make machines from it any more" : ""}. Machines already made
        from it keep their own copy.
      </p>
    </DialogFrame>
  );
};

/** cloud-config extras as YAML for editing: "" for none. */
const toYaml = (value: Readonly<Record<string, unknown>>): string =>
  Object.keys(value).length === 0 ? "" : stringifyYaml(value);

/** The edited YAML as an object, or why it isn't one. */
const fromYaml = (text: string): { readonly value: Record<string, unknown> } | { readonly error: string } => {
  if (text.trim() === "") {
    return { value: {} };
  }
  try {
    const value: unknown = parseYaml(text);
    if (value === null || value === undefined) {
      return { value: {} };
    }
    if (typeof value !== "object" || Array.isArray(value)) {
      return { error: "The cloud-config extras must be a mapping (key: value lines), not a list or a single value." };
    }
    return { value: value as Record<string, unknown> };
  } catch (failure) {
    return { error: `Not valid YAML: ${messageOf(failure)}` };
  }
};

/** Name, public, and the preset's cloud-config extras: the owner's (or an admin's) to change. */
const EditImage = ({ image, onClose }: { readonly image: VmImage; readonly onClose: () => void }) => {
  const { putImage } = useMachines();
  const action = useAction();
  const [name, setName] = useState(image.name);
  const [shared, setShared] = useState(image.public);
  const [extras, setExtras] = useState(() => toYaml(image.cloudConfig));
  const parsed = fromYaml(extras);
  const ready = nameValid(name) && "value" in parsed;
  return (
    <DialogFrame
      title={`Edit ${image.name}`}
      onClose={onClose}
      submitLabel="Save"
      submitDisabled={!ready}
      pending={action.pending}
      error={action.error}
      onSubmit={() => {
        if (!ready || !("value" in parsed)) {
          return;
        }
        const body: ImageChange = { name: name.trim(), public: shared, cloudConfig: parsed.value };
        void action
          .run(async () => {
            putImage(await patch<VmImage>(`/api/images/${encodeURIComponent(image.id)}`, body));
          })
          .then((done) => {
            if (done) {
              onClose();
            }
          });
      }}
    >
      <div className="flex flex-col gap-4">
        <FormField label="Name" htmlFor="edit-image-name">
          <TextInput
            id="edit-image-name"
            data-autofocus=""
            value={name}
            maxLength={nameMaximum * 2}
            onChange={(event) => setName(event.target.value)}
            autoComplete="off"
          />
        </FormField>
        <label className="flex items-start gap-2 text-[13px] text-foreground" htmlFor="edit-image-public">
          <input
            id="edit-image-public"
            type="checkbox"
            checked={shared}
            onChange={(event) => setShared(event.target.checked)}
            className="mt-0.5 h-3.5 w-3.5 accent-[var(--accent)]"
          />
          <span className="flex flex-col gap-0.5">
            <span>Public</span>
            <span className="text-xs text-foreground-tertiary">
              Every member sees it, with your name, and can make machines from it. Only you (and admins) can change or
              delete it.
            </span>
          </span>
        </label>
        {image.kind !== "iso" || image.profile !== "windows" ? (
          <FormField
            label="cloud-config extras"
            htmlFor="edit-image-extras"
            hint="YAML merged into the #cloud-config of machines made from this image (a preset), e.g. package mirrors. The account, password, keys and host name are VMService's and can't be set here."
          >
            <textarea
              id="edit-image-extras"
              value={extras}
              onChange={(event) => setExtras(event.target.value)}
              rows={8}
              spellCheck={false}
              placeholder={"apt:\n  primary:\n    - arches: [default]\n      uri: http://ftp.kaist.ac.kr/ubuntu/"}
              aria-invalid={"error" in parsed || undefined}
              className="w-full resize-y rounded-md border border-border bg-background px-3 py-2 font-mono text-xs text-foreground outline-none placeholder:text-foreground-quaternary focus:border-border-strong aria-[invalid=true]:border-danger"
            />
          </FormField>
        ) : null}
        {"error" in parsed ? <ErrorText message={parsed.error} /> : null}
      </div>
    </DialogFrame>
  );
};

const ImageRow = ({
  image,
  canChange,
  onEdit,
  onDelete,
}: {
  readonly image: VmImage;
  readonly canChange: boolean;
  readonly onEdit: () => void;
  readonly onDelete: () => void;
}) => {
  const size = image.kind === "disk" && image.virtualBytes > 0 ? `${formatBytes(image.virtualBytes)} disk` : formatBytes(image.sizeBytes);
  const preset = Object.keys(image.cloudConfig ?? {}).length > 0;
  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <Icon name={image.kind === "disk" ? "hard-drive" : "database"} size={14} className="shrink-0 text-foreground-tertiary" />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="truncate text-[13px] text-foreground">{image.name}</span>
          {image.public ? <Badge tone="accent">Public</Badge> : null}
          {preset ? <Badge tone="neutral">Preset</Badge> : null}
          <Badge tone={stateTones[image.state]}>
            {image.state}
            {image.progress ? ` ${image.progress}` : ""}
          </Badge>
        </div>
        <span className="truncate text-2xs text-foreground-tertiary">
          {image.mine ? "" : `by ${image.owner} · `}
          {image.kind ? kindLabel(image.kind) : "Not checked yet"}
          {image.profile ? ` · ${profileLabel(image.profile)}` : ""} · {size}
          {image.source === "url" ? " · imported from a URL" : ""}
        </span>
        {image.detail ? (
          <span className={classNames("text-2xs", image.state === "Failed" ? "text-danger" : "text-foreground-tertiary")}>
            {image.detail}
          </span>
        ) : null}
      </div>
      <span className="hidden shrink-0 text-xs text-foreground-tertiary sm:inline">{formatTime(image.createdAt)}</span>
      {canChange ? (
        <div className="flex shrink-0 items-center gap-1">
          <Button variant="ghost" size="small" onClick={onEdit} leadingIcon={<Icon name="edit" size={12} />}>
            Edit
          </Button>
          <Button variant="ghost" size="small" onClick={onDelete} leadingIcon={<Icon name="trash" size={12} />} className="hover:text-danger">
            Delete
          </Button>
        </div>
      ) : null}
    </li>
  );
};

const ImageList = ({
  images,
  empty,
  canChange,
  onEdit,
  onDelete,
}: {
  readonly images: readonly VmImage[];
  readonly empty: string;
  readonly canChange: (image: VmImage) => boolean;
  readonly onEdit: (image: VmImage) => void;
  readonly onDelete: (image: VmImage) => void;
}) =>
  images.length === 0 ? (
    <p className="rounded-lg border border-border bg-background-secondary px-4 py-6 text-center text-xs text-foreground-tertiary">
      {empty}
    </p>
  ) : (
    <ul className="flex flex-col divide-y divide-border rounded-lg border border-border">
      {images.map((image) => (
        <ImageRow
          key={image.id}
          image={image}
          canChange={canChange(image)}
          onEdit={() => onEdit(image)}
          onDelete={() => onDelete(image)}
        />
      ))}
    </ul>
  );

/** ISOs and disk images: the member's own, then everyone's public ones (admins: every member's). */
export const ImagesPage = () => {
  const { user } = useSession();
  const { images, imagesError } = useMachines();
  const [deleting, setDeleting] = useState<VmImage | null>(null);
  const [editing, setEditing] = useState<VmImage | null>(null);
  const admin = user.admin === true;
  const mine = (images ?? []).filter((image) => image.mine);
  const others = (images ?? []).filter((image) => !image.mine);
  const canChange = (image: VmImage) => image.mine || admin;
  return (
    <>
      <PageHeader title="Images" icon={<Icon name="hard-drive" size={15} />} />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-[880px] flex-col gap-6 px-4 py-5 md:px-8 md:py-8">
          <LiveNotice />
          <p className="text-xs text-foreground-tertiary">
            Machines are made from images: an ISO to install an OS from (Windows, a Linux installer, …) or a ready disk
            image (e.g. a cloud image with cloud-init). Each one is checked to tell which it is and which OS it holds.
            Make one public to let every member use it; add cloud-config extras to make it a preset.
          </p>
          <ErrorText message={imagesError} />
          <section className="flex flex-col gap-3">
            <h2 className="text-[13px] font-medium text-foreground">Your images</h2>
            {images === null ? (
              imagesError ? null : <div className="py-6 text-center text-xs text-foreground-tertiary">Loading…</div>
            ) : (
              <ImageList images={mine} empty="No images yet." canChange={canChange} onEdit={setEditing} onDelete={setDeleting} />
            )}
          </section>
          {images !== null ? (
            <section className="flex flex-col gap-3">
              <h2 className="text-[13px] font-medium text-foreground">{admin ? "Other members' images" : "Public images"}</h2>
              <ImageList
                images={others}
                empty={admin ? "No other images." : "No public images yet."}
                canChange={canChange}
                onEdit={setEditing}
                onDelete={setDeleting}
              />
            </section>
          ) : null}
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
      {deleting ? <DeleteImage image={deleting} onClose={() => setDeleting(null)} /> : null}
      {editing ? <EditImage image={editing} onClose={() => setEditing(null)} /> : null}
    </>
  );
};
