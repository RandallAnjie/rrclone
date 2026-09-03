import { normalizeHostUrl } from "./format";
import type { Host } from "./types";

export const HOSTS_STORAGE_KEY = "rrclone.dashboard.hosts";
export const SELECTED_HOST_STORAGE_KEY = "rrclone.dashboard.selectedHost";

export type HostDraft = {
  name: string;
  url: string;
  user?: string;
  pass?: string;
};

export const LOCAL_HOST: Host = {
  id: "local",
  name: "本机",
  url: "http://127.0.0.1:5572",
  locked: true,
};

export function applyHostDraft(host: Host, draft: HostDraft): Host {
  return {
    ...host,
    name: draft.name.trim() || host.name,
    url: normalizeHostUrl(draft.url),
    user: draft.user?.trim() || undefined,
    pass: draft.pass || undefined,
  };
}

export function defaultHosts(): Host[] {
  return [{ ...LOCAL_HOST }];
}

export function loadHosts(): Host[] {
  if (typeof window === "undefined") {
    return defaultHosts();
  }
  try {
    const raw = window.localStorage.getItem(HOSTS_STORAGE_KEY);
    if (!raw) {
      return defaultHosts();
    }
    const parsed = JSON.parse(raw) as Host[];
    if (!Array.isArray(parsed) || parsed.length === 0) {
      return defaultHosts();
    }
    const hosts: Host[] = parsed
      .filter((host) => host && typeof host.id === "string" && typeof host.url === "string")
      .map((host) => {
        const next: Host = {
          ...host,
          url: normalizeHostUrl(host.url),
        };
        if (host.id === LOCAL_HOST.id) {
          next.locked = true;
        }
        return next;
      });
    if (!hosts.some((host) => host.id === LOCAL_HOST.id)) {
      hosts.unshift({ ...LOCAL_HOST });
    }
    return hosts;
  } catch {
    return defaultHosts();
  }
}

export function saveHosts(hosts: Host[]): void {
  window.localStorage.setItem(HOSTS_STORAGE_KEY, JSON.stringify(hosts));
}

export function loadSelectedHostId(hosts: Host[]): string {
  if (typeof window === "undefined") {
    return LOCAL_HOST.id;
  }
  const stored = window.localStorage.getItem(SELECTED_HOST_STORAGE_KEY);
  if (stored && hosts.some((host) => host.id === stored)) {
    return stored;
  }
  return hosts[0]?.id ?? LOCAL_HOST.id;
}

export function saveSelectedHostId(id: string): void {
  window.localStorage.setItem(SELECTED_HOST_STORAGE_KEY, id);
}

export function createHostId(): string {
  return `host-${crypto.randomUUID()}`;
}
