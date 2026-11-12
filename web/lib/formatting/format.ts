export const formatMemory = (mebibytes: number): string =>
  mebibytes % 1024 === 0 ? `${mebibytes / 1024} GiB` : `${mebibytes} MiB`;

export const formatCpu = (cpu: number): string => `${cpu} vCPU`;

export const formatTime = (iso: string): string => {
  const date = new Date(iso);
  return Number.isNaN(date.getTime())
    ? iso
    : date.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
};
