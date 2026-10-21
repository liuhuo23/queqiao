# NRP 设计方案（No-config Reverse Proxy）

> 一句话：**服务端集中配置的内网穿透**。客户端 Agent 跑在你自己的主机上，安装后自动生成身份密钥，
> 首次连接在服务端登记为"待审批设备"，管理员在网页审批并配置端口映射；之后 Agent 无配置文件、
> 无参数、断线自动重连。定位是 frp 的"免配置 + 网页管理"替代品。

- 代码语言：Go（单静态二进制，除 yamux 外零第三方依赖）
- 服务端：root@39.107.230.175（Alibaba Cloud Linux 3 / x86_64 / systemd 239）
- 客户端：本机（macOS arm64），同时支持 Linux / Windows
- Go module：nrp

---

## 1. 目标与非目标

### 1.1 目标

| # | 需求 | 设计响应 |
|---|------|----------|
| 1 | 类似 frp 的反向代理，但**客户端不需要配置** | Agent 只需一次性服务端地址（可编译期固化/安装时写入），其余全部由服务端下发 |
| 2 | 客户端用"一段密钥 + 端口 + ip/域名"连接服务端；密钥客户端生成；首次连接记录唯一机器码 | Agent 首次运行本地生成 Ed25519 密钥对与机器指纹；服务端 TOFU 绑定公钥+机器指纹，需管理员审批 |
| 3 | 所有配置在服务端，服务端带网页管理 | 设备/端口映射/HTTP 域名映射全部存服务端；内嵌管理后台（单页，无构建步骤） |

### 1.2 非目标（v1 明确不做）

- 多租户 / 自助注册（只有管理员 + 设备审批制）
- 集群 / 多服务端调度 / HA
- P2P 打洞（frp xtcp 那种）：v1 全部走服务端中转
- UDP / SOCKS5 / 插件生态 / 计费
- 客户端本地配置项（除一次性 server 地址、状态目录、-allow-lan 安全开关）

---

## 2. 核心设计决策（ADR 速览）

| 决策 | 选择 | 理由 / 被否方案 |
|------|------|----------------|
| 密钥形态 | **客户端生成 Ed25519 密钥对**；私钥永不离开客户端，服务端只存公钥 | 比"客户端生成随机共享密钥、服务端存哈希"更安全：拖库也无法冒充客户端；认证用签名，无需传输密钥 |
| 认证 | 服务端发 32B nonce，客户端对 domain‖nonce‖machine_id 签名 | 防重放；每次连接新 nonce |
| 机器绑定 | **TOFU**：首次连接记录机器指纹哈希并绑定公钥；换机指纹不匹配直接拒绝，管理员可"重绑定" | 满足"记录唯一机器码"要求，同时保留运维手段；只存哈希不存裸 MAC |
| 传输层 | TLS 1.2+（自签证书 + 客户端 TOFU 指纹固定；生产可换 Let's Encrypt） | 隧道跑 SSH/数据库等明文协议，必须加密；TOFU 实现零配置 |
| 多路复用 | hashicorp/yamux（唯一第三方依赖） | 一条 TLS 长连接承载控制流+全部数据流；自研 mux 的流控/半关闭易错，不值得 |
| 数据流方向 | **服务端**在公网连接到来时 OpenStream，首部带规则 ID + 真实客户端 IP；Agent 收到后 dial 本地服务 | 免去每连接握手；入站方向由服务端权威控制，客户端无法被滥用为入口 |
| 存储 | 单文件 JSON（原子写）+ 追加式审计 JSONL + 互斥锁 | 单实例、设备/规则规模远小于 10^4；免 cgo/SQLite；store 抽象为接口，后续可换 SQLite |
| 密码哈希 | 自实现 PBKDF2-HMAC-SHA256（21 万轮）+ 16B 盐 + 常量时间比较 | 不引入 x/crypto，格式自描述、参数可调 |
| Web 管理台 | go:embed 内嵌原生 HTML/CSS/JS，无 node/打包步骤 | 单二进制交付；低频后台无需前端框架 |
| 管理面鉴权 | 内存 Session + HttpOnly/SameSite=Lax Cookie + **CSRF 双提交**（X-NRP-CSRF 头） | 后台全是状态变更接口，必须防 CSRF |
| 端口映射 | 服务端按规则**动态监听**公网端口；设备离线即关闭监听 | 冲突在网页立刻可见；离线时不会"连上但一直挂住" |
| 配置下发 | 规则变更 → 生成设备配置快照（revision）→ 推送 → Agent ACK → 重绑定监听 | 幂等、可断线恢复：重连后直接下发最新快照 |
| 公网端口段 | 39000-39998（安全组放行段为 39000-40000，39999 留给控制端口） | 服务器现状：frps 占 7000/80/443/7500，nps 占 8024/2080/2443/43039，宝塔占 38888 |

