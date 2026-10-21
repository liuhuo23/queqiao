# NRP 管理 API v1

Base URL：http://127.0.0.1:7610（默认仅本机；生产由 nginx/SSH 隧道暴露）

- 除 /api/v1/auth/login 与 /api/v1/health 外，所有请求需要登录 Cookie。
- 所有非 GET 请求必须带 CSRF 头：X-NRP-CSRF: <me.csrf_token>。
- 请求/响应均为 application/json; charset=utf-8。
- 成功：HTTP 200 + 业务对象；失败：HTTP 4xx/5xx + 错误对象。

```json
{"error":{"code":"device_not_found","message":"设备不存在"}}
```

通用错误码：unauthorized(401) / forbidden(403) / csrf_invalid(403) / invalid_request(400)
/ not_found(404) / conflict(409) / rate_limited(429) / internal(500)

---

## 1. 认证

### POST /api/v1/auth/login

```json
请求 {"username":"admin","password":"..."}
响应 {"admin":{"id":"adm_...","username":"admin"},"csrf_token":"<32B hex>"}
     Set-Cookie: nrp_admin=<token>; Path=/; HttpOnly; SameSite=Lax; Max-Age=43200
```

失败：401 invalid_credentials；429 too_many_attempts。

### POST /api/v1/auth/logout
删除会话，清 Cookie。响应 {"ok":true}

### GET /api/v1/me
```json
{"admin":{"id":"adm_...","username":"admin"},"csrf_token":"<32B hex>","must_change_password":false}
```

### POST /api/v1/auth/password
```json
请求 {"old_password":"...","new_password":"..."}   // 新密码 >= 12 字符
响应 {"ok":true}   // 修改后其它会话全部失效
```

---

## 2. 概览

### GET /api/v1/overview
```json
{
  "server": {
    "version": "0.1.0",
    "uptime_sec": 3620,
    "control_addr": ":7600",
    "web_addr": "127.0.0.1:7610",
    "public_host": "39.107.230.175",
    "port_range": [39000, 40000],
    "http_addr": ""
  },
  "counts": {"devices_total": 3, "devices_online": 2, "devices_pending": 1, "rules_total": 5, "rules_active": 4},
  "traffic": {"bytes_in": 1234567, "bytes_out": 7654321, "conns_current": 4, "conns_total": 128}
}
```

---

## 3. 设备

Device 对象（列表项在基础字段上追加运行时字段）：

```json
{
  "id": "dev_5f3a1c9e2b74",
  "key_code": "NRP-7K3F-9Q2M-8XZP",
  "name": "macbook-pro",
  "status": "active",              // pending | active | blocked
  "machine_id": "9f86d0...64hex",
  "hostname": "macbook-pro.local",
  "os": "darwin", "arch": "arm64",
  "agent_version": "0.1.0",
  "last_ip": "203.0.113.7",       // 最近一次连接来源 IP（首次连接后即固定展示）
  "created_at": "2026-10-21T23:59:00+08:00",
  "approved_at": "2026-10-22T00:01:00+08:00",
  "last_seen_at": "2026-10-22T00:05:00+08:00",
  "note": "",
  "public_key": "...",             // 只读；管理台不需要展示
  "online": true,                  // 列表项才有
  "online_since": "2026-10-22T00:01:30+08:00",
  "last_seen_sec": 3,               // 距离最后一次控制帧的秒数（在线时）
  "rule_count": 2,
  "fingerprint_conflict": false
}
```

### GET /api/v1/devices?status=all|pending|active|blocked
```json
{"devices": [Device...]}
```

### POST /api/v1/devices/{id}/approve
```json
请求 {"name":"可选，缺省用 hostname"}
响应 Device（status=active，若在线立即推送 config）
```

### POST /api/v1/devices/{id}/reject
仅对 pending 有效，删除记录。响应 {"ok":true}

### POST /api/v1/devices/{id}/block
```json
请求 {"blocked": true}
响应 Device   // blocked=true 时立即 kick 并关闭监听；blocked=false 仅对已封禁设备有效（不能把 pending 直接解封成 active）
```

### PATCH /api/v1/devices/{id}
```json
请求 {"name":"新名字","note":"备注"}   // 均可选
响应 Device
```

### POST /api/v1/devices/{id}/rebind
清空机器指纹并置回 pending（重新登记）；**已有规则保留**，但设备重新上线前不会监听端口。响应 Device

### DELETE /api/v1/devices/{id}
删除设备及其全部规则；在线则 kick，并把公钥加入**吊销列表**（同一身份无法再登记，需重装 Agent 生成新密钥）。响应 {"ok":true}

---

## 4. 映射规则

