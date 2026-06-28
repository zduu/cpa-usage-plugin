#!/usr/bin/env sh
set -eu

tag="${1:-}"
changelog="${2:-CHANGELOG.md}"
output="${3:-release-notes.md}"

if [ -z "$tag" ]; then
  printf 'usage: %s <tag> [changelog] [output]\n' "$0" >&2
  exit 2
fi

if [ ! -f "$changelog" ]; then
  printf 'changelog not found: %s\n' "$changelog" >&2
  exit 1
fi

version="${tag#v}"
tmp="${output}.tmp"
mkdir -p "$(dirname "$output")"

awk -v tag="$tag" -v version="$version" '
function section_version(line, value) {
  value = line
  sub(/^##[[:space:]]+/, "", value)
  sub(/[[:space:]]+-[[:space:]].*$/, "", value)
  sub(/^[[]/, "", value)
  sub(/[]]$/, "", value)
  return value
}

/^##[[:space:]]+/ {
  value = section_version($0)
  if (found) {
    exit
  }
  if (value == tag || value == version || value == "v" version) {
    found = 1
    print $0
  }
  next
}

found {
  print $0
}

END {
  if (!found) {
    exit 3
  }
}
' "$changelog" > "$tmp"

if ! grep -Eq '[^[:space:]]' "$tmp"; then
  printf 'release notes section for %s is empty\n' "$tag" >&2
  rm -f "$tmp"
  exit 1
fi

if grep -Eiq 'TODO|TBD|待补充' "$tmp"; then
  printf 'release notes section for %s contains placeholder text\n' "$tag" >&2
  rm -f "$tmp"
  exit 1
fi

mv "$tmp" "$output"
printf 'release notes for %s written to %s\n' "$tag" "$output"
