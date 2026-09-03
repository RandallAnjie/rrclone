#!/usr/bin/env bash
# Integration test for install.sh (no root, no GitHub release required).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
INSTALL="${ROOT}/install.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "OK: $*"; }

bash -n "$INSTALL" || fail "bash -n install.sh"

# Source helpers without running main.
# shellcheck disable=SC1090
RRCLONE_INSTALL_SOURCED=1 source "$INSTALL"

# --- unit: invoking_user_home / migrate_config --------------------------------
unit_dir=$(mktemp -d)
trap 'rm -rf "$unit_dir"' EXIT
REAL_HOME="${unit_dir}/home"
unset SUDO_USER || true
unset RRCLONE_MIGRATE_FROM || true
REAL_XDG_CONFIG_HOME=""
mkdir -p "${REAL_HOME}/.config/rclone"
printf '[drive]\ntype = drive\n' > "${REAL_HOME}/.config/rclone/rclone.conf"

home=$(invoking_user_home)
[ "$home" = "$REAL_HOME" ] || fail "invoking_user_home got ${home}"

migrate_config
[ -f "${REAL_HOME}/.config/rrclone/rrclone.conf" ] || fail "config was not copied"
grep -q 'type = drive' "${REAL_HOME}/.config/rrclone/rrclone.conf" || fail "copied config mismatch"

printf '[other]\ntype = alias\n' > "${REAL_HOME}/.config/rclone/rclone.conf"
migrate_config
grep -q 'type = drive' "${REAL_HOME}/.config/rrclone/rrclone.conf" || fail "existing rrclone.conf was overwritten"
pass "migrate_config copies once and does not overwrite"

# --- integration: fake GitHub-style release over HTTP -------------------------
work=$(mktemp -d)
trap 'rm -rf "$unit_dir" "$work"; kill "${httpd_pid:-}" 2>/dev/null || true' EXIT

fake_bin="${work}/payload/rrclone-linux-amd64"
mkdir -p "$fake_bin"
cat > "${fake_bin}/rclone" <<'EOF'
#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "rclone v9.9.9-rrclone"
  exit 0
fi
echo "fake-rrclone $*"
EOF
chmod 755 "${fake_bin}/rclone"
printf 'v9.9.9-rrclone\n' > "${fake_bin}/VERSION"
(
  cd "${work}/payload"
  zip -q -r rrclone-linux-amd64.zip rrclone-linux-amd64
)

www="${work}/www/latest/download"
mkdir -p "$www"
cp "${work}/payload/rrclone-linux-amd64.zip" "$www/"
printf 'rclone v9.9.9-rrclone\n' > "${www}/version.txt"

port_file="${work}/port"
python3 - "$work/www" "$port_file" <<'PY' &
import http.server, socketserver, sys, pathlib
root = pathlib.Path(sys.argv[1])
port_file = pathlib.Path(sys.argv[2])

class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=str(root), **kwargs)
    def log_message(self, fmt, *args):
        pass

httpd = socketserver.TCPServer(("127.0.0.1", 0), Handler)
port_file.write_text(str(httpd.server_address[1]))
httpd.serve_forever()
PY
httpd_pid=$!
for _ in $(seq 1 50); do
  [ -s "$port_file" ] && break
  sleep 0.05
done
[ -s "$port_file" ] || fail "http test server did not start"
port=$(cat "$port_file")
base="http://127.0.0.1:${port}"

prefix="${work}/prefix"
home2="${work}/home2"
mkdir -p "${prefix}/bin" "${home2}/.config/rclone"
printf '[remote]\ntype = local\n' > "${home2}/.config/rclone/rclone.conf"
# Pretend official rclone is already on PATH
cat > "${prefix}/bin/rclone" <<'EOF'
#!/bin/sh
echo "rclone v1.75.0"
EOF
chmod 755 "${prefix}/bin/rclone"

# Official version string matches upstream — must NOT skip install.
export PATH="${prefix}/bin:${PATH}"
export HOME="$home2"
export RRCLONE_RELEASE_BASE="$base"
export RRCLONE_BIN_DIR="${prefix}/bin"
unset XDG_CONFIG_HOME || true

if ! "$INSTALL" --migrate --force >/tmp/rrclone-install-test.log 2>&1; then
  cat /tmp/rrclone-install-test.log >&2
  fail "install.sh --migrate failed"
