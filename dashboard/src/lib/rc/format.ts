const BYTE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"] as const;

export function formatBytes(value?: number | null): string {
  if (value == null || Number.isNaN(value)) {
    return "—";
  }
  const abs = Math.abs(value);
  if (abs < 1024) {
    return `${Math.round(value)} B`;
  }
  let n = abs;
  let unit = 0;
  while (n >= 1024 && unit < BYTE_UNITS.length - 1) {
    n /= 1024;
    unit += 1;
  }
  const signed = value < 0 ? -n : n;
  return `${signed.toFixed(signed >= 100 || unit === 0 ? 0 : 1)} ${BYTE_UNITS[unit]}`;
}

export function formatSpeed(bytesPerSecond?: number | null): string {
  if (bytesPerSecond == null || Number.isNaN(bytesPerSecond)) {
    return "—";
  }
  return `${formatBytes(bytesPerSecond)}/s`;
}

export function formatDuration(seconds?: number | null): string {
  if (seconds == null || Number.isNaN(seconds) || seconds < 0) {
    return "—";
  }
  const total = Math.round(seconds);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) {
    return `${h}h ${m}m`;
  }
  if (m > 0) {
    return `${m}m ${s}s`;
  }
  return `${s}s`;
}

export function formatEta(seconds?: number | null): string {
  if (seconds == null) {
    return "—";
  }
  return formatDuration(seconds);
}

export function formatPercent(value?: number | null): number {
  if (value == null || Number.isNaN(value)) {
    return 0;
  }
  return Math.max(0, Math.min(100, Math.round(value)));
}

export function formatTime(timestampMs?: number | null): string {
  if (!timestampMs) {
    return "—";
  }
  return new Date(timestampMs).toLocaleString();
}

const SENSITIVE_KEY =
  /(token|secret|password|passwd|pass|key|credential|auth|client_secret|service_account)/i;

export function isSensitiveKey(key: string): boolean {
  return SENSITIVE_KEY.test(key);
}

export function redactRecord(
  input: Record<string, unknown> | undefined,
): Record<string, string> {
  const out: Record<string, string> = {};
  if (!input) {
    return out;
  }
  for (const [key, value] of Object.entries(input)) {
    if (isSensitiveKey(key)) {
      out[key] = "••••";
      continue;
    }
    if (value == null) {
      out[key] = "";
      continue;
    }
    if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") {
      out[key] = String(value);
      continue;
    }
    out[key] = JSON.stringify(value);
  }
  return out;
}

export function parseHostUrl(raw: string): URL {
  const trimmed = raw.trim();
  if (!trimmed) {
    throw new Error("主机地址不能为空");
  }
  let url: URL;
  try {
    url = new URL(trimmed);
  } catch {
    throw new Error("主机地址不是合法 URL");
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error("只允许 http 或 https 的 rclone RC 地址");
  }
  if (url.username || url.password) {
    throw new Error("不要把账号密码写进 URL，请使用单独的用户名和密码字段");
  }
  if (!url.hostname) {
    throw new Error("主机地址缺少 hostname");
  }
  return url;
}

export function normalizeHostUrl(raw: string): string {
  const url = parseHostUrl(raw);
  url.hash = "";
  return url.toString().replace(/\/+$/, "");
}
