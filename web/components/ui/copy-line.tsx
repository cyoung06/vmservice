"use client";

import { useState } from "react";

import { Icon } from "./icon";

/** A command in a monospace box with a button that copies it. */
export const CopyLine = ({ text }: { readonly text: string }) => {
  const [copied, setCopied] = useState(false);
  const copy = () => {
    void navigator.clipboard?.writeText(text).then(
      () => {
        setCopied(true);
      },
      () => {
        // No clipboard access (insecure origin or denied): the text is still there to select.
      },
    );
  };
  return (
    <div className="flex min-w-0 items-center gap-2 rounded-md border border-border bg-background-secondary py-1 pr-1 pl-3">
      <code className="min-w-0 flex-1 truncate font-mono text-xs text-foreground select-all">{text}</code>
      <button
        type="button"
        onClick={copy}
        onBlur={() => setCopied(false)}
        aria-label={copied ? "Copied" : "Copy"}
        title={copied ? "Copied" : "Copy"}
        className="flex h-6 w-6 shrink-0 items-center justify-center rounded text-foreground-tertiary hover:bg-background-tertiary hover:text-foreground"
      >
        <Icon name={copied ? "check" : "copy"} size={13} />
      </button>
    </div>
  );
};
