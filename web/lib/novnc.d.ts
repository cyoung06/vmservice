// The part of noVNC's RFB class (https://github.com/novnc/noVNC/blob/master/docs/API.md) the VNC console uses.
declare module "@novnc/novnc" {
  type RfbOptions = {
    readonly shared?: boolean;
    readonly credentials?: { readonly username?: string; readonly password?: string; readonly target?: string };
    readonly wsProtocols?: readonly string[];
  };

  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, urlOrChannel: string | WebSocket, options?: RfbOptions);
    scaleViewport: boolean;
    resizeSession: boolean;
    focusOnClick: boolean;
    background: string;
    disconnect(): void;
    sendCtrlAltDel(): void;
    focus(): void;
  }
}
