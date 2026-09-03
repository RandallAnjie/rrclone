"use client";

import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { normalizeHostUrl } from "@/lib/rc/format";
import {
  createHostId,
  defaultHosts,
  HOSTS_STORAGE_KEY,
  loadHosts,
  loadSelectedHostId,
  LOCAL_HOST,
  saveHosts,
  saveSelectedHostId,
  SELECTED_HOST_STORAGE_KEY,
} from "@/lib/rc/hosts";
import type { Host } from "@/lib/rc/types";

type HostDraft = {
  name: string;
  url: string;
  user?: string;
  pass?: string;
};

type HostContextValue = {
  hosts: Host[];
  selectedId: string;
  host: Host;
  ready: boolean;
  selectHost: (id: string) => void;
  addHost: (draft: HostDraft) => Host;
  updateHost: (id: string, draft: HostDraft) => void;
  removeHost: (id: string) => void;
};

const HostContext = createContext<HostContextValue | null>(null);
const HOSTS_EVENT = "rrclone-hosts-changed";

function subscribeHosts(onStoreChange: () => void) {
  const handler = () => onStoreChange();
  window.addEventListener("storage", handler);
  window.addEventListener(HOSTS_EVENT, handler);
  return () => {
    window.removeEventListener("storage", handler);
    window.removeEventListener(HOSTS_EVENT, handler);
  };
}

function getHostsSnapshot() {
  return window.localStorage.getItem(HOSTS_STORAGE_KEY) ?? "";
}

function getSelectedSnapshot() {
  return window.localStorage.getItem(SELECTED_HOST_STORAGE_KEY) ?? "";
}

function getServerSnapshot() {
  return "";
}

function notifyHostsChanged() {
  window.dispatchEvent(new Event(HOSTS_EVENT));
}

export function HostProvider({ children }: { children: ReactNode }) {
  const hostsJson = useSyncExternalStore(subscribeHosts, getHostsSnapshot, getServerSnapshot);
  const selectedJson = useSyncExternalStore(subscribeHosts, getSelectedSnapshot, getServerSnapshot);
  const ready = hostsJson !== undefined;

  const hosts = useMemo(() => {
    if (!hostsJson) {
      return defaultHosts();
    }
    return loadHosts();
  }, [hostsJson]);

  const selectedId = useMemo(() => {
    if (!selectedJson) {
      return loadSelectedHostId(hosts);
    }
    return loadSelectedHostId(hosts);
  }, [hosts, selectedJson]);

  const persist = useCallback((nextHosts: Host[], nextSelected: string) => {
    saveHosts(nextHosts);
    saveSelectedHostId(nextSelected);
    notifyHostsChanged();
  }, []);

  const selectHost = useCallback(
    (id: string) => {
      if (!hosts.some((item) => item.id === id)) {
        return;
      }
      persist(hosts, id);
    },
    [hosts, persist],
  );

  const addHost = useCallback(
    (draft: HostDraft) => {
      const host: Host = {
        id: createHostId(),
        name: draft.name.trim() || "未命名主机",
        url: normalizeHostUrl(draft.url),
        user: draft.user?.trim() || undefined,
        pass: draft.pass || undefined,
      };
      persist([...hosts, host], host.id);
      return host;
    },
    [hosts, persist],
  );

  const updateHost = useCallback(
    (id: string, draft: HostDraft) => {
      const next = hosts.map((item) =>
        item.id === id
          ? {
              ...item,
              name: draft.name.trim() || item.name,
              url: item.locked ? item.url : normalizeHostUrl(draft.url),
              user: draft.user?.trim() || undefined,
              pass: draft.pass || undefined,
            }
          : item,
      );
      persist(next, selectedId);
    },
    [hosts, persist, selectedId],
  );

  const removeHost = useCallback(
    (id: string) => {
      const target = hosts.find((item) => item.id === id);
      if (!target || target.locked) {
        return;
      }
      persist(
        hosts.filter((item) => item.id !== id),
        selectedId === id ? LOCAL_HOST.id : selectedId,
      );
    },
    [hosts, persist, selectedId],
  );

  const host = hosts.find((item) => item.id === selectedId) ?? hosts[0] ?? LOCAL_HOST;

  const value = useMemo(
    () => ({
      hosts,
      selectedId: host.id,
      host,
      ready,
      selectHost,
      addHost,
      updateHost,
      removeHost,
    }),
    [addHost, host, hosts, ready, removeHost, selectHost, updateHost],
  );

  return <HostContext.Provider value={value}>{children}</HostContext.Provider>;
}

export function useHosts(): HostContextValue {
  const ctx = useContext(HostContext);
  if (!ctx) {
    throw new Error("useHosts must be used within HostProvider");
  }
  return ctx;
}
