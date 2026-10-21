# NRP — 免配置内网穿透（frp 的可管理替代）

> 装好客户端就忘了它：**所有映射都在服务端网页上配置**。客户端首次运行本地生成密钥，
> 在服务端登记并等待管理员审批，之后自动同步配置、断线重连、开机自启。

- 语言：Go（单静态二进制，唯一第三方依赖 yamux）
- 服务端：`nrpd`（控制面 TLS + 公网端口映射 + Web 管理台）
- 客户端：`nrp-agent`（macOS / Linux / Windows，零配置文件）
- 设计文档：[docs/DESIGN.md](docs/DESIGN.md) · [线路协议](docs/PROTOCOL.md) · [管理 API](docs/API.md) · [部署](docs/DEPLOY.md)

## 它解决什么问题

| frp | NRP |
|-----|-----|
| 每台机器写 `frpc.toml`，改映射要改文件 + 重启 | 客户端只跑一个命令，映射在网页上改，秒级下发 |
| 预共享 token，泄露即可接入 | 客户端生成 Ed25519 密钥，首次连接绑定机器指纹 + 人工审批 |
| 配置文件是事实源，面板只是展示 | 服务端配置是唯一事实源，网页即管理入口 |

## 工作方式

```text
        浏览器 ──▶ 管理台 127.0.0.1:7610（SSH 隧道/nginx）
                        │ 配置：审批设备 / 映射规则
                        ▼
公网用户 ──▶ :39001 ──▶ [ nrpd 39.107.230.175 ] ◀── TLS 长连接 ── [ nrp-agent 你的主机 ]
                              ▲  控制流 + yamux 数据流                 │
                              └────────── 127.0.0.1:8080 ◀────────────┘
```

1. Agent 首次运行：生成密钥 + 机器指纹 → 连接服务端 → 登记为「待审批」，打印密钥码。
2. 管理员在网页批准，并为它添加映射（如 `127.0.0.1:8080 → 公网 39001`）。
3. Agent 自动收到配置；访问 `http://服务器:39001` 即到本机 8080。
4. 后续改映射/封禁/重绑定全部在网页完成；Agent 不需要任何操作。

## 快速开始

### 服务端（Linux x86_64）

```bash
make linux-server
scp dist/nrpd-linux-amd64 root@39.107.230.175:/usr/local/bin/nrpd
# 见 docs/DEPLOY.md：配置 /etc/nrp/nrpd.json、systemd、firewalld、安全组
```

### 客户端（本机 macOS）

```bash
make agent
sudo install -m 0755 dist/nrp-agent /usr/local/bin/nrp-agent

# 首次运行：指定服务端地址（之后会记住）
nrp-agent -s 39.107.230.175:39999

# 输出（示例）：
#   ================= 设备待审批 =================
#     密钥:   NRP-7K3F-9Q2M-8XZP
#     主机:   macbook-pro
#   → 在管理台批准后自动连上

# 批准后安装为开机自启（macOS launchd / Linux systemd）
nrp-agent install-service
```

管理台访问（无需开放端口）：

```bash
ssh -L 7610:127.0.0.1:7610 root@39.107.230.175
# 浏览器打开 http://127.0.0.1:7610，账号 admin，初始密码见 /var/lib/nrp/initial-admin.txt
```

## 目录结构

```text
cmd/
  nrpd/                服务端入口（flag/config/systemd）
  nrp-agent/           客户端入口（run/status/identity/install-service）
internal/
  protocol/            线路协议帧与消息（握手、控制流、数据流）
  auth/                Ed25519 密钥、密钥码、挑战-应答
  passwd/              PBKDF2 密码哈希（自包含，不引入 x/crypto）
  machineid/           机器指纹（平台机器码优先，MAC 兜底）
  tlsutil/             自签证书生成/加载、指纹
  config/              服务端配置文件
  agentstate/          Agent 本地状态与身份密钥
  store/               设备/规则/管理员/设置存储 + 审计日志
  server/              控制面握手、会话、公网监听、Web 后端实现
  web/                 管理台 HTTP API + 内嵌前端（static/）
  agent/               客户端连接、配置同步、数据转发
  agentsvc/            安装为 launchd/systemd 服务
  logx/ version/
web 前端（内嵌）：internal/web/static/{index.html,app.js,style.css}
deploy/                systemd unit、配置示例、nginx 反代
scripts/e2e.sh         端到端冒烟测试（本机全链路，无需 root/外网）
docs/                  DESIGN / PROTOCOL / API / DEPLOY / AGENT_TASKS
```

## 开发

```bash
go build ./...          # 需要 Go 1.22+
go vet ./...
go test ./...           # 单元测试
bash scripts/e2e.sh     # 起服务端 + Agent + 批审批 + 建映射 + 真实 HTTP 请求
make build              # 产出 dist/nrpd、dist/nrp-agent
```

> 在受限环境（如本仓库的沙箱）中构建时，把 GOMODCACHE/GOCACHE 指到工作目录内即可，例如：
> `GOMODCACHE=$PWD/.gomodcache/gopath/pkg/mod GOCACHE=$PWD/.gomodcache/build go build ./...`

## 安全设计（摘要）

- 私钥只在客户端；服务端只存公钥。认证 = Ed25519 挑战-应答（防重放）。
- 首次连接绑定机器指纹（平台机器码；缺失时用物理网卡 MAC），换机会被拒绝，需管理员重绑定。
- 客户端对服务端证书做 TOFU 指纹固定（也可 `-ca` 走标准校验）。
- Agent 默认只能连本机 loopback，`-allow-lan` 才放开私网，防止服务端被入侵后横向打内网。
- 管理台：HttpOnly + SameSite Cookie、CSRF 头、PBKDF2、登录限流、默认只监听 127.0.0.1。
- 完整威胁模型见 [docs/DESIGN.md §8](docs/DESIGN.md)。

## 状态与路线图

- **M1（已完成）**：TLS 握手 + 密钥认证 + 机器指纹审批、TCP 映射、Web 管理台（登录/设备/映射/日志/设置）、端到端测试、systemd/launchd 安装。
- **M2**：HTTP 域名映射完善（SNI/HTTPS 透传）、SSE 实时状态、Agent 健康检查、Prometheus 面板。
- **M3**：多管理员/RBAC、审计查询、Let's Encrypt、端口段策略。
- **M4**：SQLite 存储、UDP、集群。

任务拆分与文件归属见 [docs/AGENT_TASKS.md](docs/AGENT_TASKS.md)。
