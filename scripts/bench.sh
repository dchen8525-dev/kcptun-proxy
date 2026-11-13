#!/usr/bin/env bash
# Reproducible throughput comparison: direct HTTP vs the same download through
# kcptun-proxy's SOCKS5 entry. Everything is loopback, no VPS involved.
set -euo pipefail
cd "$(dirname "$0")/.."

KEY="${KEY:-00112233445566778899aabbccddeeff}"
SERVER_PORT="${SERVER_PORT:-14462}"
CLIENT_PORT="${CLIENT_PORT:-11080}"
ORIGIN_PORT="${ORIGIN_PORT:-18080}"
SIZE_MB="${SIZE_MB:-32}"
SERVER_EXE="${SERVER_EXE:-dist/kcptun-server-windows-amd64.exe}"
CLIENT_EXE="${CLIENT_EXE:-dist/kcptun-client-windows-amd64.exe}"
NODE="${NODE:-node}"

for f in "$SERVER_EXE" "$CLIENT_EXE"; do
  [ -f "$f" ] || { echo "missing $f (run scripts/build.sh first)" >&2; exit 1; }
done

ORIGIN_PID="" SERVER_PID="" CLIENT_PID=""
cleanup() {
  for pid in "$CLIENT_PID" "$SERVER_PID" "$ORIGIN_PID"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT

ORIGIN_PORT="$ORIGIN_PORT" SIZE_MB="$SIZE_MB" "$NODE" -e '
const http = require("http");
const size = Number(process.env.SIZE_MB) * 1024 * 1024;
const buf = Buffer.alloc(size, 0x5a);
http.createServer((req, res) => {
  res.writeHead(200, {"content-type": "application/octet-stream", "content-length": size});
  res.end(buf);
}).listen(Number(process.env.ORIGIN_PORT), "127.0.0.1");
' &
ORIGIN_PID=$!

"$SERVER_EXE" --key="$KEY" -b="127.0.0.1:$SERVER_PORT" --log-level=off >/dev/null &
SERVER_PID=$!
"$CLIENT_EXE" --key="$KEY" --peer="127.0.0.1:$SERVER_PORT" -b="127.0.0.1:$CLIENT_PORT" --log-level=off >/dev/null &
CLIENT_PID=$!
sleep 1

URL="http://127.0.0.1:$ORIGIN_PORT/data"
echo "payload: ${SIZE_MB} MiB (loopback)"
echo -n "direct          : "
curl -sS --max-time 120 -o /dev/null -w "%{speed_download} B/s  (%{time_total}s)\n" "$URL"
echo -n "kcptun-proxy    : "
curl -sS --max-time 120 -x "socks5h://127.0.0.1:$CLIENT_PORT" -o /dev/null -w "%{speed_download} B/s  (%{time_total}s)\n" "$URL"
echo -n "kcptun-proxy x2 : "
curl -sS --max-time 120 -x "socks5h://127.0.0.1:$CLIENT_PORT" -o /dev/null -w "%{speed_download} B/s  (%{time_total}s)\n" "$URL"
