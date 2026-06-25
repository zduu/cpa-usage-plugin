#!/usr/bin/env sh
set -eu

REPO="${REPO:-zduu/cpa-usage-plugin}"
CONFIG_FILE="${CONFIG_FILE:-/home/zoex/docker/CLIProxyAPI/config.yaml}"
PLUGIN_DIR="${PLUGIN_DIR:-/home/zoex/docker/CLIProxyAPI/plugins}"
PLUGIN_FILE="${PLUGIN_FILE:-usage-statistics.so}"
STATE_DIR="${STATE_DIR:-$PLUGIN_DIR}"
FORCE="${1:-}"

LATEST_API="https://api.github.com/repos/$REPO/releases/latest"
LOG_PREFIX="[usage-statistics-updater]"

log() {
  printf '%s %s\n' "$LOG_PREFIX" "$*"
}

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

state_file="$STATE_DIR/.usage-statistics.release"
current_tag=""
if [ -f "$state_file" ]; then
  current_tag="$(cat "$state_file")"
fi

if [ "$FORCE" != "--force" ] && [ "$current_tag" = "$target_tag" ] && [ -f "$PLUGIN_DIR/$PLUGIN_FILE" ]; then
  log "already on $target_tag"
  exit 0
fi

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

new_sha="$(sha256sum "$tmp_file" | awk '{print $1}')"
old_sha=""
if [ -f "$PLUGIN_DIR/$PLUGIN_FILE" ]; then
  old_sha="$(sha256sum "$PLUGIN_DIR/$PLUGIN_FILE" | awk '{print $1}')"
fi

if [ "$FORCE" != "--force" ] && [ -n "$old_sha" ] && [ "$old_sha" = "$new_sha" ]; then
  printf '%s' "$target_tag" > "$state_file"
  log "binary unchanged: $target_tag $new_sha"
  exit 0
fi

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
log "restart CPA manually to load the new plugin"
