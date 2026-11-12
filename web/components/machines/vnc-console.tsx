"use client";

import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { socketUrl } from "@/lib/api/http";

import { ConsoleFrame, type ConsoleStatus } from "./console-frame";

type Rfb = import("@novnc/novnc").default;

/**
 * The VNC console: KubeVirt's VMI `vnc` subresource, passed through by vmservice, drawn by noVNC.
 * noVNC is loaded in the browser only.
 */
export const VncConsole = ({ machineId }: { readonly machineId: string }) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const rfbRef = useRef<Rfb | null>(null);
  const [status, setStatus] = useState<ConsoleStatus>("connecting");
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) {
      return;
    }
    let disposed = false;
    void import("@novnc/novnc").then(({ default: RFB }) => {
      if (disposed) {
        return;
      }
      const rfb = new RFB(container, socketUrl(`/api/vms/${encodeURIComponent(machineId)}/vnc`), {
        wsProtocols: ["plain.kubevirt.io"],
      });
      rfb.scaleViewport = true;
      rfb.background = "transparent";
      rfb.addEventListener("connect", () => setStatus("connected"));
      rfb.addEventListener("disconnect", () => {
        if (!disposed) {
          setStatus("closed");
        }
      });
      rfbRef.current = rfb;
    });
    return () => {
      disposed = true;
      rfbRef.current?.disconnect();
      rfbRef.current = null;
    };
  }, [machineId, attempt]);

  return (
    <ConsoleFrame
      status={status}
      onReconnect={() => {
        setStatus("connecting");
        setAttempt((value) => value + 1);
      }}
      actions={
        <Button size="small" disabled={status !== "connected"} onClick={() => rfbRef.current?.sendCtrlAltDel()}>
          Send Ctrl+Alt+Del
        </Button>
      }
    >
      <div key={attempt} ref={containerRef} className="h-full w-full" />
    </ConsoleFrame>
  );
};
