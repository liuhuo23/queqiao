#!/usr/bin/env bash
# 端到端冒烟测试：起服务端 -> Agent 待审批 -> 网页 API 批准 -> 建映射 -> 公网端口访问本地 HTTP。
# 只用 127.0.0.1，不依赖外网与 root。
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK="${E2E_WORK:-$ROOT/.e2e}"
rm -rf "$WORK"
mkdir -p "$WORK/www" "$WORK/agent" "$WORK/data"

export GOPATH="$ROOT/.gomodcache/gopath"
export GOMODCACHE="$ROOT/.gomodcache/gopath/pkg/mod"
export GOCACHE="$ROOT/.gomodcache/build"
export GOFLAGS=-mod=mod

CONTROL="${E2E_CONTROL:-127.0.0.1:17600}"
WEB="${E2E_WEB:-127.0.0.1:17610}"
LOCAL_HTTP="${E2E_LOCAL_HTTP:-127.0.0.1:18080}"
REMOTE_PORT="${E2E_REMOTE_PORT:-39125}"

PIDS=""
cleanup() {
  for pid in $PIDS; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT

fail() { echo "FAIL: $*"; echo "--- nrpd.log ---"; tail -50 "$WORK/nrpd.log" 2>/dev/null; echo "--- agent.log ---"; tail -50 "$WORK/agent.log" 2>/dev/null; exit 1; }

BUILD_EXTRA=""
if [ "${NRP_RACE:-}" = "1" ]; then
  BUILD_EXTRA="-race"
  echo "==> 构建（race detector 已开启）"
fi

echo "==> 构建"
(cd "$ROOT" && go build $BUILD_EXTRA -o "$WORK/nrpd" ./cmd/nrpd && go build $BUILD_EXTRA -o "$WORK/nrp-agent" ./cmd/nrp-agent) || fail "构建失败"

cat > "$WORK/nrpd.json" <<JSON
{
  "control_addr": "$CONTROL",
  "web_addr": "$WEB",
  "data_dir": "$WORK/data",
  "public_host": "127.0.0.1",
  "public_bind_addr": "127.0.0.1",
  "port_range": [39120, 39130],
  "log_level": "debug",
  "log_format": "text"
}
JSON

echo "==> 启动服务端"
"$WORK/nrpd" -config "$WORK/nrpd.json" > "$WORK/nrpd.log" 2>&1 &
PIDS="$PIDS $!"

for _ in $(seq 1 100); do
  curl -sf "http://$WEB/api/v1/health" > /dev/null 2>&1 && break
  sleep 0.1
done
curl -sf "http://$WEB/api/v1/health" > /dev/null || fail "服务端未启动"

echo "==> 启动 Agent（首次连接应处于待审批）"
"$WORK/nrp-agent" -s "$CONTROL" -state-dir "$WORK/agent" -log-level debug > "$WORK/agent.log" 2>&1 &
PIDS="$PIDS $!"

PW=$(sed -n 's/^密码:   //p' "$WORK/data/initial-admin.txt")
[ -n "$PW" ] || fail "未找到初始管理员密码文件"

echo "==> 登录管理台"
LOGIN=$(curl -sf -X POST "http://$WEB/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$PW\"}" \
  -c "$WORK/cookies.txt") || fail "登录失败"
CSRF=$(echo "$LOGIN" | python3 -c 'import sys,json;print(json.load(sys.stdin)["csrf_token"])') || fail "解析登录响应失败"
[ -n "$CSRF" ] || fail "空 CSRF"

echo "==> 安全负例：未登录 / CSRF / 未知 API"
CODE=$(curl -s -o /dev/null -w '%{http_code}' "http://$WEB/api/v1/devices")
[ "$CODE" = "401" ] || fail "未登录访问设备列表应 401, got $CODE"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -b "$WORK/cookies.txt" -X POST "http://$WEB/api/v1/devices/x/approve" -H 'Content-Type: application/json' -d '{}')
[ "$CODE" = "403" ] || fail "缺少 CSRF 头的写请求应 403, got $CODE"
CODE=$(curl -s -o /dev/null -w '%{http_code}' "http://$WEB/api/v1/no-such-endpoint")
[ "$CODE" = "404" ] || fail "未知 API 应 404, got $CODE"

echo "==> 等待设备出现并批准"
DEV_ID=""
for _ in $(seq 1 100); do
  DEV_ID=$(curl -s -b "$WORK/cookies.txt" "http://$WEB/api/v1/devices" | python3 -c 'import sys,json;ds=json.load(sys.stdin)["devices"];print(ds[0]["id"] if ds else "")')
  [ -n "$DEV_ID" ] && break
  sleep 0.2
