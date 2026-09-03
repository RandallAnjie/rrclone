#!/usr/bin/env bash
# rrclone one-click installer (modeled on https://rclone.org/install.sh)
#
# Install (replace official rclone, keep a .official.bak copy of the binary):
#   sudo -v ; curl -fsSL https://raw.githubusercontent.com/RandallAnjie/rrclone/master/install.sh | sudo bash
#
# Migrate from official rclone (backup binary + copy rclone.conf → rrclone.conf):
#   sudo -v ; curl -fsSL https://raw.githubusercontent.com/RandallAnjie/rrclone/master/install.sh | sudo bash -s -- --migrate
#
# Install as rrclone only (do not replace /usr/bin/rclone):
#   ... | sudo bash -s -- --no-replace
#
# Exit codes:
#   0 - success
#   1 - bad args / unexpected error
#   2 - OS / arch not supported
#   3 - already up to date
#   4 - no unzip tool
#   5 - download / release missing (and no Go fallback)

set -euo pipefail

REPO="${RRCLONE_REPO:-RandallAnjie/rrclone}"
RELEASE_BASE="${RRCLONE_RELEASE_BASE:-https://github.com/${REPO}/releases}"
RAW_BASE="${RRCLONE_RAW_BASE:-https://raw.githubusercontent.com/${REPO}/master}"

unzip_tools_list=('unzip' '7z' 'busybox')

migrate=0
replace=1
force=0

# Saved before the installer overrides XDG_CONFIG_HOME (see rclone #2127).
REAL_XDG_CONFIG_HOME="${XDG_CONFIG_HOME:-}"
REAL_HOME="${HOME:-}"

SCRIPT_PATH="${BASH_SOURCE[0]:-}"

usage() {
  cat <<EOF 1>&2
Usage:
  sudo -v ; curl -fsSL ${RAW_BASE}/install.sh | sudo bash
  sudo -v ; curl -fsSL ${RAW_BASE}/install.sh | sudo bash -s -- [options]

Options:
  --migrate      Backup official rclone binary and copy rclone.conf → rrclone.conf
  --no-replace   Install rrclone only; leave the existing rclone command alone
  --force        Reinstall even if the same rrclone version is already present
  -h, --help     Show this help

Environment:
  RRCLONE_REPO           GitHub repo (default: RandallAnjie/rrclone)
  RRCLONE_RELEASE_BASE   Release download base URL
  RRCLONE_BIN_DIR        Binary install dir (default: /usr/bin, or /usr/local/bin on macOS)
  RRCLONE_MIGRATE_FROM   Explicit path to an official rclone.conf to copy
  RRCLONE_SOURCE_DIR     Local source tree for Go fallback (skips git clone)
EOF
  exit 1
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --migrate) migrate=1 ;;
      --no-replace) replace=0 ;;
      --force) force=1 ;;
      -h|--help) usage ;;
      *)
        echo "Unknown option: $1" 1>&2
        usage
        ;;
    esac
    shift
  done
}

log() { printf '%s\n' "$*"; }

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Required command not found: $1" 1>&2
    exit 1
  fi
}

# Local checkout when this file is run from a git clone (not curl | bash).
detect_source_dir() {
  if [ -n "${RRCLONE_SOURCE_DIR:-}" ] && [ -f "${RRCLONE_SOURCE_DIR}/go.mod" ]; then
    printf '%s' "$RRCLONE_SOURCE_DIR"
    return 0
  fi
  if [ -n "$SCRIPT_PATH" ] && [ -f "$SCRIPT_PATH" ]; then
    local dir
    dir=$(cd "$(dirname "$SCRIPT_PATH")" && pwd)
    if [ -f "$dir/go.mod" ] && grep -q 'module github.com/rclone/rclone' "$dir/go.mod" 2>/dev/null; then
      printf '%s' "$dir"
      return 0
    fi
  fi
  return 1
}

