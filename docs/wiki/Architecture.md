# 架构与协议

## 总体架构

```text
公网用户 ──▶ 服务器 :39001 ──▶ [ queqiaod ] ◀── TLS + yamux 长连接 ── [ queqiao 本机 ]
                                   ▲
                              管理台 127.0.0.1:7610
                              （SSH 隧道访问）
```

## 关键设计

| 设计 | 说明 |
|------|------|
| 密钥认证 | 客户端生成 Ed25519 密钥；服务端发 nonce，客户端签名，服务端验签（防重放） |
| 机器指纹 | 首次连接登记平台机器码哈希；换机拒绝，管理员可重绑定 |
| 公钥吊销 | 删除设备时把公钥加入吊销列表，泄露私钥不能重新登记 |
| TLS | 自签证书 + 客户端 TOFU 指纹固定；也可用正式证书 |
| 复用 | 一条 TLS 连接上跑 yamux：控制流 + 数据流 |
| 数据面 | 服务端在公网连接到来时 OpenStream，Agent dial 本地服务并双向转发 |
| 配置下发 | 服务端保存全量快照（revision），客户端 ack；旧 revision 被忽略 |
| 存储 | 单文件 JSON 原子写 + 审计 JSONL；规模适合单机个人/小团队 |

## 安全模型

- 私钥只在客户端，服务端只存公钥；
- 未审批设备无法建立数据面；
- 管理台：HttpOnly + SameSite Cookie、CSRF 头、PBKDF2 哈希、登录限流、默认只监听 loopback；
- Agent 默认只 dial loopback，`-allow-lan` 才放开私网；
- 每个映射有并发上限（每规则 256 / 每 IP 64）。

## 线路协议

握手：`hello → challenge → proof → ok / pending / reject`；认证成功后升级为 yamux。
完整帧定义见仓库 [docs/PROTOCOL.md](https://github.com/liuhuo23/queqiao/blob/master/docs/PROTOCOL.md)，
管理 API 见 [docs/API.md](https://github.com/liuhuo23/queqiao/blob/master/docs/API.md)。
