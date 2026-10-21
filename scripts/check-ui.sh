#!/usr/bin/env bash
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
export GOPATH="$PWD/.gomodcache/gopath" GOMODCACHE="$PWD/.gomodcache/gopath/pkg/mod" GOFLAGS=-mod=mod GOCACHE="$PWD/.gomodcache/build" TMPDIR="$PWD/.gomodcache/tmp"
mkdir -p .e2e/data
go build -o .e2e/nrpd ./cmd/nrpd || exit 1
./.e2e/nrpd -data-dir .e2e/data -control-addr 127.0.0.1:17700 -web-addr 127.0.0.1:17710 -public-host 127.0.0.1 -public-bind-addr 127.0.0.1 > .e2e/ui.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null' EXIT
for i in $(seq 1 100); do curl -sf http://127.0.0.1:17710/api/v1/health >/dev/null 2>&1 && break; sleep 0.1; done
echo "--- health"; curl -s http://127.0.0.1:17710/api/v1/health; echo
echo "--- index"; curl -s -o /dev/null -w '%{http_code} %{content_type} %{size_download}\n' http://127.0.0.1:17710/
echo "--- app.js"; curl -s -o /dev/null -w '%{http_code} %{content_type} %{size_download}\n' http://127.0.0.1:17710/app.js
echo "--- style.css"; curl -s -o /dev/null -w '%{http_code} %{content_type} %{size_download}\n' http://127.0.0.1:17710/style.css
echo "--- spa fallback"; curl -s -o /dev/null -w '%{http_code} %{content_type}\n' http://127.0.0.1:17710/devices
echo "--- headers"; curl -sI http://127.0.0.1:17710/ | grep -iE 'content-security|cache-control|x-frame'
echo "--- unauth api"; curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:17710/api/v1/devices
echo "--- log tail"; tail -3 .e2e/ui.log
