#!/bin/sh
set -eu

cache_dir="${RESIN_CACHE_DIR:-/var/cache/resin}"
state_dir="${RESIN_STATE_DIR:-/var/lib/resin}"
log_dir="${RESIN_LOG_DIR:-/var/log/resin}"

if [ "$#" -eq 0 ]; then
  set -- /usr/local/bin/resin
fi

require_writable_dir() {
  dir="$1"
  label="$2"

  if [ ! -d "$dir" ]; then
    mkdir -p "$dir"
  fi
  if [ ! -w "$dir" ]; then
    cat >&2 <<EOF
fatal: ${label} directory is not writable: ${dir}
hint: mount it with write permission, or use Docker named volumes.
EOF
    exit 1
  fi
}

if [ "$(id -u)" -eq 0 ]; then
  mkdir -p "$cache_dir" "$state_dir" "$log_dir"
  if [ "${RESIN_SKIP_CHOWN:-0}" != "1" ]; then
    chown -R resin:resin "$cache_dir" "$state_dir" "$log_dir"
  fi
  exec su-exec resin:resin "$@"
fi

require_writable_dir "$cache_dir" "cache"
require_writable_dir "$state_dir" "state"
require_writable_dir "$log_dir" "log"

exec "$@"
