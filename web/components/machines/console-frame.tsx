import type { ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";
import { classNames } from "@/lib/utilities/class-names";

export type ConsoleStatus = "connecting" | "connected" | "closed";

const labels: Readonly<Record<ConsoleStatus, string>> = {
  connecting: "Connecting…",
  connected: "Connected",
  closed: "Closed",
};

/** A console's screen with its status line and, once it has closed, a button to connect again. */
export const ConsoleFrame = ({
  status,
  onReconnect,
  actions,
  fill = false,
  children,
}: {
  readonly status: ConsoleStatus;
  readonly onReconnect: () => void;
  readonly actions?: ReactNode;
  /** Take all the height the parent gives (a flex column) instead of a fixed height. */
  readonly fill?: boolean;
  readonly children: ReactNode;
}) => (
  <div
    className={classNames(
      "flex flex-col overflow-hidden border-border",
      fill ? "min-h-0 flex-1" : "rounded-lg border",
    )}
  >
    <div className="flex h-9 items-center gap-2 border-b border-border bg-background-secondary px-3 text-xs">
      <span
        className={classNames(
          "inline-block h-2 w-2 shrink-0 rounded-full",
          status === "connected" ? "bg-success" : status === "connecting" ? "bg-warning" : "bg-foreground-quaternary",
        )}
      />
      <span className="text-foreground-secondary">{labels[status]}</span>
      <span className="flex-1" />
      {actions}
      {status === "closed" ? (
        <Button size="small" onClick={onReconnect} leadingIcon={<Icon name="refresh" size={12} />}>
          Reconnect
        </Button>
      ) : null}
    </div>
    <div className={classNames("bg-background-secondary", fill ? "min-h-0 flex-1" : "h-[min(70vh,560px)]")}>{children}</div>
  </div>
);
