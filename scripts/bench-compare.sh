#!/usr/bin/env bash
# Same-payload, same-loopback comparison against the sibling FCP Node port.
# Optional: set FCP_NODE=/path/to/fcp-node/src/app/cli.ts (default: ../fcp-node).
# If that file is absent the script still reports direct + kcptun-proxy.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

KEY="${KEY:-00112233445566778899aabbccddeeff}"
K_SERVER_PORT="${K_SERVER_PORT:-14462}"
K_CLIENT_PORT="${K_CLIENT_PORT:-11080}"
F_SERVER_PORT="${F_SERVER_PORT:-14460}"
F_CLIENT_PORT="${F_CLIENT_PORT:-11081}"
ORIGIN_PORT="${ORIGIN_PORT:-18080}"
SIZE_MB="${SIZE_MB:-32}"
K_SERVER_EXE="${K_SERVER_EXE:-dist/kcptun-server-windows-amd64.exe}"
K_CLIENT_EXE="${K_CLIENT_EXE:-dist/kcptun-client-windows-amd64.exe}"
FCP_NODE="${FCP_NODE:-$(cd "$ROOT/.." && pwd)/fcp-node/src/app/cli.ts}"
NODE="${NODE:-node}"

for f in "$K_SERVER_EXE" "$K_CLIENT_EXE"; do
  [ -f "$f" ] || { echo "missing $f (run scripts/build.sh first)" >&2; exit 1; }
done

PIDS=()
cleanup() {
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
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
PIDS+=("$!")

# kcptun-proxy: fixed 1400B datagram budget, FEC 10:3.
"$K_SERVER_EXE" --key="$KEY" -b="127.0.0.1:$K_SERVER_PORT" --mtu=1400 --fec=10:3 --log-level=off >/dev/null &
PIDS+=("$!")
"$K_CLIENT_EXE" --key="$KEY" --peer="127.0.0.1:$K_SERVER_PORT" -b="127.0.0.1:$K_CLIENT_PORT" --mtu=1400 --fec=10:3 --log-level=off >/dev/null &
PIDS+=("$!")

HAVE_FCP=0
if [ -f "$FCP_NODE" ]; then
  cd "$(dirname "$FCP_NODE")/../.."
  # FCP's --mtu-min is the same on-wire datagram budget; pin probing off so
  # both tunnels use exactly 1400B and FEC 10:3.
  "$NODE" "$FCP_NODE" server --key="$KEY" -b="127.0.0.1:$F_SERVER_PORT" \
    --mtu-min=1400 --mtu-max=1400 --no-pmtud --fec=10:3 --log-level=off >/dev/null &
  PIDS+=("$!")
  "$NODE" "$FCP_NODE" client --key="$KEY" --peer="127.0.0.1:$F_SERVER_PORT" -b="127.0.0.1:$F_CLIENT_PORT" \
    --mtu-min=1400 --mtu-max=1400 --no-pmtud --fec=10:3 --log-level=off >/dev/null &
  PIDS+=("$!")
  HAVE_FCP=1
  cd "$ROOT"
fi

sleep 1
URL="http://127.0.0.1:$ORIGIN_PORT/data"
echo "payload: ${SIZE_MB} MiB, loopback, MTU 1400B, FEC 10:3"
printf "%-22s %s\n" "path" "download"
printf "%-22s " "direct"
curl -sS --max-time 180 -o /dev/null -w "%{speed_download} B/s  (%{time_total}s)\n" "$URL"
printf "%-22s " "kcptun-proxy"
curl -sS --max-time 180 -x "socks5h://127.0.0.1:$K_CLIENT_PORT" -o /dev/null -w "%{speed_download} B/s  (%{time_total}s)\n" "$URL"
if [ "$HAVE_FCP" = "1" ]; then
  printf "%-22s " "fcp-node"
  curl -sS --max-time 180 -x "socks5h://127.0.0.1:$F_CLIENT_PORT" -o /dev/null -w "%{speed_download} B/s  (%{time_total}s)\n" "$URL"
else
  echo "fcp-node: skipped ($FCP_NODE not found)"
fi
