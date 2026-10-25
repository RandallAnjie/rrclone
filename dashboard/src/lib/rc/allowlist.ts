// Server-side guards for the /api/rc proxy.
//
// The proxy forwards requests to whatever rclone RC address the browser
// asks for, so without these checks it is an open relay: anything that
// can reach the dashboard could use it to probe internal hosts or drive
// an unauthenticated `rclone rcd`.

const LOOPBACK_HOSTS = new Set(["localhost", "127.0.0.1", "[::1]", "::1"]);

/**
 * Parse RRCLONE_DASHBOARD_ALLOWED_HOSTS: a comma separated list of
 * hostnames or host:port pairs the proxy may talk to in addition to
 * loopback. "*" allows every host.
 */
export function parseAllowedHosts(raw: string | undefined): string[] {
  return (raw ?? "")
    .split(",")
    .map((item) => item.trim().toLowerCase())
    .filter(Boolean);
}

/** Report whether the proxy may forward to target. */
export function isAllowedTarget(target: URL, allowed: string[]): boolean {
  const hostname = target.hostname.toLowerCase();
  if (LOOPBACK_HOSTS.has(hostname)) {
    return true;
  }
  const host = target.host.toLowerCase();
  return allowed.some((item) => item === "*" || item === hostname || item === host);
}

/**
 * Report whether a request came from the dashboard itself.
 *
 * Browsers send Origin on cross-site POSTs, including "simple" ones with
 * a text/plain body that skip the CORS preflight, so comparing it with
 * Host stops other web pages from driving rclone through the proxy.
 */
export function isSameOrigin(origin: string | null, host: string | null): boolean {
  if (!origin) {
    return true;
  }
  if (!host) {
    return false;
  }
  try {
    return new URL(origin).host.toLowerCase() === host.toLowerCase();
  } catch {
    return false;
  }
}
