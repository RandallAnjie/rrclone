export type Host = {
  id: string;
  name: string;
  url: string;
  user?: string;
  pass?: string;
  locked?: boolean;
};

export type RcErrorBody = {
  error?: string;
  status?: number;
};

export type CoreVersion = {
  version: string;
  os?: string;
  osVersion?: string;
  osKernel?: string;
  osArch?: string;
  arch?: string;
  goVersion?: string;
  linking?: string;
  goTags?: string;
  isGit?: boolean;
  isBeta?: boolean;
};

export type CorePid = {
  pid: number;
};

export type CoreMemStats = {
  Alloc: number;
  Sys: number;
  HeapAlloc: number;
  HeapSys: number;
  HeapInuse: number;
  HeapReleased: number;
};

export type TransferringFile = {
  name: string;
  size?: number;
  bytes?: number;
  percentage?: number;
  speed?: number;
  speedAvg?: number;
  eta?: number | null;
};

export type CoreStats = {
  bytes?: number;
  checks?: number;
  deletes?: number;
  deletedDirs?: number;
  elapsedTime?: number;
  errors?: number;
  eta?: number | null;
  fatalError?: boolean;
  lastError?: string;
  listed?: number;
  retryError?: boolean;
  speed?: number;
  totalBytes?: number;
  totalChecks?: number;
  totalTransfers?: number;
  transferTime?: number;
  transfers?: number;
  transferring?: TransferringFile[];
  checking?: string[];
  serverSideCopies?: number;
  serverSideCopyBytes?: number;
  serverSideMoves?: number;
  serverSideMoveBytes?: number;
};

export type TransferredFile = {
  name: string;
  size?: number;
  bytes?: number;
  checked?: boolean;
  what?: string;
  timestamp?: number;
  error?: string;
  jobid?: number;
};

export type CoreTransferred = {
  transferred?: TransferredFile[];
};

export type CoreBwLimit = {
  bytesPerSecond?: number;
  bytesPerSecondTx?: number;
  bytesPerSecondRx?: number;
  rate?: string;
};

export type ConfigDump = Record<string, Record<string, unknown>>;

export type ConfigListRemotes = {
  remotes?: string[];
};

export type JobList = {
  executeId?: string;
  jobids?: number[];
  runningIds?: number[];
  finishedIds?: number[];
};

export type JobStatus = {
  id?: number;
  finished?: boolean;
  success?: boolean;
  duration?: number;
  startTime?: string;
  endTime?: string;
  error?: string;
  group?: string;
  output?: unknown;
};

export type MountInfo = {
  Fs?: string;
  MountPoint?: string;
  MountedOn?: string;
};

export type MountList = {
  mountPoints?: MountInfo[];
};

export type OperationsAbout = {
  total?: number;
  used?: number;
  free?: number;
  trashed?: number;
};

export type OverviewSnapshot = {
  version: CoreVersion;
  pid: CorePid;
  mem: CoreMemStats;
  stats: CoreStats;
  remotes: ConfigListRemotes;
  jobs: JobList;
  mounts: MountList;
  bwlimit?: CoreBwLimit;
};