detect_platform() {
  OS="$(uname)"
  case "$OS" in
    Linux) OS='linux' ;;
    FreeBSD) OS='freebsd' ;;
    NetBSD) OS='netbsd' ;;
    OpenBSD) OS='openbsd' ;;
    Darwin) OS='osx' ;;
    *)
      echo 'OS not supported'
      exit 2
      ;;
  esac

  OS_type="$(uname -m)"
  case "$OS_type" in
    x86_64|amd64) OS_type='amd64' ;;
    i?86|x86) OS_type='386' ;;
    aarch64|arm64) OS_type='arm64' ;;
    armv7*) OS_type='arm-v7' ;;
    armv6*) OS_type='arm-v6' ;;
    arm*) OS_type='arm' ;;
    *)
      echo 'OS type not supported'
      exit 2
      ;;
  esac

  if [ "$OS" = "osx" ]; then
    binTgtDir="${RRCLONE_BIN_DIR:-/usr/local/bin}"
    man1TgtDir=/usr/local/share/man/man1
  elif [ "$OS" = "freebsd" ] || [ "$OS" = "openbsd" ] || [ "$OS" = "netbsd" ]; then
    binTgtDir="${RRCLONE_BIN_DIR:-/usr/bin}"
    man1TgtDir=/usr/local/man/man1
  else
    binTgtDir="${RRCLONE_BIN_DIR:-/usr/bin}"
    man1TgtDir=/usr/local/share/man/man1
  fi
}

choose_unzip_tool() {
  unzip_tool=""
  local tool
  set +e
  for tool in "${unzip_tools_list[@]}"; do
    if hash "$tool" 2>/dev/null; then
      unzip_tool="$tool"
      break
    fi
  done
  set -e
  if [ -z "$unzip_tool" ]; then
    printf '\nNone of the supported unzip tools (%s) were found.\nPlease install one and retry.\n\n' "${unzip_tools_list[*]}"
    exit 4
  fi
}

# Home of the user who invoked sudo, not root's.
invoking_user_home() {
  local user="${SUDO_USER:-}"
  local home=""

  case "$user" in
    ''|root) user="" ;;
    *[!a-zA-Z0-9._-]*) user="" ;;
  esac

  if [ -n "$user" ]; then
    if command -v getent >/dev/null 2>&1; then
      home=$(getent passwd "$user" | cut -d: -f6 || true)
    fi
    if [ -z "$home" ]; then
      home=$(eval echo "~$user")
    fi
  fi

  if [ -z "$home" ] || [ "$home" = "~${user}" ]; then
    home="$REAL_HOME"
  fi
  printf '%s' "$home"
}

invoking_user_group() {
  local user="${SUDO_USER:-}"
  if [ -n "$user" ] && [ "$user" != "root" ]; then
    id -gn "$user" 2>/dev/null || printf '%s' "$user"
  fi
}

same_file() {
  local a="$1" b="$2"
  [ -e "$a" ] && [ -e "$b" ] || return 1
  cmp -s "$a" "$b"
}

# Official rclone --version is "rclone v1.75.0". A matching string must NOT
# skip install unless rrclone is already present.
is_rrclone_install() {
  if [ -x "${binTgtDir}/rrclone" ]; then
    return 0
  fi
  if [ -n "${installed_version:-}" ] && printf '%s' "$installed_version" | grep -qi rrclone; then
    return 0
  fi
  return 1
}

probe_installed_version() {
  installed_version=""
  installed_bin=""
  if command -v rrclone >/dev/null 2>&1; then
    installed_bin=$(command -v rrclone)
  elif command -v rclone >/dev/null 2>&1; then
    installed_bin=$(command -v rclone)
  else
    return 0
  fi
  installed_version=$("$installed_bin" --version 2>/dev/null | head -n 1 || true)
}

install_binary() {
  local src="$1"
  local dest="$2"
  mkdir -p "$(dirname "$dest")"
  cp "$src" "${dest}.new"
  chmod 755 "${dest}.new"
  case "$OS" in
    linux)
      chown root:root "${dest}.new" 2>/dev/null || true
      ;;
    freebsd|openbsd|netbsd)
      chown root:wheel "${dest}.new" 2>/dev/null || true
      ;;
  esac
  mv "${dest}.new" "$dest"
}

