#!/usr/bin/env bash
# Gate: vet + fresh tests + build + CLI smoke. Run before committing.
set -euo pipefail
cd "$(dirname "$0")/.."

GO="${GO:-go}"
echo "== gofmt =="
unformatted=$("$GO" fmt ./...)
if [ -n "$unformatted" ]; then
  echo "gofmt rewrote files: $unformatted" >&2
  exit 1
fi

echo "== go vet =="
"$GO" vet ./...

echo "== go test (no cache) =="
"$GO" test -count=1 ./...

echo "== build =="
bash scripts/build.sh >/dev/null

echo "== CLI smoke =="
SERVER=dist/kcptun-server-windows-amd64.exe
CLIENT=dist/kcptun-client-windows-amd64.exe
"$SERVER" -h | head -1 | grep -q "kcptun-proxy"
"$CLIENT" -h | head -1 | grep -q "kcptun-proxy"

# Retired knobs must fail loudly, not be ignored.
if "$CLIENT" --mode=fast3 --key=00000000000000000000000000000000 --peer=127.0.0.1:4462 >/dev/null 2>&1; then
  echo "--mode=fast3 was accepted (must be rejected)" >&2
  exit 1
fi
if "$CLIENT" --key=00000000000000000000000000000000 --peer=127.0.0.1:4462 --mtu-min=1040 >/dev/null 2>&1; then
  echo "--mtu-min was accepted (must be rejected)" >&2
  exit 1
fi

echo "== loopback speedtest =="
"$CLIENT" --speedtest --seconds=3 | tail -1 | grep -q "up="

echo "ALL GATES PASSED"