fi

[ -x "${prefix}/bin/rrclone" ] || fail "rrclone not installed"
[ -x "${prefix}/bin/rclone" ] || fail "rclone replacement not installed"
[ -x "${prefix}/bin/rclone.official.bak" ] || fail "official binary was not backed up"
"${prefix}/bin/rrclone" --version | grep -q 'v9.9.9-rrclone' || fail "new binary version mismatch"
"${prefix}/bin/rclone" --version | grep -q 'v9.9.9-rrclone' || fail "replaced rclone is not rrclone"
grep -q 'type = local' "${home2}/.config/rrclone/rrclone.conf" || fail "config migrate failed"
pass "install.sh --migrate replaces rclone, backups official binary, copies config"

# Already up to date should exit 3
set +e
"$INSTALL" >/tmp/rrclone-install-skip.log 2>&1
rc=$?
set -e
[ "$rc" -eq 3 ] || { cat /tmp/rrclone-install-skip.log >&2; fail "expected exit 3 when up to date, got ${rc}"; }
pass "install.sh exits 3 when rrclone is already current"

# Official rclone with the same version string as version.txt must still install.
# (rrclone --version still starts with "rclone v…", so a naive compare would skip.)
samever="${work}/samever"
mkdir -p "${samever}/www/latest/download" "${samever}/bin" "${samever}/home"
cat > "${samever}/bin/rclone" <<'EOF'
#!/bin/sh
echo "rclone v1.75.0"
EOF
chmod 755 "${samever}/bin/rclone"
# Reuse the zip but advertise an official-looking version.txt
cp "${work}/payload/rrclone-linux-amd64.zip" "${samever}/www/latest/download/"
printf 'rclone v1.75.0\n' > "${samever}/www/latest/download/version.txt"
same_port_file="${samever}/port"
python3 - "$samever/www" "$same_port_file" <<'PY' &
import http.server, socketserver, sys, pathlib
root = pathlib.Path(sys.argv[1])
port_file = pathlib.Path(sys.argv[2])
class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=str(root), **kwargs)
    def log_message(self, fmt, *args):
        pass
httpd = socketserver.TCPServer(("127.0.0.1", 0), Handler)
port_file.write_text(str(httpd.server_address[1]))
httpd.serve_forever()
PY
same_httpd=$!
trap 'rm -rf "$unit_dir" "$work"; kill "${httpd_pid:-}" "${same_httpd:-}" 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do
  [ -s "$same_port_file" ] && break
  sleep 0.05
done
export PATH="${samever}/bin:${PATH}"
export HOME="${samever}/home"
export RRCLONE_RELEASE_BASE="http://127.0.0.1:$(cat "$same_port_file")"
export RRCLONE_BIN_DIR="${samever}/bin"
"$INSTALL" >/tmp/rrclone-install-official.log 2>&1 || {
  cat /tmp/rrclone-install-official.log >&2
  fail "install should not treat official rclone as already-current rrclone"
}
"${samever}/bin/rrclone" --version | grep -q rrclone || fail "official same-version install did not place rrclone"
pass "install.sh does not skip when only official rclone is present"

# --no-replace leaves a different rclone alone
prefix3="${work}/prefix3"
mkdir -p "${prefix3}/bin"
cat > "${prefix3}/bin/rclone" <<'EOF'
#!/bin/sh
echo "rclone v1.75.0"
EOF
chmod 755 "${prefix3}/bin/rclone"
export PATH="${prefix3}/bin:${PATH}"
export RRCLONE_BIN_DIR="${prefix3}/bin"
export HOME="${work}/home3"
mkdir -p "$HOME"
"$INSTALL" --no-replace --force >/tmp/rrclone-install-noreplace.log 2>&1 || {
  cat /tmp/rrclone-install-noreplace.log >&2
  fail "install.sh --no-replace failed"
}
[ -x "${prefix3}/bin/rrclone" ] || fail "rrclone missing after --no-replace"
"${prefix3}/bin/rclone" --version | grep -qx 'rclone v1.75.0' || fail "--no-replace overwrote rclone"
[ ! -e "${prefix3}/bin/rclone.official.bak" ] || fail "--no-replace should not backup/replace rclone"
pass "install.sh --no-replace installs rrclone only"

echo
echo "All install.sh tests passed."