---

## 3. 总体架构

```text
                         Internet
        ┌────────────────────────────────────────────────┐
        │  39.107.230.175 (nrpd)                         │
        │   :39999  control (TLS，客户端长连接)            │
        │   :39000-39998  TCP 映射监听（按规则动态创建）     │
        │   :可配  HTTP 域名映射监听（默认关）              │
        │   127.0.0.1:7610  Web 管理台（SSH 隧道/nginx 暴露）│
        └───────▲───────────────────────────▲────────────┘
                │ TLS + yamux               │ 公网用户访问 :39001
                │ 控制流 / 数据流             │
   ┌────────────┴────────────┐    ┌──────────┴─────────┐
   │ nrp-agent（你的主机）     │    │ 浏览器 / ssh / curl │
   │  · 身份密钥 + 机器指纹    │    └────────────────────┘
   │  · 自动重连 + 配置同步    │
   │  · 数据流 → dial 本地服务 │
   └────────────┬────────────┘
                │ 127.0.0.1:8080 / 127.0.0.1:22 / 局域网(-allow-lan)
                ▼
          本地服务 / 本机 SSH
```

**数据面时序（TCP 映射）**

```text
公网用户 ──TCP──▶ nrpd:39001 ──OpenStream──▶ yamux ──▶ agent
                                                   │ 首部 {rule_id, remote_addr}
                                                   ├─ dial 127.0.0.1:8080
                                                   └─ 回 0x00 后双向 io.Copy
公网用户 ◀──────────────────── 原始字节流 ◀────────────┘
```

**控制面时序（首次连接）**

```text
agent                                   nrpd
  │── TLS（TOFU pin）─────────────────────▶│
  │── hello{pubkey, machine_id, os...} ──▶│  ← 公钥未知：创建 pending 设备
  │◀──────── challenge{nonce} ────────────│
  │── proof{ed25519_sig} ────────────────▶│  ← 验签 + 校验机器指纹
  │◀── pending{key_code}（关闭，退避重试）──│  ← 等网页审批
  │                ...管理员在网页点"批准"...
  │── hello/proof（重连）─────────────────▶│
  │◀── ok{device_id, config_version} ─────│
  │── yamux: control stream ─────────────▶│
  │◀── config{rules, revision} ───────────│
  │── config_ack ────────────────────────▶│
  │◀──── ping / stats / event ────────────│  （双向心跳，15s）
  │◀──── data stream{rule, addr} ─────────│  （按需，公网连接到来时）
```

---

## 4. 身份与信任模型

### 4.1 客户端密钥

- Agent 首次运行在状态目录 **`~/.nrp`**（所有平台统一使用当前用户主目录下的 `.nrp`）生成 **Ed25519 密钥对**，私钥文件 0600。
- 服务端只保存**公钥**（base64）。
- 人类可读"密钥码"：NRP- + base32(SHA-256(pubkey)) 的前 12 字符分 4 组（如 NRP-7K3F-9Q2M-8XZP）。用于审批时肉眼核对，**不参与认证**。
- 私钥丢失 = 新设备（需重新审批）；管理员可删除旧记录。

### 4.2 机器指纹（首次连接登记）

machine_id = SHA-256("nrp-machine-v1" ‖ id)，id = 平台机器码；平台机器码缺失时回落到排序后的物理网卡 MAC

- Linux：/etc/machine-id；macOS：ioreg -rd1 -c IOPlatformExpertDevice 的 IOPlatformUUID；Windows：MachineGuid
- 平台机器码稳定、不受 VPN/网卡变化影响，优先使用；只有拿不到时才用 MAC 兜底
- 服务端存**哈希** + 可读标签（hostname / os / arch），不存裸 MAC

### 4.3 设备状态机

```text
            首次连接               管理员批准
  (无记录) ─────────▶ pending ──────────────▶ active ◀──┐
                       │                        │       │
                       │ 管理员拒绝/删除          │ 封禁   │ 解封
                       ▼                        ▼       │
                    (删除)                   blocked ────┘

  active 收到不同 machine_id → 认证失败（fingerprint_mismatch），网页标红"指纹冲突"
  管理员点"重绑定机器" → 清空指纹，设备回到 pending，下次连接重新登记
```

