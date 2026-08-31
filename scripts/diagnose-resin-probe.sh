#!/usr/bin/env bash

# Collect a reproducible Resin probe failure without printing credentials.
#
# The script talks to an already running Resin instance.  It deliberately
# clears conventional proxy variables in the curl subprocesses so that the
# control-plane requests themselves do not accidentally use a proxy.  Resin's
# own network path is controlled by the environment with which Resin was
# started (see RESIN_NODE_DNS_UPSTREAMS below).
#
# Required commands: curl, jq.
#
# Example:
#   RESIN_BASE_URL=http://127.0.0.1:2260 \
#   RESIN_NODE_HASH=<hash> \
#   RESIN_LOG_FILE=/var/log/resin/resin.log \
#   ./scripts/diagnose-resin-probe.sh

set -Eeuo pipefail

base_url="${RESIN_BASE_URL:-http://127.0.0.1:${RESIN_PORT:-2260}}"
base_url="${base_url%/}"
node_hash="${RESIN_NODE_HASH:-}"
log_file="${RESIN_LOG_FILE:-}"
docker_container="${RESIN_DOCKER_CONTAINER:-}"
wait_seconds="${RESIN_PROBE_WAIT_SECONDS:-8}"
artifact_dir="${RESIN_DIAGNOSTIC_DIR:-}"

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v jq >/dev/null 2>&1 || die "jq is required"

if [[ -z "$artifact_dir" ]]; then
  artifact_dir="$(mktemp -d "${TMPDIR:-/tmp}/resin-probe.XXXXXX")"
  remove_artifacts=1
else
  mkdir -p -- "$artifact_dir"
  remove_artifacts=0
fi

cleanup() {
  if [[ "$remove_artifacts" -eq 1 && "${RESIN_KEEP_DIAGNOSTIC:-0}" != "1" ]]; then
    rm -rf -- "$artifact_dir"
  else
    printf 'diagnostic artifacts: %s\n' "$artifact_dir" >&2
  fi
}
trap cleanup EXIT

# Never echo token values.  Authorization is inherited from the environment
# only when the caller has explicitly supplied RESIN_ADMIN_TOKEN.
curl_args=(--silent --show-error --fail --connect-timeout 5 --max-time 20)
if [[ "${RESIN_ADMIN_TOKEN+x}" == x ]]; then
  curl_args+=(--header "Authorization: Bearer ${RESIN_ADMIN_TOKEN}")
fi

request() {
  # Run curl with proxy variables removed.  This does not alter the parent
  # shell and prevents HTTP_PROXY/ALL_PROXY from affecting the API calls.
  env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY \
    -u http_proxy -u https_proxy -u all_proxy \
    curl "${curl_args[@]}" "$@"
}

printf '%s\n' '=== Resin probe diagnostic ==='
printf 'base_url=%s\n' "$base_url"
for var in HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy; do
  if [[ -n "${!var:-}" ]]; then
    printf '%s=set\n' "$var"
  else
    printf '%s=unset\n' "$var"
  fi
done

request "$base_url/api/v1/system/info" >"$artifact_dir/system-info.json" \
  || die "cannot read /api/v1/system/info"

printf '%s\n' '--- system info (sensitive fields omitted) ---'
jq 'with_entries(select(.key | test("token|secret|password|url"; "i") | not))' \
  "$artifact_dir/system-info.json" 2>/dev/null \
  || jq '.' "$artifact_dir/system-info.json"

request "$base_url/api/v1/nodes" >"$artifact_dir/nodes.json" \
  || die "cannot read /api/v1/nodes"

if [[ -z "$node_hash" ]]; then
  # The API shape has changed between early builds; accept either a top-level
  # array or a nested data/items/nodes array and choose the first node.
  node_hash="$(jq -r '
    [.. | objects | (.node_hash? // .hash?)
      | select(type == "string" and length > 0)]
    | .[0] // empty
  ' "$artifact_dir/nodes.json")"
fi
[[ -n "$node_hash" ]] || die "set RESIN_NODE_HASH to a node hash"

printf 'node_hash=%s\n' "$node_hash"
request "$base_url/api/v1/nodes/$node_hash" >"$artifact_dir/node-before.json" \
  || die "cannot read node $node_hash"

printf '%s\n' '--- node state before probe ---'
jq '{node_hash:(.node_hash // .hash),display_tag,enabled,has_outbound,
    failure_count,circuit_open_since,last_latency_probe_attempt,
    last_authority_latency_probe_attempt,last_egress_update_attempt}' \
  "$artifact_dir/node-before.json"

printf '%s\n' '--- triggering latency probe ---'
probe_curl_args=(--silent --show-error --connect-timeout 5 --max-time 30)
if [[ "${RESIN_ADMIN_TOKEN+x}" == x ]]; then
  probe_curl_args+=(--header "Authorization: Bearer ${RESIN_ADMIN_TOKEN}")
fi
env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY \
  -u http_proxy -u https_proxy -u all_proxy \
  curl "${probe_curl_args[@]}" -X POST \
  "$base_url/api/v1/nodes/$node_hash/actions/probe-latency" \
  >"$artifact_dir/probe-response.json" \
  || true
cat "$artifact_dir/probe-response.json"
printf '\n'

sleep "$wait_seconds"

request "$base_url/api/v1/nodes/$node_hash" >"$artifact_dir/node-after.json" \
  || die "cannot read node state after probe"

printf '%s\n' '--- node state after probe ---'
jq '{node_hash:(.node_hash // .hash),display_tag,enabled,has_outbound,
    failure_count,circuit_open_since,last_latency_probe_attempt,
    last_authority_latency_probe_attempt,last_egress_update_attempt}' \
  "$artifact_dir/node-after.json"

if [[ -n "$log_file" ]]; then
  [[ -r "$log_file" ]] || die "log file is not readable: $log_file"
  printf '%s\n' '--- probe log lines ---'
  grep -E '\[probe\]|anytls|DNS|outbound' "$log_file" | tail -200 || true
elif [[ -n "$docker_container" ]]; then
  command -v docker >/dev/null 2>&1 || die "docker is required for RESIN_DOCKER_CONTAINER"
  printf '%s\n' '--- probe container log lines ---'
  env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY \
    -u http_proxy -u https_proxy -u all_proxy \
    docker logs --since "${RESIN_DOCKER_LOG_SINCE:-10m}" "$docker_container" 2>&1 \
    | grep -E '\[probe\]|anytls|DNS|outbound' | tail -200 || true
else
  printf '%s\n' '--- logs ---'
  printf '%s\n' 'Set RESIN_LOG_FILE or RESIN_DOCKER_CONTAINER to include the underlying probe error.'
fi

printf '%s\n' '--- saved JSON files ---'
printf '%s\n' "$artifact_dir/system-info.json" "$artifact_dir/nodes.json" \
  "$artifact_dir/node-before.json" "$artifact_dir/node-after.json" \
  "$artifact_dir/probe-response.json"
