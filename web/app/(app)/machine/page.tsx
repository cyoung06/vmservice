import { Suspense } from "react";

import { MachineDetail } from "@/components/machines/machine-detail";

export default function MachinePage() {
  return (
    <Suspense>
      <MachineDetail />
    </Suspense>
  );
}