### 4.4 滥用处置

| 场景 | 处置 |
|------|------|
| 设备丢失 | 网页"封禁" → 断开会话、关闭监听、拒绝重连 |
| 私钥泄露 | 删除设备记录，同时把公钥加入**吊销列表**（该私钥永久无法再登记）；重装 Agent 生成新身份 |
| 换机 | "重绑定机器" → 清指纹 → 重新登记 |
| 暴力尝试 | 认证失败按 IP 令牌桶限流（10 次/分）；失败写审计 |
| MITM（首次） | 客户端对服务端证书 TOFU 固定指纹，之后变化硬失败 |

---

## 5. 协议概要

完整帧定义见 docs/PROTOCOL.md。要点：

- 控制连接 = TLS →（4 字节长度 + JSON 帧）握手 → 成功后同一连接升级为 yamux。
- 握手帧：hello → challenge → proof → ok / pending / reject。
- yamux 流类型（首字节）：0x01 控制流、0x02 数据流。
- 控制流消息：config（全量快照 + revision）、config_ack、ping/pong、stats（10s）、event（Agent 日志上送）、kick（踢下线）。
- 数据流：服务端发 {rule_id, remote_addr, proto} JSON 头，Agent 回 1 字节（0=成功 1=失败）后裸字节转发。
- 超时：握手 10s；控制流 45s 无帧判死链；重连退避 1s→30s（±20% 抖动）。
- 版本：hello.version 主版本不匹配直接 reject。

---

## 6. 服务端配置模型

```text
Device  : id, key_code, public_key, name, status, machine_id, hostname, os, arch,
          agent_version, last_ip, last_seen_at, approved_at, note
Rule    : id, device_id, name, proto(tcp|http), local_addr, remote_port, domain,
          enabled, created_at, updated_at
Admin   : id, username, password_hash(pbkdf2), created_at, last_login_at
Settings: public_host, port_range, http_addr, device_auto_approve, max_devices,
          admin_session_ttl, traffic_stats_enabled
Audit   : at, actor, action, target, detail, ip（append-only JSONL）
```

规则语义：

- tcp：服务端 0.0.0.0:<remote_port> 监听 → 隧道 → Agent dial local_addr。端口必须在 settings.port_range 内且不冲突（网页可预检）。
- http：服务端共享一个 HTTP 监听，按 Host 头匹配 domain → 隧道 → local_addr（含 WebSocket；v1 明文 HTTP，TLS 由前端 nginx 终止）。
- local_addr 默认只允许 loopback；Agent 需显式 -allow-lan 才允许私网地址，防止被入侵的服务端当内网跳板。

---

## 7. Web 管理台

单页应用（内嵌静态资源，/ 即入口）：

1. **登录**：首次启动自动生成 admin 随机密码，打印到 stderr 与 <data-dir>/initial-admin.txt（0600），提示尽快修改。
2. **概览**：版本、运行时长、在线/待审批设备数、规则数、连接数、累计流量。
3. **设备**：待审批列表（密钥码、主机名、系统、首次 IP）→ 批准/拒绝；已批准设备可改名、封禁/解封、重绑定、删除；显示在线状态、最后心跳、实时流量、该设备的映射。
4. **映射**：按设备增删改 TCP/HTTP 规则（本地地址、公网端口/域名、启停）；端口预检；修改即时生效并推送。
5. **日志**：审计日志 + Agent 上送事件。
6. **设置**：公网主机名、端口段、HTTP 监听、自动批准、最大设备数、修改密码。

前端只依赖 fetch；变更接口需 CSRF 头；输出一律 textContent 防 XSS；CSP 头。

**自动刷新策略**（避免刷新打断操作）：默认 4s 轮询（日志 8s），但只有数据签名变化时才整页重绘；
心跳/流量/运行时长等易变字段原地更新文本；重绘前后保持滚动位置；用户正在输入、打开弹窗或选中文本时自动暂停；
顶栏提供「自动刷新」开关（持久化到 localStorage）与「刷新」按钮；页面不可见时暂停轮询。

---

## 8. 安全设计（威胁模型）

