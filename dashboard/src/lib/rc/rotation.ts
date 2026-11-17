import type { RotationAccount, RotationStatus } from "./types";

export type RotationSummary = {
  remotes: number;
  accounts: number;
  coolingDown: number;
  switches: number;
};

/** Totals across every remote with account rotation. */
export function summarizeRotation(rotators: RotationStatus[]): RotationSummary {
  let accounts = 0;
  let coolingDown = 0;
  let switches = 0;
  for (const r of rotators) {
    accounts += r.accounts?.length ?? 0;
    coolingDown += r.accounts?.filter((a) => a.cooling_down).length ?? 0;
    switches += r.switches ?? 0;
  }
  return { remotes: rotators.length, accounts, coolingDown, switches };
}

/** The file name of an account file path, for either separator. */
export function accountName(file?: string): string {
  if (!file) {
    return "—";
  }
  const parts = file.split(/[\\/]/);
  return parts[parts.length - 1] || file;
}

export type AccountState = "active" | "cooling" | "ready";

/** What an account is doing now. */
export function accountState(account: RotationAccount): AccountState {
  if (account.active && !account.cooling_down) {
    return "active";
  }
  return account.cooling_down ? "cooling" : "ready";
}

/** Whether every account of a remote is resting, so it is stuck waiting. */
export function allCoolingDown(r: RotationStatus): boolean {
  const accounts = r.accounts ?? [];
  return accounts.length > 0 && accounts.every((a) => a.cooling_down);
}
