#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu

image="${1:-yunpin-sync:smoke-test}"
if [ "$#" -eq 0 ]; then
  docker build -t "$image" "$(dirname "$0")/.."
fi

container_id="$(docker run -d -p 127.0.0.1:0:8080 "$image")"
cleanup() {
  docker rm -f "$container_id" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

attempt=0
while [ "$attempt" -lt 20 ]; do
  if [ "$(docker inspect --format '{{.State.Running}}' "$container_id")" = "true" ]; then
    host_port="$(docker port "$container_id" 8080/tcp | awk -F: 'NR == 1 {print $NF}')"
    if curl --fail --silent "http://127.0.0.1:${host_port}/healthz" | grep -q '"status":"ok"'; then
      exit 0
    fi
  fi
  attempt=$((attempt + 1))
  sleep 0.25
done

docker logs "$container_id" >&2
exit 1
