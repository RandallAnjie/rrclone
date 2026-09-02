import type { Host } from "./types";

type RcResponse<T> = {
  data?: T;
  error?: string;
};

export class RcError extends Error {
  status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "RcError";
    this.status = status;
  }
}

export async function rcCall<T>(
  host: Host,
  path: string,
  params: Record<string, unknown> = {},
): Promise<T> {
  const res = await fetch("/api/rc", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      url: host.url,
      user: host.user,
      pass: host.pass,
      path,
      params,
    }),
  });
  const json = (await res.json()) as RcResponse<T>;
  if (!res.ok || json.error) {
    throw new RcError(json.error || res.statusText, res.status);
  }
  return (json.data ?? {}) as T;
}
