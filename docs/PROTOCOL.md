# 鹊桥 线路协议 v1

服务端与 Agent 之间的全部通信都在**同一条 TLS 连接**上完成：

```text
TLS 握手
  └─ 明文 JSON 握手帧（4 字节大端长度 + JSON）   ← 身份认证
       └─ 认证成功 → 升级为 yamux 会话
            ├─ stream 0x01  control（Agent 主动打开，长度前缀 JSON）
            └─ stream 0x02  data    （服务端按需打开，1 字节类型 + JSON 头 + 原始字节）
```

- 字符编码：UTF-8 JSON；所有时间戳为 Unix 秒（int64）或 RFC3339（管理 API 用后者）。
- 长度前缀：uint32 大端，**不含**长度字段自身；单帧上限 1 MiB（数据流无此上限，数据流是裸字节）。
- 版本：hello.version = 1；服务端主版本不等即 reject(unsupported_version)。

---

## 1. 认证握手

### 1.1 帧序列

```text
Agent                                          Server
  | -- hello {version, public_key, machine_id, hostname, os, arch, agent_version, key_code?} --> |
  | <------------------- challenge {nonce, device_id, key_code, status} -------------------- |
  | -- proof {signature} ------------------------------------------------------------------->  |
  | <-- ok {device_id, key_code, config_version, heartbeat_sec, server_time}                |  → yamux
  | <-- pending {device_id, key_code, retry_after_sec, message}                             |  → 关闭
  | <-- reject {code, message}                                                             |  → 关闭
```

- hello.public_key：Ed25519 公钥，base64（std，无换行），固定 32 字节。
- hello.machine_id：64 位小写 hex（SHA-256）。
- hello.key_code：可选；Agent 本地缓存过的密钥码，仅用于服务端日志辅助（服务端以 public_key 为准）。
- challenge.nonce：32 字节随机数，base64；10 秒内有效，一次性。
- proof.signature：Ed25519 签名，base64；签名内容（字节拼接）：
  ```text
  "queqiao-auth-v1" ‖ 0x00 ‖ nonce ‖ 0x00 ‖ ASCII(machine_id)
  ```

### 1.2 服务端判定逻辑

| 条件 | 响应 |
|------|------|
| public_key 不存在 | 创建设备（status=pending，记录 machine_id/指纹标签）→ 仍发 challenge 验签 → pending |
| public_key 存在且 status=pending | 验签后 → pending（等待审批；审批后下次连接即 ok） |
| public_key 存在且 status=active，machine_id 一致 | ok |
| public_key 存在且 status=active，machine_id **不一致** | reject(fingerprint_mismatch)；网页设备标红 |
| public_key 存在且 status=blocked | reject(blocked) |
| 验签失败 / nonce 失效 | reject(bad_proof / bad_nonce) |
| 同 IP 认证尝试超限 | reject(rate_limited) |
| 设备数超过 settings.max_devices | reject(device_limit) |
| 已是 active 但 machine_id 不同且管理员已允许重绑定 | 管理员点「重绑定」后指纹清空、设备回到 pending；下次连接以新指纹重新登记，需再次批准 |

### 1.3 错误码

| code | 含义 | Agent 行为 |
|------|------|-----------|
| unsupported_version | 协议版本不匹配 | 报错退出（提示升级） |
| bad_proof | 签名校验失败 | 退出并提示（本机身份密钥可能损坏，可 reset-identity） |
| bad_nonce | nonce 过期/重复（保留，v1 服务端不会发送） | 立即重连一次 |
| fingerprint_mismatch | 机器指纹变化 | 每 5 分钟重试，打印"需管理员重绑定" |
| blocked | 设备被封禁/公钥已吊销 | 每 5 分钟重试，打印提示 |
| device_limit | 服务端设备数已满 | 每 5 分钟重试 |
| rate_limited | 认证限流 | 退避重试（60s） |
| server_busy | 服务端负载保护/待审批设备过多 | 退避重试（1 分钟） |

---

## 2. 控制流（stream 类型 0x01）

Agent 认证成功后立刻打开第一条 yamux 流，首字节 0x01，之后双向发送长度前缀 JSON 帧。

