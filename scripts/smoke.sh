#!/usr/bin/env bash
# Real-process smoke test: two built .exe files + a local HTTP origin + curl.
# Verifies SOCKS5, HTTP CONNECT and absolute-form HTTP through the tunnel.
set -euo pipefail
cd "$(dirname "$0")/.."

KEY="${KEY:-00112233445566778899aabbccddeeff}"
SERVER_PORT="${SERVER_PORT:-14462}"
CLIENT_PORT="${CLIENT_PORT:-11080}"
ORIGIN_PORT="${ORIGIN_PORT:-18080}"
SERVER_EXE="${SERVER_EXE:-dist/kcptun-server-windows-amd64.exe}"
CLIENT_EXE="${CLIENT_EXE:-dist/kcptun-client-windows-amd64.exe}"

for f in "$SERVER_EXE" "$CLIENT_EXE"; do
  [ -f "$f" ] || { echo "missing $f (run scripts/build.sh first)" >&2; exit 1; }
done

NODE="${NODE:-node}"
command -v curl >/dev/null || { echo "curl not found" >&2; exit 1; }

ORIGIN_PID="" SERVER_PID="" CLIENT_PID=""
cleanup() {
  for pid in "$CLIENT_PID" "$SERVER_PID" "$ORIGIN_PID"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT

ORIGIN_PORT="$ORIGIN_PORT" "$NODE" -e '
const http = require("http");
const port = Number(process.env.ORIGIN_PORT);
http.createServer((req, res) => {
  res.writeHead(200, {"content-type": "text/plain", "x-seen-path": req.url});
  res.end("SMOKE-OK:" + req.url);
}).listen(port, "127.0.0.1");
' &
ORIGIN_PID=$!

"$SERVER_EXE" --key="$KEY" -b="127.0.0.1:$SERVER_PORT" --log-level=warn &
SERVER_PID=$!
"$CLIENT_EXE" --key="$KEY" --peer="127.0.0.1:$SERVER_PORT" -b="127.0.0.1:$CLIENT_PORT" --log-level=warn &
CLIENT_PID=$!
sleep 1

echo "== SOCKS5 =="
out=$(curl -fsS --max-time 15 -x "socks5h://127.0.0.1:$CLIENT_PORT" "http://127.0.0.1:$ORIGIN_PORT/socks5")
[ "$out" = "SMOKE-OK:/socks5" ] || { echo "unexpected: $out" >&2; exit 1; }
echo "$out"

echo "== absolute-form HTTP =="
out=$(curl -fsS --max-time 15 -x "http://127.0.0.1:$CLIENT_PORT" "http://127.0.0.1:$ORIGIN_PORT/absolute")
[ "$out" = "SMOKE-OK:/absolute" ] || { echo "unexpected: $out" >&2; exit 1; }
echo "$out"

echo "== HTTP CONNECT =="
out=$(curl -fsS --max-time 15 --proxytunnel -x "http://127.0.0.1:$CLIENT_PORT" "http://127.0.0.1:$ORIGIN_PORT/connect")
[ "$out" = "SMOKE-OK:/connect" ] || { echo "unexpected: $out" >&2; exit 1; }
echo "$out"

echo "SMOKE PASSED (SOCKS5 + CONNECT + absolute-form HTTP)"
