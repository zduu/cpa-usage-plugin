#!/usr/bin/env sh
set -eu

REPO="${REPO:-zduu/cpa-usage-plugin}"
DOCKER_CONTAINER="${DOCKER_CONTAINER:-cli-proxy-api}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CONFIG_FILE="${CONFIG_FILE:-$SCRIPT_DIR/config.yaml}"
PLUGIN_DIR="${PLUGIN_DIR:-$SCRIPT_DIR/plugins}"
PLUGIN_FILE="${PLUGIN_FILE:-usage-statistics.so}"
STATE_DIR="${STATE_DIR:-$PLUGIN_DIR}"

LATEST_API="https://api.github.com/repos/$REPO/releases/latest"
LOG_PREFIX="[usage-statistics-updater]"

log() {
  printf '%s %s\n' "$LOG_PREFIX" "$*"
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

# --- skip if already on this version ---
state_file="$STATE_DIR/.usage-statistics.release"
current_tag=""
if [ -f "$state_file" ]; then
  current_tag="$(cat "$state_file")"
fi

if [ -z "$FORCE" ] && [ "$current_tag" = "$target_tag" ] && [ -f "$PLUGIN_DIR/$PLUGIN_FILE" ]; then
  log "already on $target_tag"
  exit 0
fi

# --- download ---
tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

asset_url="https://github.com/$REPO/releases/download/$target_tag/$PLUGIN_FILE"
tmp_file="$tmp_dir/$PLUGIN_FILE"

log "downloading $asset_url"
curl -fL --retry 3 --retry-delay 2 -o "$tmp_file" "$asset_url"

if ! file "$tmp_file" | grep -q 'ELF 64-bit.*x86-64'; then
  log "downloaded file is not a Linux x86_64 shared object"
  exit 1
fi

# --- skip if binary identical ---
new_sha="$(sha256sum "$tmp_file" | awk '{print $1}')"
old_sha=""
if [ -f "$PLUGIN_DIR/$PLUGIN_FILE" ]; then
  old_sha="$(sha256sum "$PLUGIN_DIR/$PLUGIN_FILE" | awk '{print $1}')"
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
