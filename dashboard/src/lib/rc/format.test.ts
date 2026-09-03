import { describe, expect, it } from "vitest";
import {
  formatBytes,
  formatDuration,
  formatSpeed,
  isSensitiveKey,
  normalizeHostUrl,
  parseHostUrl,
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
});