| type | 方向 | 字段 | 说明 |
|------|------|------|------|
| config | S→A | revision:int64, rules:[RuleSpec] | 全量配置快照（覆盖式）；rules 只含 enabled=true 且属于该设备的规则 |
| config_ack | A→S | revision:int64, ok:bool, error?:string | Agent 应用结果（逐条 dial 检查可选，v1 只校验地址合法性） |
| ping | S→A | ts:int64 | 服务端每 15s 发；Agent 回 pong |
| pong | A→S | ts:int64 | 任意控制帧都刷新服务端读截止时间 |
| stats | A→S | at:int64, rules:[{rule_id, bytes_in, bytes_out, conns_total}] | Agent 侧统计（v1 每 10s；服务端另有公网侧统计） |
| event | A→S | level:string, msg:string, rule_id?:string | Agent 日志上送，进入服务端事件环形缓冲与审计文件 |
| kick | S→A | reason:string | 设备被删除/封禁/重绑定 → Agent 断开后按退避重连（会被拒绝） |
| shutdown | S→A | reason:string | 服务端优雅停机，Agent 5s 后重连 |

RuleSpec：

```json
{"id":"rule_ab12cd34ef56","name":"web","proto":"tcp","local_addr":"127.0.0.1:8080","remote_port":39001,"domain":""}
```

- Agent 收到 config 后：替换本地规则表（只影响后续数据流 dial，不主动监听），回 config_ack；revision 小于已应用值的旧配置会被忽略。
- 服务端对同一会话的「计算快照 + 发送」串行化，避免并发修改导致旧配置后到。
- revision 单调递增（持久化在 <data-dir>/state.json 的 config_revision，重启后继续递增）。

---

## 3. 数据流（stream 类型 0x02）

服务端在公网连接到来时打开流：

```text
[S→A] 1 字节 0x02
[S→A] 4 字节大端 len + JSON 头 {"rule_id":"rule_...","proto":"tcp","remote_addr":"1.2.3.4:56789"}
[A]   1 字节状态：0x00 = dial 成功，0x01 = dial 失败（随后关闭流）
[双向] 原始字节流（io.Copy），任一方向 EOF/错误即关闭
```

- 服务端收到 0x01 或 10s 内未收到状态字节 → 关闭公网连接（发 RST，不返回半截数据）。
- 服务端在写 JSON 头前已消费掉首字节，Agent 侧用 io.ReadFull 读满。
- 半关闭：一侧 EOF → CloseWrite 语义由 yamux 流支持，反方向继续传输到 EOF（TCP 半关闭透传）。
- 空闲：不设数据流超时（SSH 等长连接靠自身 keepalive）；连接数上限由服务端保护。

---

## 4. 心跳、超时、重连

| 参数 | 值 | 说明 |
|------|----|------|
| 握手超时 | 10s | TLS 建连后到 challenge 发出前的 hello；proof 后等待结果 |
| 控制流读超时 | 45s | 45s 内无任何控制帧 → 判定死链并关闭 |
| 服务端 ping 间隔 | 15s | Agent 收到即回 pong |
| 控制流写超时 | 10s | 写阻塞超时即关闭会话 |
| Agent 重连退避 | 1s 起，×2，上限 30s，±20% 抖动 | 认证 ok 后重置 |
| pending 轮询 | retry_after_sec（默认 5s） | 等待审批期间 |
| TLS 握手超时 | 10s | |

会话替换：同一 device 新连接认证成功时，服务端向旧会话发 kick(reason="replaced") 并关闭旧会话（旧连接上的数据流随之断开）。

---

## 5. TLS 与证书固定

- 服务端启动时若未配置证书，自动生成自签 ECDSA P-256 证书（SAN 含 settings.public_host、localhost、127.0.0.1；若要用本机 IP 访问，请把 IP 写进 public_host），有效期 10 年，落盘 <data-dir>/tls/。
- Agent：
  - 有 -ca 参数 → 标准校验。
  - 无 -ca → 首次连接把服务端证书 SHA-256 指纹写入 <state-dir>/agent.json 的 trust_pins，之后校验固定指纹；不匹配则硬失败并提示。
  - -insecure 显式关闭校验（仅调试）。
- 管理台（HTTP）默认只监听 127.0.0.1，不建议直接暴露公网；需要公网访问时由 nginx 终止 TLS。

---

## 6. 管理 API 与线路协议的关系

- 管理 API（docs/API.md）只影响服务端存储；服务端在规则/设备变更后立即向相关 Agent 推送新 config 或 kick。
- 公网监听的生命周期 = 规则 enabled && 设备在线：设备断线 → 关闭该设备全部监听；重连 → 按最新快照重新创建监听。
- 端口冲突在创建监听时检测，失败信息通过设备/规则状态暴露给网页（不阻塞其他规则）。
