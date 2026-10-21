# 代码结构与 Agent 任务拆分

本文是给实现者（人或 AI Agent）的施工图：包边界、依赖方向、每个任务的写入范围与验收标准。
新增功能前先读 docs/DESIGN.md 与 docs/PROTOCOL.md，改动协议要升 `protocol.Version`。

## 1. 依赖方向（禁止反向 import）

```text
cmd/nrpd ─▶ internal/server ─▶ internal/web ─▶ internal/store
                │                  │
                ├─▶ internal/protocol, auth, tlsutil, config, machineid(只读)
                └─▶ internal/store
cmd/nrp-agent ─▶ internal/agent ─▶ internal/{protocol,auth,agentstate,machineid,tlsutil}
                    └─▶ internal/agentsvc（安装系统服务）
internal/web 只通过 `Backend` 接口访问运行时，不依赖 internal/server（避免循环依赖）。
```

关键约束：

- `internal/protocol` 只依赖标准库，协议帧必须有单元测试。
- `internal/store` 不 import 任何其它 internal 包；返回副本，不暴露内部指针。
- `internal/web` 的错误统一用 `web.Err{Status, Code, Message}`；server 返回它，handler 负责转 HTTP。
- 任何对 `store` 的写操作后：`BumpConfigRevision()` → `pushConfig(deviceID)` → `tunnels.reconcileAll()`。
- 数据面统计只有服务端（公网侧）计数为权威；Agent 的 stats 仅上送，v1 不参与 UI。
- 客户端默认只允许 loopback 目标，放宽必须显式 `-allow-lan`。

## 2. 文件地图

