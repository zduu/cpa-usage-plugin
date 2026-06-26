#!/usr/bin/env sh
set -eu

REPO="${REPO:-zduu/cpa-usage-plugin}"
DOCKER_CONTAINER="${DOCKER_CONTAINER:-cli-proxy-api}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CONFIG_FILE="${CONFIG_FILE:-$SCRIPT_DIR/config.yaml}"
PLUGIN_DIR="${PLUGIN_DIR:-$SCRIPT_DIR/plugins}"
PLUGIN_FILE="${PLUGIN_FILE:-}"
STATE_DIR="${STATE_DIR:-$PLUGIN_DIR}"
PLUGIN_PLATFORM="${PLUGIN_PLATFORM:-}"
PLUGIN_ASSET="${PLUGIN_ASSET:-}"

LATEST_API="https://api.github.com/repos/$REPO/releases/latest"
LOG_PREFIX="[usage-statistics-updater]"

log() {
  printf '%s %s\n' "$LOG_PREFIX" "$*"
}

detect_platform() {
  os="$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m 2>/dev/null | tr '[:upper:]' '[:lower:]')"
  case "$os" in
    linux) os="linux" ;;
    darwin) os="darwin" ;;
    mingw*|msys*|cygwin*) os="windows" ;;
    *) log "unsupported OS '$os'; set PLUGIN_PLATFORM manually"; exit 1 ;;
  esac
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) log "unsupported architecture '$arch'; set PLUGIN_PLATFORM manually"; exit 1 ;;
  esac
  printf '%s-%s' "$os" "$arch"
}

asset_for_platform() {
  platform="$1"
  case "$platform" in
    linux-amd64|linux-arm64) printf 'usage-statistics-%s.so' "$platform" ;;
    darwin-amd64|darwin-arm64) printf 'usage-statistics-%s.dylib' "$platform" ;;
    windows-amd64) printf 'usage-statistics-%s.dll' "$platform" ;;
    *) log "unsupported plugin platform '$platform'"; exit 1 ;;
  esac
}

plugin_file_for_platform() {
  platform="$1"
  case "$platform" in
    linux-*) printf 'usage-statistics.so' ;;
    darwin-*) printf 'usage-statistics.dylib' ;;
    windows-*) printf 'usage-statistics.dll' ;;
    *) log "unsupported plugin platform '$platform'"; exit 1 ;;
  esac
}

file_pattern_for_platform() {
  platform="$1"
  case "$platform" in
    linux-amd64) printf 'ELF 64-bit.*x86-64' ;;
    linux-arm64) printf 'ELF 64-bit.*\(ARM aarch64\|aarch64\)' ;;
    darwin-amd64) printf 'Mach-O 64-bit.*x86_64' ;;
    darwin-arm64) printf 'Mach-O 64-bit.*arm64' ;;
    windows-amd64) printf 'PE32+.*x86-64' ;;
    *) log "unsupported plugin platform '$platform'"; exit 1 ;;
  esac
}

file_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
    return
  fi
  log "sha256sum or shasum is required"
  exit 1
}

# --- flag parsing ---
FORCE=
RESTART=
for arg do
  case "$arg" in
    --force)   FORCE=1 ;;
    --restart) RESTART=1 ;;
    --auto-restart) RESTART=1 ;;
    *) printf 'unknown arg: %s\n' "$arg"; exit 1 ;;
  esac
done

# --- config ---
yaml_value() {
  key="$1"
  [ -f "$CONFIG_FILE" ] || return 0
  sed -n "s/^[[:space:]]*$key:[[:space:]]*//p" "$CONFIG_FILE" |
    head -n 1 |
    sed 's/[[:space:]]*#.*$//' |
    sed 's/^[[:space:]]*//;s/[[:space:]]*$//;s/^["'\'']//;s/["'\'']$//'
}

enabled="${UPDATE_ENABLED:-$(yaml_value update_enabled)}"
case "$(printf '%s' "$enabled" | tr '[:upper:]' '[:lower:]')" in
  true|yes|on|1) ;;
  *)
    log "update is disabled; set update_enabled: true in $CONFIG_FILE"
    exit 0
    ;;
esac

target_version="${UPDATE_VERSION:-$(yaml_value update_version)}"
if [ -z "$target_version" ]; then
  target_version="latest"
fi

normalized_target_version="$(printf '%s' "$target_version" | tr '[:upper:]' '[:lower:]')"
case "$normalized_target_version" in
  latest)
    target_version="latest"
    ;;
  lastest|vlastest)
    log "normalizing update_version typo '$target_version' to 'latest'"
    target_version="latest"
    ;;
