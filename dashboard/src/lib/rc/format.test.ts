import { describe, expect, it } from "vitest";
import {
  formatBytes,
  formatDuration,
  formatSpeed,
  isBlockedRcHost,
  isSensitiveKey,
  normalizeHostUrl,
  parseHostUrl,
  redactDeep,
  redactRecord,
} from "./format";

describe("formatBytes", () => {
  it("formats small and large sizes", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(1048576)).toBe("1.0 MiB");
  });
});

describe("formatSpeed", () => {
  it("adds a per-second suffix", () => {
    expect(formatSpeed(2048)).toBe("2.0 KiB/s");
  });
});

describe("formatDuration", () => {
  it("uses compact units", () => {
    expect(formatDuration(9)).toBe("9s");
    expect(formatDuration(75)).toBe("1m 15s");
    expect(formatDuration(3661)).toBe("1h 1m");
  });
});

describe("host url parsing", () => {
  it("normalizes a trailing slash", () => {
    expect(normalizeHostUrl("http://127.0.0.1:5572/")).toBe("http://127.0.0.1:5572");
  });

  it("rejects credentials in the URL", () => {
    expect(() => parseHostUrl("http://gui:secret@127.0.0.1:5572")).toThrow(/不要把账号密码/);
  });

  it("rejects non-http schemes", () => {
    expect(() => parseHostUrl("file:///etc/passwd")).toThrow(/只允许/);
  });

  it("rejects cloud metadata hosts", () => {
    expect(isBlockedRcHost("169.254.169.254")).toBe(true);
    expect(isBlockedRcHost("metadata.google.internal")).toBe(true);
    expect(() => parseHostUrl("http://169.254.169.254/")).toThrow(/metadata/);
  });
});

describe("redactRecord", () => {
  it("hides tokens and secrets", () => {
    expect(isSensitiveKey("token")).toBe(true);
    expect(
      redactRecord({
        type: "drive",
        token: "abc",
        client_secret: "xyz",
        scope: "drive",
      }),
    ).toEqual({
      type: "drive",
      token: "••••",
      client_secret: "••••",
      scope: "drive",
    });
  });

  it("redacts nested tokens before they leave the BFF", () => {
    expect(
      redactDeep({
        drive: { type: "drive", token: "abc", nested: { client_secret: "xyz" } },
      }),
    ).toEqual({
      drive: { type: "drive", token: "••••", nested: { client_secret: "••••" } },
    });
  });
});