Rule 对象：

```json
{
  "id": "rule_ab12cd34ef56",
  "device_id": "dev_5f3a1c9e2b74",
  "device_name": "macbook-pro",
  "name": "web",
  "proto": "tcp",                  // tcp | http
  "local_addr": "127.0.0.1:8080",
  "remote_port": 39001,            // tcp 必填；http 为 0
  "domain": "",                    // http 必填；tcp 为空
  "enabled": true,
  "created_at": "...", "updated_at": "...",
  "state": "active",               // active | device_offline | disabled | error
  "error": "",                     // state=error 时的原因（如端口被占用）
  "public_addr": "39.107.230.175:39001",  // tcp=host:port；http=域名（不含 scheme）
  "bytes_in": 0,                   // 公网 -> 本地 的累计字节
  "bytes_out": 0,                  // 本地 -> 公网 的累计字节
  "conns_current": 0, "conns_total": 0
}
```

### GET /api/v1/rules?device_id=
```json
{"rules": [Rule...]}
```

### POST /api/v1/rules
```json
请求 {
  "device_id":"dev_...",
  "name":"web",
  "proto":"tcp",
  "local_addr":"127.0.0.1:8080",
  "remote_port":39001,
  "domain":"",
  "enabled":true
}
响应 201 Rule
校验：设备存在；local_addr 合法（host:port，port 1-65535）；tcp 端口在 port_range 内且未占用（服务端监听预检）；
      remote_port / domain 全局唯一；每设备规则数 <= 50。
错误：409 port_in_use / domain_taken / rule_limit；400 invalid_local_addr
```

### PATCH /api/v1/rules/{id}
```json
请求 { 任意字段：name, local_addr, remote_port, domain, enabled }
响应 Rule   // 立即 reconcile：关闭旧监听、创建新监听、推送 config
```

### DELETE /api/v1/rules/{id}
响应 {"ok":true}

### GET /api/v1/rules/check-port?port=39001
```json
{"port":39001,"available":true,"reason":""}
```

---

## 5. 日志

### GET /api/v1/logs?kind=audit|agent&limit=200
```json
{"logs":[{"at":"2026-10-22T00:01:00+08:00","level":"info","actor":"admin",
          "action":"device.approve","target":"dev_...","detail":"macbook-pro","ip":"1.2.3.4"}]}
```
- audit：管理员动作（登录、审批、改规则、改设置……），来自 audit.jsonl
- agent：Agent 上送事件 + 服务端连接事件（内存环形缓冲，最多 1000 条）

---

## 6. 设置

### GET /api/v1/settings
```json
{
  "public_host": "39.107.230.175",
  "port_range": [39000, 40000],
  "http_addr": "",
  "device_auto_approve": false,
  "max_devices": 200,
  "max_rules_per_device": 50,
  "admin_session_ttl_sec": 43200,
  "traffic_stats_enabled": true    // 关闭后不再统计隧道流量（连接数仍统计）
}
```

### PUT /api/v1/settings
请求：上述字段的任意子集。校验：port_range 1024-65535 且 start<=end；public_host 可为空；http_addr 形如 `:39080` 或 `0.0.0.0:39080`；
max_devices 1-10000；max_rules_per_device 1-1000；admin_session_ttl_sec 300-604800。
响应：完整 Settings。端口段变更不影响已存在规则，仅影响新建；http_addr 变更会重建 HTTP 监听。

---

## 7. 系统

### GET /api/v1/health   （无需登录）
```json
{"ok":true,"version":"0.1.0","time":"2026-10-22T00:00:00+08:00"}
```

### GET /metrics   （无需登录，仅本机管理口；nginx 也可拦截）
Prometheus 文本格式：
```text
nrp_devices_online 2
nrp_devices_pending 1
nrp_rules_active 4
nrp_conns_current 4
nrp_bytes_in_total 1234567
nrp_bytes_out_total 7654321
nrp_auth_failures_total 3
```

---

## 8. 前端约定（给 UI 实现者的契约）

- 启动：GET /api/v1/me → 200 进入主界面；401 显示登录页。
- 所有 fetch 统一封装：非 2xx 弹出错误对象 message；401 跳登录。
- 非 GET 自动加 X-NRP-CSRF。
- 轮询：overview/devices/rules 每 4s；日志页每 8s。
- 页面：登录 / 概览 / 设备 / 映射 / 日志 / 设置，顶部导航 + 内容区，纯原生 JS（无框架、无构建）。
- 颜色：跟随 prefers-color-scheme；主色 #2563eb；危险操作红色确认。
- 状态徽标：pending=黄色、active=绿色、blocked=红色、offline=灰色。
