# 配置参考

## 服务端配置文件

路径：` /etc/queqiao/queqiaod.json`（可用 `-config` 指定）。

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `control_addr` | string | `:7600` | 客户端 TLS 控制端口 |
| `web_addr` | string | `127.0.0.1:7610` | 管理台监听地址 |
| `data_dir` | string | `/var/lib/queqiao` | 数据目录 |
| `public_host` | string | 空 | 网页展示的公网主机 |
| `public_bind_addr` | string | `0.0.0.0` | 公网映射监听地址 |
| `http_addr` | string | 空 | HTTP 域名映射监听（空=关闭） |
| `port_range` | [2]int | `[39000,40000]` | 映射端口段 |
| `tls_cert` / `tls_key` | string | 空 | 自定义证书；空则自签并落盘 |
| `log_level` | string | `info` | debug/info/warn/error |
| `log_format` | string | `text` | text/json |

命令行参数会覆盖配置文件；`-version` 打印版本。

## 管理台设置（持久化在数据目录 state.json）

| 字段 | 默认 | 说明 |
|------|------|------|
| `public_host` | - | 公网主机 |
| `port_range` | [39000,40000] | 端口段 |
| `http_addr` | 空 | HTTP 监听 |
| `device_auto_approve` | false | 自动批准新设备 |
| `max_devices` | 200 | 最大设备数 |
| `max_rules_per_device` | 50 | 每设备映射上限 |
| `admin_session_ttl_sec` | 43200 | 管理台会话有效期 |
| `traffic_stats_enabled` | true | 是否统计流量 |

## 映射规则

| 字段 | 说明 |
|------|------|
| `proto` | `tcp` 或 `http` |
| `local_addr` | 客户端侧地址，如 `127.0.0.1:8080` |
| `remote_port` | TCP 映射的公网端口（必须在端口段内且未占用） |
| `domain` | HTTP 映射的域名（全局唯一） |
| `enabled` | 是否启用 |

## 客户端状态

- `identity.json`：身份密钥（0600），丢失=新设备；
- `agent.json`：服务端地址、设备 ID、证书指纹（`trust_pins`）；
- 重置身份：`queqiao reset-identity -yes`。

## 数据与备份

- `state.json`：设备/规则/管理员/设置/吊销列表；
- `audit.jsonl`：审计日志（追加写）；
- `tls/`：自签证书与私钥；
- `initial-admin.txt`：初始密码（改密后删除）。

建议定期备份整个数据目录，并保持 `0700` 权限。

## API 与协议

管理 API、线路协议细节见仓库：[docs/API.md](https://github.com/liuhuo23/queqiao/blob/master/docs/API.md)、[docs/PROTOCOL.md](https://github.com/liuhuo23/queqiao/blob/master/docs/PROTOCOL.md)。