esac

# --- resolve tag ---
if [ "$target_version" = "latest" ]; then
  latest_json="$(curl -fsSL "$LATEST_API")"
  target_tag="$(printf '%s\n' "$latest_json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
else
  target_tag="$target_version"
  case "$target_tag" in
    v*) ;;
    *) target_tag="v$target_tag" ;;
  esac
fi

if [ -z "$target_tag" ]; then
  log "failed to resolve target release tag"
  exit 1
fi

if [ -z "$PLUGIN_PLATFORM" ]; then
  PLUGIN_PLATFORM="$(detect_platform)"
fi
if [ -z "$PLUGIN_FILE" ]; then
  PLUGIN_FILE="$(plugin_file_for_platform "$PLUGIN_PLATFORM")"
fi
asset_auto=
if [ -z "$PLUGIN_ASSET" ]; then
  PLUGIN_ASSET="$(asset_for_platform "$PLUGIN_PLATFORM")"
  asset_auto=1
fi

# --- skip if already on this version ---
state_file="$STATE_DIR/.usage-statistics.release.$PLUGIN_PLATFORM"
legacy_state_file="$STATE_DIR/.usage-statistics.release"
mkdir -p "$STATE_DIR"
current_tag=""
if [ -f "$state_file" ]; then
	current_tag="$(cat "$state_file")"
elif [ "$PLUGIN_PLATFORM" = "linux-amd64" ] && [ -f "$legacy_state_file" ]; then
	current_tag="$(cat "$legacy_state_file")"
fi

if [ -z "$FORCE" ] && [ "$current_tag" = "$target_tag" ] && [ -f "$PLUGIN_DIR/$PLUGIN_FILE" ]; then
	printf '%s' "$target_tag" > "$state_file"
	log "already on $target_tag"
	exit 0
fi

# --- download ---
tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

asset_url="https://github.com/$REPO/releases/download/$target_tag/$PLUGIN_ASSET"
tmp_file="$tmp_dir/$PLUGIN_ASSET"

log "downloading $asset_url for $PLUGIN_PLATFORM"
if ! curl -fL --retry 3 --retry-delay 2 -o "$tmp_file" "$asset_url"; then
  if [ -n "$asset_auto" ] && [ "$PLUGIN_PLATFORM" = "linux-amd64" ]; then
    legacy_url="https://github.com/$REPO/releases/download/$target_tag/usage-statistics.so"
    tmp_file="$tmp_dir/usage-statistics.so"
    log "platform asset not found, falling back to legacy amd64 asset $legacy_url"
    curl -fL --retry 3 --retry-delay 2 -o "$tmp_file" "$legacy_url"
  else
    exit 1
  fi
fi

if ! file "$tmp_file" | grep -q "$(file_pattern_for_platform "$PLUGIN_PLATFORM")"; then
  log "downloaded file does not match $PLUGIN_PLATFORM"
  exit 1
fi

# --- skip if binary identical ---
new_sha="$(file_sha256 "$tmp_file")"
old_sha=""
if [ -f "$PLUGIN_DIR/$PLUGIN_FILE" ]; then
  old_sha="$(file_sha256 "$PLUGIN_DIR/$PLUGIN_FILE")"
fi

if [ -z "$FORCE" ] && [ -n "$old_sha" ] && [ "$old_sha" = "$new_sha" ]; then
  printf '%s' "$target_tag" > "$state_file"
  log "binary unchanged: $target_tag $new_sha"
  exit 0
fi

# --- install ---
mkdir -p "$PLUGIN_DIR"
if [ -f "$PLUGIN_DIR/$PLUGIN_FILE" ]; then
  backup="$PLUGIN_DIR/$PLUGIN_FILE.bak-$(date +%Y%m%d-%H%M%S)"
  cp "$PLUGIN_DIR/$PLUGIN_FILE" "$backup"
  log "backup created: $backup"
fi

cp "$tmp_file" "$PLUGIN_DIR/$PLUGIN_FILE"
chmod 755 "$PLUGIN_DIR/$PLUGIN_FILE"
printf '%s' "$target_tag" > "$state_file"

log "installed $target_tag sha256=$new_sha"

# --- restart ---
if [ -n "$RESTART" ]; then
  if command -v docker >/dev/null 2>&1; then
    log "restarting Docker container '$DOCKER_CONTAINER'..."
    docker restart "$DOCKER_CONTAINER" 2>&1 | while IFS= read -r line; do log "docker: $line"; done
    log "restart done"
  else
    log "docker not found, skipping restart"
  fi
else
  log "restart CPA manually to load the new plugin"
fi
