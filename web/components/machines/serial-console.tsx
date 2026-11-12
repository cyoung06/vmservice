"use client";

import { useEffect, useRef, useState } from "react";

import "@xterm/xterm/css/xterm.css";

import { socketUrl } from "@/lib/api/http";

import { ConsoleFrame, type ConsoleStatus } from "./console-frame";

/**
 * The serial console (ttyS0): KubeVirt's VMI `console` subresource, passed through by vmservice.
 * Raw bytes in binary frames both ways; xterm.js draws them. xterm is loaded in the browser only.
 */
export const SerialConsole = ({ machineId }: { readonly machineId: string }) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<ConsoleStatus>("connecting");
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) {
      return;
    }
    let disposed = false;
    let cleanup = () => {};
    void Promise.all([import("@xterm/xterm"), import("@xterm/addon-fit")]).then(([{ Terminal }, { FitAddon }]) => {
      if (disposed) {
        return;
      }
      const styles = getComputedStyle(document.documentElement);
      const terminal = new Terminal({
        cursorBlink: true,
        fontFamily: '"Geist Mono Variable", ui-monospace, monospace',
        fontSize: 13,
        theme: {
          background: styles.getPropertyValue("--background-secondary").trim() || undefined,
          foreground: styles.getPropertyValue("--foreground").trim() || undefined,
        },
      });
      const fit = new FitAddon();
      terminal.loadAddon(fit);
      terminal.open(container);
      fit.fit();
      terminal.focus();

      const socket = new WebSocket(socketUrl(`/api/vms/${encodeURIComponent(machineId)}/serial`), ["plain.kubevirt.io"]);
      socket.binaryType = "arraybuffer";
      const encoder = new TextEncoder();
      socket.onopen = () => setStatus("connected");
      socket.onmessage = (event: MessageEvent<ArrayBuffer | string>) => {
        terminal.write(typeof event.data === "string" ? event.data : new Uint8Array(event.data));
      };
      socket.onclose = () => {
        if (!disposed) {
          setStatus("closed");
          terminal.write("\r\n\x1b[2m[console closed]\x1b[0m\r\n");
        }
      };
      const send = (data: string) => {
        if (socket.readyState === WebSocket.OPEN) {
          socket.send(encoder.encode(data));
        }
      };
      const typing = terminal.onData(send);
      const binary = terminal.onBinary((data) => {
        if (socket.readyState === WebSocket.OPEN) {
          socket.send(Uint8Array.from(data, (character) => character.charCodeAt(0) & 0xff));
        }
      });
      const resize = new ResizeObserver(() => fit.fit());
      resize.observe(container);

      cleanup = () => {
        resize.disconnect();
        typing.dispose();
        binary.dispose();
        socket.onclose = null;
        socket.close();
        terminal.dispose();
      };
    });
    return () => {
      disposed = true;
      cleanup();
    };
  }, [machineId, attempt]);

  return (
    <ConsoleFrame
      status={status}
      onReconnect={() => {
        setStatus("connecting");
        setAttempt((value) => value + 1);
      }}
    >
      <div key={attempt} ref={containerRef} className="h-full w-full p-2" />
    </ConsoleFrame>
  );
};