| 威胁 | 对策 |
|------|------|
| 未授权客户端接入 | 公钥白名单 + 管理员审批；未审批设备无法建立数据面 |
| 公钥冒用 | 认证需私钥签名（挑战应答），公钥本身不可用 |
| 重放认证包 | 每次连接新 32B nonce，签名绑定 nonce，10s 有效 |
| 中间人 | TLS + 客户端 TOFU 证书指纹固定；生产可配 CA 校验 |
| 服务端被入侵后横向打内网 | Agent 默认只 dial loopback；-allow-lan 显式开启 |
| 管理台 CSRF/XSS | SameSite=Lax + HttpOnly；非 GET 需 X-NRP-CSRF；输出 textContent；CSP |
| 管理台撞库 | PBKDF2 21 万轮 + 登录限流 + 失败审计 |
| 存储泄露 | 只存公钥/密码哈希/机器指纹哈希 |
| 端口滥用 | 端口段限制 + 每设备规则数上限 + 并发上限（每规则 256、每 IP 64，超限直接拒绝） |

---

## 9. 可观测性

- 日志：slog，默认文本进 journald，-log-format json 可切。
- 审计：所有管理动作写 audit.jsonl，网页可查。
- 指标：/metrics（Prometheus 文本格式；在线设备、连接数、字节数、认证失败数）。
- 流量：服务端按规则统计上下行字节/连接数，网页展示。

---

## 10. 容量与限制

- 单实例；预期 ≤ 200 台设备、≤ 2000 条规则、同时在线 ≤ 5000 连接。
- 服务端每会话约 64KB + 每连接约 32KB 缓冲；2GB 内存机器可承载数百并发。
- JSON 存储在 10^4 级实体下 O(n) 全量写；超出换 SQLite（store 已抽象接口）。
- 单 TLS 连接承载单设备全部流量，受单核 TLS 加解密限制（约数百 Mbps 量级）。

---

## 11. 部署方案（39.107.230.175）

| 用途 | 端口 | 说明 |
|------|------|------|
| 控制端口 | 39999/tcp（实际部署；设计默认 7600） | 安全组只放行 39000-40000，因此放在 39999；规则端口会自动避开它 |
| 公网映射 | 39000-39998/tcp | 已在 firewalld 放行；remote_port 必须落在此范围 |
| 管理台 | 127.0.0.1:7610 | 默认只监听本机；ssh -L 7610:127.0.0.1:7610 root@39.107.230.175 访问（零安全组改动）；或 nginx 反代 |
| HTTP 域名映射 | 可配（默认关） | 80/443 被 frps 占用；切换期用 :39080，替换 frp 后再改 80/443 |

部署物：

```text
/usr/local/bin/nrpd
/etc/nrp/nrpd.json
/var/lib/nrp/            # 配置 JSON、TLS 自签证书、审计日志
/etc/systemd/system/nrpd.service
```

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o dist/nrpd ./cmd/nrpd
go build -o dist/nrp-agent ./cmd/nrp-agent
```

详见 docs/DEPLOY.md。

---

## 12. 与 frp 的对比

| 维度 | frp | NRP |
|------|-----|-----|
| 客户端配置 | frpc.toml，每条代理都要写，改配置要重启 | 无配置文件；服务端网页改，秒级下发 |
| 身份 | 预共享 token | 客户端生成 Ed25519 密钥 + 机器指纹绑定 + 审批 |
| 管理 | 有 dashboard，但配置仍以文件为准 | 配置只在服务端，网页即唯一事实源 |
| 部署 | 两个二进制 + 配置 | 服务端一个二进制 + 客户端一个二进制（零配置） |
| 适合场景 | 通用/团队/复杂拓扑 | 个人多设备、给非专业用户装一次就不用管 |

保留 frp 的成熟点：TCP/HTTP 两种映射、TLS、yamux 复用、断线重连、流量统计。

---

## 13. 里程碑

| 阶段 | 内容 | 状态 |
|------|------|------|
| M1 | 骨架 + 协议 + 认证 + TCP 映射 + 管理台（登录/设备审批/规则 CRUD）+ 端到端冒烟 | 本次交付 |
| M2 | HTTP 域名映射、SSE 实时状态、Prometheus、Agent 服务自安装 | 下一阶段 |
| M3 | 审计查询、设备分组、限流策略、Let's Encrypt | 后续 |
| M4 | SQLite、多管理员/RBAC、UDP、健康检查 | 后续 |

实现任务拆分与文件归属见 docs/AGENT_TASKS.md；接口契约见 docs/API.md 与 docs/PROTOCOL.md。
