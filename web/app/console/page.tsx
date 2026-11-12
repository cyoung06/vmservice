import { Suspense } from "react";

import { ConsoleWindow } from "@/components/machines/console-window";
import { SessionGate } from "@/components/session/session-gate";

/** A machine's console alone, full window: where "Pop out" opens it. No sidebar, no app chrome. */
export default function ConsolePage() {
  return (
    <SessionGate>
      <Suspense>
        <ConsoleWindow />
      </Suspense>
    </SessionGate>
  );
}