backup_official_rclone() {
  local existing
  existing=$(command -v rclone 2>/dev/null || true)
  if [ -z "$existing" ] || [ ! -e "$existing" ]; then
    return 0
  fi
  if same_file "$existing" "${binTgtDir}/rrclone"; then
    return 0
  fi
  if printf '%s' "$installed_version" | grep -qi rrclone; then
    return 0
  fi
  local bak="${existing}.official.bak"
  if [ ! -e "$bak" ]; then
    cp -a "$existing" "$bak"
    log "Backed up existing rclone to ${bak}"
  else
    log "Official rclone backup already exists at ${bak}"
  fi
}

# Copy official rclone.conf → ~/.config/rrclone/rrclone.conf when dest is absent.
migrate_config() {
  local user_home dest_dir dest src="" candidate
  user_home=$(invoking_user_home)

  for candidate in \
    "${RRCLONE_MIGRATE_FROM:-}" \
    "${user_home}/.config/rclone/rclone.conf" \
    "${REAL_XDG_CONFIG_HOME:+${REAL_XDG_CONFIG_HOME}/rclone/rclone.conf}" \
    "${user_home}/.rclone.conf"
  do
    if [ -n "$candidate" ] && [ -f "$candidate" ]; then
      src="$candidate"
      break
    fi
  done

  if [ -z "$src" ]; then
    log "No official rclone.conf found to migrate (looked under ~/.config/rclone)."
    return 0
  fi

  dest_dir="${user_home}/.config/rrclone"
  dest="${dest_dir}/rrclone.conf"
  mkdir -p "$dest_dir"
  if [ -f "$dest" ]; then
    log "rrclone config already exists at ${dest} — leaving it unchanged."
    return 0
  fi

  cp -a "$src" "$dest"
  if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
    local group
    group=$(invoking_user_group)
    chown -R "${SUDO_USER}:${group}" "$dest_dir" 2>/dev/null || true
  fi
  log "Migrated config:"
  log "  ${src}"
  log "  → ${dest}"
  log "Tip: rrclone also accepts RCLONE_* env vars as a fallback for RRCLONE_*."
}

