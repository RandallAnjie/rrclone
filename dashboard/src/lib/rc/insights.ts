import type {
  ConfigDump,
  CoreMemStats,
  CoreStats,
  JobStatus,
  TransferringFile,
  TransferredFile,
} from "./types";

export type ChartSlice = {
  name: string;
  value: number;
};

export type TrendPoint = {
  t: number;
  label: string;
  speed: number;
  bytes: number;
  transferring: number;
  errors: number;
  heap: number;
};

export const TREND_MAX = 90;

function clockLabel(ts: number): string {
  return new Date(ts).toLocaleTimeString("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

export function appendTrend(
  history: TrendPoint[],
  stats: CoreStats | null | undefined,
  mem?: CoreMemStats | null,
): TrendPoint[] {
  if (!stats) {
    return history;
  }
  const now = Date.now();
  const next: TrendPoint = {
    t: now,
    label: clockLabel(now),
    speed: Number(stats.speed ?? 0),
    bytes: Number(stats.bytes ?? 0),
    transferring: stats.transferring?.length ?? 0,
    errors: Number(stats.errors ?? 0),
    heap: Number(mem?.HeapAlloc ?? 0),
  };
  return [...history, next].slice(-TREND_MAX);
}

export function sparkFromTrend(points: TrendPoint[], key: keyof TrendPoint): Array<{ value: number }> {
  return points
    .map((point) => point[key])
    .filter((value): value is number => typeof value === "number")
    .map((value) => ({ value }));
}

function slices(entries: Array<[string, number]>): ChartSlice[] {
  return entries
    .map(([name, value]) => ({ name, value: Math.max(0, value) }))
    .filter((item) => item.value > 0);
}

export function transferStatusSlices(stats: CoreStats | null | undefined): ChartSlice[] {
  if (!stats) {
    return [];
  }
  return slices([
    ["进行中", stats.transferring?.length ?? 0],
    ["校验中", stats.checking?.length ?? 0],
    ["已完成", stats.transfers ?? 0],
    ["错误", stats.errors ?? 0],
  ]);
}

export function bytesSlices(stats: CoreStats | null | undefined): ChartSlice[] {
  if (!stats) {
    return [];
  }
  const done = stats.bytes ?? 0;
  const total = stats.totalBytes ?? 0;
  const remaining = Math.max(0, total - done);
  if (total > 0) {
    return slices([
      ["已传输", done],
      ["剩余", remaining],
    ]);
  }
  return done > 0 ? [{ name: "已传输", value: done }] : [];
}

export function operationSlices(stats: CoreStats | null | undefined): ChartSlice[] {
  if (!stats) {
    return [];
  }
  return slices([
    ["传输", stats.transfers ?? 0],
    ["校验", stats.checks ?? 0],
    ["删除", stats.deletes ?? 0],
    ["服务端复制", stats.serverSideCopies ?? 0],
    ["服务端移动", stats.serverSideMoves ?? 0],
  ]);
}

export function remoteTypeSlices(dump: ConfigDump | null | undefined): ChartSlice[] {
  if (!dump) {
    return [];
  }
  const counts = new Map<string, number>();
  for (const config of Object.values(dump)) {
    const type = typeof config.type === "string" && config.type ? config.type : "unknown";
    counts.set(type, (counts.get(type) ?? 0) + 1);
  }
  return [...counts.entries()]
    .map(([name, value]) => ({ name, value }))
    .sort((a, b) => b.value - a.value);
}

export function jobStatusSlices(jobs: JobStatus[]): ChartSlice[] {
  let running = 0;
  let success = 0;
  let failed = 0;
  for (const job of jobs) {
    if (!job.finished) {
      running += 1;
    } else if (job.success) {
      success += 1;
    } else {
      failed += 1;
    }
  }
  return slices([
    ["运行中", running],
    ["成功", success],
    ["失败", failed],
  ]);
}

export function transferSizeBars(files: TransferringFile[] | undefined, limit = 8): ChartSlice[] {
  return [...(files ?? [])]
    .map((file) => ({
      name: file.name.split("/").pop() || file.name,
      value: Number(file.size ?? file.bytes ?? 0),
    }))
    .filter((item) => item.value > 0)
    .sort((a, b) => b.value - a.value)
    .slice(0, limit);
}

export function transferredOutcomeSlices(files: TransferredFile[] | undefined): ChartSlice[] {
  const list = files ?? [];
  let ok = 0;
  let failed = 0;
  let checked = 0;
  for (const file of list) {
    if (file.error) {
      failed += 1;
    } else if (file.checked) {
      checked += 1;
    } else {
      ok += 1;
    }
  }
  return slices([
    ["成功", ok],
    ["仅校验", checked],
    ["失败", failed],
  ]);
}

export function overallPercent(stats: CoreStats | null | undefined): number {
  if (!stats?.totalBytes) {
    return 0;
  }
  return Math.max(0, Math.min(100, Math.round(((stats.bytes ?? 0) / stats.totalBytes) * 100)));
}