done
[ -n "$DEV_ID" ] || fail "设备未注册"

curl -sf -b "$WORK/cookies.txt" -X POST "http://$WEB/api/v1/devices/$DEV_ID/approve" \
  -H "X-NRP-CSRF: $CSRF" -H 'Content-Type: application/json' -d '{"name":"e2e-host"}' \
  > "$WORK/approve.json" || fail "批准设备失败"
python3 -c 'import sys,json;d=json.load(open(sys.argv[1]));assert d["status"]=="active",d' "$WORK/approve.json" || fail "设备未变为 active"

echo "==> 等待 Agent 上线"
ONLINE=""
for _ in $(seq 1 200); do
  ONLINE=$(curl -s -b "$WORK/cookies.txt" "http://$WEB/api/v1/devices" | python3 -c 'import sys,json;d=json.load(sys.stdin)["devices"][0];print("yes" if d["online"] else "no")')
  [ "$ONLINE" = "yes" ] && break
  sleep 0.2
done
[ "$ONLINE" = "yes" ] || fail "Agent 未上线"

echo "==> 创建映射 $LOCAL_HTTP -> :$REMOTE_PORT"
RULE_JSON=$(curl -sf -b "$WORK/cookies.txt" -X POST "http://$WEB/api/v1/rules" \
  -H "X-NRP-CSRF: $CSRF" -H 'Content-Type: application/json' \
  -d "{\"device_id\":\"$DEV_ID\",\"name\":\"e2e\",\"proto\":\"tcp\",\"local_addr\":\"$LOCAL_HTTP\",\"remote_port\":$REMOTE_PORT,\"enabled\":true}") || fail "创建映射失败"
RULE_ID=$(echo "$RULE_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])') || fail "解析映射失败"

echo "hello-nrp-e2e" > "$WORK/www/hello.txt"
LOCAL_HOST="${LOCAL_HTTP%:*}"
LOCAL_PORT="${LOCAL_HTTP##*:}"
( cd "$WORK/www" && exec python3 -m http.server "$LOCAL_PORT" --bind "$LOCAL_HOST" ) > "$WORK/http.log" 2>&1 &
PIDS="$PIDS $!"

echo "==> 通过公网端口访问本地服务"
BODY=""
for _ in $(seq 1 100); do
  BODY=$(curl -s --max-time 3 "http://127.0.0.1:$REMOTE_PORT/hello.txt" || true)
  [ "$BODY" = "hello-nrp-e2e" ] && break
  sleep 0.2
done
[ "$BODY" = "hello-nrp-e2e" ] || fail "隧道请求失败，响应: '$BODY'"

echo "==> 校验配置下发：禁用映射后公网端口应关闭"
curl -sf -b "$WORK/cookies.txt" -X PATCH "http://$WEB/api/v1/rules/$RULE_ID" \
  -H "X-NRP-CSRF: $CSRF" -H 'Content-Type: application/json' -d '{"enabled":false}' > /dev/null || fail "禁用映射失败"
sleep 0.5
if curl -s --max-time 2 "http://127.0.0.1:$REMOTE_PORT/hello.txt" > /dev/null 2>&1; then
  fail "禁用后端口仍然可访问"
fi

echo "==> 校验封禁设备"
curl -sf -b "$WORK/cookies.txt" -X POST "http://$WEB/api/v1/devices/$DEV_ID/block" \
  -H "X-NRP-CSRF: $CSRF" -H 'Content-Type: application/json' -d '{"blocked":true}' > /dev/null || fail "封禁失败"
sleep 0.5
for _ in $(seq 1 50); do
  ONLINE=$(curl -s -b "$WORK/cookies.txt" "http://$WEB/api/v1/devices" | python3 -c 'import sys,json;d=json.load(sys.stdin)["devices"][0];print("yes" if d["online"] else "no")')
  [ "$ONLINE" = "no" ] && break
  sleep 0.2
done
[ "$ONLINE" = "no" ] || fail "封禁后设备仍在线"

echo "==> 校验审计日志"
AUDIT=$(curl -sf -b "$WORK/cookies.txt" "http://$WEB/api/v1/logs?kind=audit&limit=20")
echo "$AUDIT" | grep -q "device.approve" || fail "审计日志缺少 device.approve"

echo
echo "PASS: 端到端冒烟测试全部通过"
echo "  设备: $DEV_ID"
echo "  映射: $RULE_ID -> 127.0.0.1:$REMOTE_PORT"