extract_zip() {
  local zipfile="$1"
  local unzip_dir="tmp_unzip_dir_for_rrclone"
  case "$unzip_tool" in
    unzip) unzip -a "$zipfile" -d "$unzip_dir" ;;
    7z) 7z x "$zipfile" "-o$unzip_dir" ;;
    busybox)
      mkdir -p "$unzip_dir"
      busybox unzip "$zipfile" -d "$unzip_dir"
      ;;
  esac
  # Zip layout: rrclone-<os>-<arch>/rclone  (and optional rclone.1)
  cd "$unzip_dir"
  if [ -d "rrclone-${OS}-${OS_type}" ]; then
    cd "rrclone-${OS}-${OS_type}"
  else
    cd ./*
  fi
}

build_from_source() {
  local src="$1"
  if ! command -v go >/dev/null 2>&1; then
    return 1
  fi
  log "Building rrclone from source in ${src} ..."
  (
    cd "$src"
    CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "${tmp_dir}/rrclone" .
  )
  mkdir -p "${tmp_dir}/extract"
  cp "${tmp_dir}/rrclone" "${tmp_dir}/extract/rclone"
  if [ -f "${src}/rclone.1" ]; then
    cp "${src}/rclone.1" "${tmp_dir}/extract/rclone.1"
  fi
  cd "${tmp_dir}/extract"
}

rrclone_install_main() {
  parse_args "$@"
  need_cmd curl
  detect_platform
  choose_unzip_tool

  mkdir -p "$binTgtDir"
  if [ ! -w "$binTgtDir" ]; then
    echo "Cannot write to ${binTgtDir}. Re-run with sudo, or set RRCLONE_BIN_DIR to a writable directory." 1>&2
    exit 1
  fi

  tmp_dir=$(mktemp -d 2>/dev/null || mktemp -d -t 'rrclone-install.XXXXXXXXXX')
  cleanup() { rm -rf "$tmp_dir"; }
  trap cleanup EXIT
  cd "$tmp_dir"

  # Avoid creating a root-owned config directory while probing version (#2127)
  export XDG_CONFIG_HOME="${tmp_dir}/config"

  probe_installed_version

  local asset version_url download_link current_version="" download_ok=0
  asset="rrclone-${OS}-${OS_type}.zip"
  version_url="${RELEASE_BASE}/latest/download/version.txt"
  download_link="${RELEASE_BASE}/latest/download/${asset}"

  if current_version=$(curl -fsSL "$version_url" 2>/dev/null); then
    current_version=$(printf '%s' "$current_version" | tr -d '\r' | head -n 1)
  else
    current_version=""
  fi

  if [ "$force" -eq 0 ] && is_rrclone_install && [ -n "$installed_version" ] && [ -n "$current_version" ] && [ "$installed_version" = "$current_version" ]; then
    printf '\nThe latest version of rrclone (%s) is already installed.\n\n' "$installed_version"
    exit 3
  fi

  if curl -fsSL -o "$asset" "$download_link"; then
    download_ok=1
  else
    printf '\nCould not download %s\n' "$download_link"
    printf 'No published GitHub Release asset yet, or this OS/arch is missing.\n'
  fi

  if [ "$download_ok" -eq 1 ]; then
    extract_zip "$asset"
  else
    local src_dir=""
    if src_dir=$(detect_source_dir); then
      if ! build_from_source "$src_dir"; then
        echo "Go build from ${src_dir} failed." 1>&2
        exit 5
      fi
    elif command -v go >/dev/null 2>&1 && command -v git >/dev/null 2>&1; then
      log "Falling back to git clone + go build..."
      git clone --depth 1 "https://github.com/${REPO}.git" "${tmp_dir}/src"
      if ! build_from_source "${tmp_dir}/src"; then
        echo "Go build failed." 1>&2
        exit 5
      fi
    else
      printf 'Install Go (and git), or publish a GitHub Release with %s, then retry.\n\n' "$asset"
      exit 5
    fi
  fi

  if [ ! -f rclone ] && [ -f rrclone ]; then
    mv rrclone rclone
  fi
  if [ ! -f rclone ]; then
    echo "Downloaded archive did not contain an rclone/rrclone binary" 1>&2
    exit 1
  fi

  if [ "$migrate" -eq 1 ]; then
    backup_official_rclone
    unset XDG_CONFIG_HOME
    migrate_config
    export XDG_CONFIG_HOME="${tmp_dir}/config"
  elif [ "$replace" -eq 1 ] && command -v rclone >/dev/null 2>&1; then
    backup_official_rclone || true
  fi

  install_binary rclone "${binTgtDir}/rrclone"
  log "Installed ${binTgtDir}/rrclone"

  if [ "$replace" -eq 1 ]; then
    install_binary rclone "${binTgtDir}/rclone"
    log "Installed ${binTgtDir}/rclone (drop-in replacement for official rclone)"
  else
    log "Left existing rclone command unchanged (--no-replace)."
  fi

  if [ -f rclone.1 ]; then
    if mkdir -p "$man1TgtDir" 2>/dev/null && [ -w "$man1TgtDir" ]; then
      cp rclone.1 "${man1TgtDir}/rclone.1"
      chmod a=r "${man1TgtDir}/rclone.1" 2>/dev/null || true
      if command -v mandb >/dev/null 2>&1; then
        mandb >/dev/null 2>&1 || true
      elif command -v makewhatis >/dev/null 2>&1; then
        makewhatis >/dev/null 2>&1 || true
      fi
    else
      log "Man page directory ${man1TgtDir} is not writable; skipping man page."
    fi
  fi

  local version
  version=$("${binTgtDir}/rrclone" --version 2>/dev/null | head -n 1 || echo "rrclone")

  printf '\n%s has successfully installed as rrclone' "$version"
  if [ "$replace" -eq 1 ]; then
    printf ' (and rclone)'
  fi
  printf '.\n'
  if [ "$migrate" -eq 1 ]; then
    printf 'Config migration attempted; default config path is ~/.config/rrclone/rrclone.conf\n'
  else
    printf 'Now run "rrclone config" (or "rclone config" if replaced).\n'
    printf 'To migrate an existing official rclone install and config:\n'
    printf '  curl -fsSL %s/install.sh | sudo bash -s -- --migrate\n' "$RAW_BASE"
  fi
  printf 'Docs: https://github.com/%s\n\n' "$REPO"
  exit 0
}

if [ "${RRCLONE_INSTALL_SOURCED:-}" != "1" ]; then
  rrclone_install_main "$@"
fi