| 文件 | 职责 |
|------|------|
| internal/protocol/protocol.go | 帧编解码、握手/控制/数据消息、错误码、流类型 |
| internal/auth/auth.go | Ed25519 生成/编解码、密钥码、签名与验签 |
| internal/passwd/passwd.go | PBKDF2-HMAC-SHA256 哈希与校验 |
| internal/machineid/*.go | 机器指纹（平台机器码优先，物理 MAC 兜底），按 OS 分文件 |
| internal/tlsutil/tlsutil.go | 自签证书生成/加载、证书指纹 |
| internal/config/config.go | 服务端 JSON 配置（默认值 + 覆盖） |
| internal/agentstate/state.go | Agent 状态（server 地址、trust pins）与身份密钥文件 |
| internal/store/{model,store}.go | 数据模型、原子 JSON 持久化、CRUD、审计 JSONL |
| internal/server/server.go | 启动/关闭、控制端口 accept、认证握手、会话建立 |
| internal/server/session.go | 控制流循环、心跳、配置下发、数据流打开与双向转发 |
| internal/server/tunnels.go | 公网 TCP 监听生命周期、HTTP 域名路由、规则运行时状态/统计 |
| internal/server/backend.go | 管理台 Backend 实现（审批/规则/设置/日志/指标/登录校验） |
| internal/web/web.go | 路由、认证/CSRF 中间件、静态资源、视图类型、Backend 接口 |
| internal/web/handlers.go | 全部 JSON API handler |
| internal/agent/agent.go | 连接循环、握手、配置同步、数据流转发、TLS TOFU |
| internal/agentsvc/service.go | launchd / systemd 服务安装卸载 |
| internal/web/static/* | 单页管理台（原生 HTML/CSS/JS，无构建） |
| cmd/nrpd、cmd/nrp-agent | 参数解析、banner、信号处理 |

## 3. 任务拆分（可并行）

### 已完成（M1）

| ID | 任务 | 写入范围 | 验收 |
|----|------|----------|------|
| T1 | 协议与认证 | internal/protocol, internal/auth | 单测：帧往返/超限、签名校验/篡改；PROTOCOL.md 一致 |
| T2 | 存储与审计 | internal/store | 单测：CRUD、端口/域名唯一、级联删除、重启后持久化 |
| T3 | 服务端控制面 | internal/server/{server,session}.go | 端到端：pending→active→配置下发→心跳→踢下线 |
| T4 | 公网映射 | internal/server/tunnels.go | 端到端：TCP 隧道 curl 成功；禁用后端口关闭；HTTP Host 路由 |
| T5 | 管理 API + 管理台 | internal/web, internal/web/static | e2e 覆盖登录/CSRF/审批/规则/日志；UI 可完成全部操作 |
| T6 | 客户端 | internal/agent, internal/agentsvc | 首次连接→待审批→批准后自动上线→自动重连 |
| T7 | 部署 | deploy/, docs/DEPLOY.md | 服务端 systemd 启动、7600 可达、管理台可访问 |
| T8 | 测试与文档 | scripts/e2e.sh, docs/ | `go test ./...` 与 `bash scripts/e2e.sh` 全绿 |

### 下一阶段（M2，可并行分派）

| ID | 任务 | 写入范围 | 依赖 | 验收 |
|----|------|----------|------|------|
| M2-1 | HTTP 映射增强：HTTPS SNI 透传、路径级路由、响应头 X-Forwarded-For | internal/server/tunnels.go | T4 | 本地 https 站点可通过域名访问；WebSocket 正常 |
| M2-2 | SSE 实时状态（设备在线/流量） | internal/web + static | T5 | 页面无需轮询即可更新；断线自动重连 |
| M2-3 | Agent 本地服务健康检查（TCP dial + 状态上报） | internal/agent, protocol | T3 | 设备页显示每条映射的本地可达性 |
| M2-4 | Prometheus 指标完善 + Grafana 面板示例 | internal/server/backend.go, deploy/ | T5 | /metrics 覆盖每设备/每规则维度 |
| M2-5 | 管理台增强：批量操作、搜索、日志分页 | internal/web + static | T5 | 200 条规则下操作流畅 |
| M2-6 | TLS 自动证书（Let's Encrypt / DNS-01） | internal/tlsutil, cmd/nrpd | T7 | 域名证书自动续期；Agent 可用 -ca 标准校验 |
| M2-7 | 多管理员 + RBAC（只读/运维/超管） | store, web, server | T5 | 只读账号无法改配置；审计记录操作人 |

### 后续（M3/M4）

- SQLite 存储（保持 store 接口不变，新增 store/sqlite.go，用 `-store-driver` 切换）
- UDP 映射（协议新增 `proto="udp"`，服务端用 packet 转发而非 stream）
- 端口段按设备配额、流量限额、告警
- 集群：多服务端 + 设备归属调度

## 4. 协作约定

1. **写作用域**：每条任务只改自己范围；跨包改动先在任务里列出文件，避免并行冲突。
2. **协议变更**：改 `internal/protocol` 必须同时改 docs/PROTOCOL.md，并保证新旧版本可协商（`hello.version`）。
3. **新增依赖**：默认只用标准库；引入第三方需在 docs/DESIGN.md 的 ADR 表补一行理由。
4. **错误处理**：对外 API 用 `web.Err`；内部错误 `fmt.Errorf("...: %w")`；日志用 slog 键值对。
5. **测试**：proto/auth/store 改动必须带单测；涉及隧道的改动必须跑 `scripts/e2e.sh`。
6. **安全红线**：不把私钥/密码/明文 token 落盘或写日志；管理台任何写接口都要过 CSRF 中间件。

## 5. 本地开发速查

```bash
make build && bash scripts/e2e.sh        # 全链路
go test ./...                            # 单测
go vet ./...
# 手动起服务端（本机调试）
go run ./cmd/nrpd -data-dir /tmp/nrp-data -control-addr 127.0.0.1:7600 \
  -web-addr 127.0.0.1:7610 -public-host 127.0.0.1 -public-bind-addr 127.0.0.1
# 手动起 Agent
go run ./cmd/nrp-agent -s 127.0.0.1:7600 -state-dir /tmp/nrp-agent
```
