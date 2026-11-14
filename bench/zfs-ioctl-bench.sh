#!/usr/bin/env bash
#
# Ground truth for cmd/zdap-bench: count the /dev/zfs ioctls each zdapd REST
# endpoint really causes, by running zdapd under strace.
#
# cmd/zdap-bench counts libzfs calls inside zdapd and derives an ioctl estimate
# from a calibration constant. This script measures the ioctls instead, so the
# estimate can be checked, and re-calibrated if the ZFS or go-libzfs version
# changes.
#
# zdapd is started by this script rather than attached to, because
# /proc/sys/kernel/yama/ptrace_scope is usually 1, which only allows tracing
# your own descendants.
#
# Usage:
#   bench/zfs-ioctl-bench.sh [-p zpool] [-c config-dir] [-P port] [-n requests]
#
set -euo pipefail

ZPOOL=${ZPOOL:-zdap/1/databases}
CONFIG_DIR=${CONFIG_DIR:-/zdap/1/config}
PORT=${API_PORT:-43219}
N=5
AUTH=zdap-bench

while getopts "p:c:P:n:h" opt; do
  case $opt in
    p) ZPOOL=$OPTARG ;;
    c) CONFIG_DIR=$OPTARG ;;
    P) PORT=$OPTARG ;;
    n) N=$OPTARG ;;
    h) sed -n '2,17p' "$0"; exit 0 ;;
    *) exit 2 ;;
  esac
done

command -v strace >/dev/null || { echo "strace is not installed" >&2; exit 1; }

cd "$(dirname "$0")/.."
WORK=$(mktemp -d -t zdap-ioctl-bench.XXXXXX)
TRACE=$WORK/ioctl.log
SERVER_PID=

# cleanup kills the traced zdapd first and strace second. Signalling strace
# alone is not enough: it keeps running for as long as the process it traces is
# alive, so a plain kill followed by wait hangs forever.
cleanup() {
  pkill -TERM -f "$WORK/zdapd serve" 2>/dev/null || true
  if [ -n "$SERVER_PID" ]; then
    for _ in $(seq 1 20); do
      kill -0 "$SERVER_PID" 2>/dev/null || break
      sleep 0.25
    done
    kill -KILL "$SERVER_PID" 2>/dev/null || true
  fi
  pkill -KILL -f "$WORK/zdapd serve" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "building zdapd"
go build -o "$WORK/zdapd" ./cmd/zdapd

echo "starting zdapd on :$PORT under strace (pool $ZPOOL)"
NETWORK_ADDRESS=127.0.0.1 API_PORT=$PORT ZPOOL=$ZPOOL CONFIG_DIR=$CONFIG_DIR \
  strace -f -e trace=ioctl -o "$TRACE" "$WORK/zdapd" serve >"$WORK/zdapd.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 60); do
  curl -sf -m 5 -H "auth: $AUTH" "http://127.0.0.1:$PORT/resources" -o /dev/null && break
  sleep 0.5
done
curl -sf -m 30 -H "auth: $AUTH" "http://127.0.0.1:$PORT/resources" -o "$WORK/resources.json" || {
  echo "zdapd did not come up; see below" >&2
  cat "$WORK/zdapd.log" >&2
  exit 1
}

read -r RESOURCE SNAP < <(python3 - "$WORK/resources.json" <<'PY'
import json, sys
rs = json.load(open(sys.argv[1]))
pick = next((r for r in rs if r.get("snaps")), rs[0] if rs else None)
if pick is None:
    sys.exit("zdapd reports no resources")
snaps = pick.get("snaps") or []
print(pick["name"], snaps[-1]["created_at"] if snaps else "-")
PY
)
[ "$SNAP" = "-" ] && SNAP=
echo "resource $RESOURCE ${SNAP:+snapshot $SNAP}"
echo

# ioctls returns the number of ioctl lines strace has written so far. The short
# sleep lets strace flush before and after the window being measured.
ioctls() {
  sleep 0.4
  grep -c 'ioctl(' "$TRACE" || true
}

# Measure the drift while nothing is being requested, so background work (cron,
# clone pool GC) does not silently inflate the per endpoint numbers.
IDLE_A=$(ioctls); sleep 1; IDLE_B=$(ioctls)
IDLE=$((IDLE_B - IDLE_A))
if [ "$IDLE" -gt 0 ]; then
  echo "note: $IDLE ioctls in one idle second, background work is running and the"
  echo "      numbers below include whatever of it lands in the measured window"
  echo
fi

bench_endpoint() {
  local name=$1 method=$2 path=$3
  # Warm up once, unmeasured.
  curl -s -m 300 -X "$method" -H "auth: $AUTH" "http://127.0.0.1:$PORT$path" -o /dev/null || true

  local before after total
  before=$(ioctls)
  for _ in $(seq 1 "$N"); do
    curl -s -m 300 -X "$method" -H "auth: $AUTH" "http://127.0.0.1:$PORT$path" -o /dev/null || true
  done
  after=$(ioctls)
  total=$((after - before))
  printf '%-46s %10d %12s\n' "$name" "$total" "$(python3 -c "print(f'{$total/$N:.1f}')")"
}

R=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))" "$RESOURCE")

printf '%-46s %10s %12s\n' ENDPOINT "IOCTLS($N)" "PER REQUEST"
bench_endpoint "GET  /status"                     GET "/status"
bench_endpoint "GET  /resources"                  GET "/resources"
bench_endpoint "GET  /resources/:resource"        GET "/resources/$R"
bench_endpoint "GET  /resources/:resource/snaps"  GET "/resources/$R/snaps"
bench_endpoint "GET  /resources/:resource/clones" GET "/resources/$R/clones"
if [ -n "$SNAP" ]; then
  bench_endpoint "GET  /resources/:resource/snaps/:createdAt" GET "/resources/$R/snaps/$SNAP"
fi

echo
echo "ioctl request types over the whole run:"
grep -o 'ioctl([0-9]*, [A-Z_0-9x]*' "$TRACE" | awk '{print "  " $2}' | sort | uniq -c | sort -rn
