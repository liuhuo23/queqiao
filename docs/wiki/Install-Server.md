# 服务端部署

## 前置检查

- Linux x86_64，systemd，root 权限
- 一个公网可达的控制端口（默认 `7600`；若云安全组限制，可放进映射段如 `39999`）
- 一段公网映射端口（示例 `39000-40000`）
- 管理台只监听 `127.0.0.1:7610`，通过 SSH 隧道访问，不需要公网开放

## 端口规划

| 用途 | 示例 | 说明 |
|------|------|------|
| 控制端口 | 7600/tcp | TLS，需要防火墙 + 安全组放行 |
| 公网映射 | 39000-40000/tcp | 规则里的 `remote_port` 必须在此范围 |
| 管理台 | 127.0.0.1:7610 | 只监听本机，SSH 隧道访问 |
| HTTP 域名映射 | 可配（默认关） | 80/443 被占用时可先用 `:39080` |

## 安装

```bash
# 本机构建（linux/amd64）
make linux-server
scp dist/queqiaod-linux-amd64 root@YOUR_SERVER_IP:/usr/local/bin/queqiaod

ssh root@YOUR_SERVER_IP '
  chmod +x /usr/local/bin/queqiaod
  mkdir -p /etc/queqiao /var/lib/queqiao
  chmod 700 /var/lib/queqiao
'
```

## 配置文件

` /etc/queqiao/queqiaod.json`：

```json
{
  "control_addr": ":7600",
  "web_addr": "127.0.0.1:7610",
  "data_dir": "/var/lib/queqiao",
  "public_host": "YOUR_SERVER_IP",
  "public_bind_addr": "0.0.0.0",
  "http_addr": "",
  "port_range": [39000, 40000],
  "log_level": "info",
  "log_format": "text"
}
```

字段说明：

| 字段 | 说明 |
|------|------|
| `control_addr` | 客户端连接的 TLS 控制端口 |
| `web_addr` | 管理台监听地址；建议保持 127.0.0.1 |
| `data_dir` | 数据目录（设备/规则/审计/TLS 证书） |
| `public_host` | 网页里展示的公网主机名/IP |
| `public_bind_addr` | 公网映射监听地址，默认 0.0.0.0 |
| `http_addr` | HTTP 域名映射监听地址；空=关闭 |
| `port_range` | 允许分配的映射端口段 |
| `log_level` / `log_format` | 日志级别与格式（text/json） |

> `public_host`/`port_range`/`http_addr` 只在**首次启动、数据目录为空**时写入设置；之后以网页「设置」为准。

## systemd

```bash
scp deploy/queqiaod.service root@YOUR_SERVER_IP:/etc/systemd/system/
ssh root@YOUR_SERVER_IP 'systemctl daemon-reload && systemctl enable --now queqiaod'
ssh root@YOUR_SERVER_IP 'systemctl status queqiaod --no-pager'
ssh root@YOUR_SERVER_IP 'journalctl -u queqiaod -f'
```

## 防火墙 / 安全组

```bash
firewall-cmd --permanent --add-port=7600/tcp
firewall-cmd --permanent --add-port=39000-40000/tcp
firewall-cmd --reload
firewall-cmd --list-ports
```

云厂商安全组同样放行控制端口与映射段。

## TLS 与证书

- 服务端首次启动自动生成自签 ECDSA 证书，落盘 `/var/lib/queqiao/tls/`；
- 客户端首次连接会把证书指纹固定在本机（TOFU），之后指纹变化会硬失败；
- 要使用正式证书，可在配置里指定 `tls_cert`/`tls_key`，或在前面用 nginx 终止 TLS（管理台另见）。

## 初始管理员

```bash
cat /var/lib/queqiao/initial-admin.txt
```

首次登录后立刻在「设置」改密码；改密成功会删除该文件。忘记密码时：停止服务、备份 `state.json`、删除其中 `admins` 数组后重启，会重新生成初始管理员（设备/规则不受影响）。

## 升级 / 备份 / 回滚

```bash
# 升级
scp dist/queqiaod-linux-amd64 root@YOUR_SERVER_IP:/usr/local/bin/queqiaod.new
ssh root@YOUR_SERVER_IP 'systemctl stop queqiaod && mv /usr/local/bin/queqiaod /usr/local/bin/queqiaod.bak && mv /usr/local/bin/queqiaod.new /usr/local/bin/queqiaod && systemctl start queqiaod'

# 备份（设备/规则/审计/TLS）
ssh root@YOUR_SERVER_IP 'tar czf /root/queqiao-data-$(date +%F).tar.gz -C /var/lib queqiao'

# 回滚
ssh root@YOUR_SERVER_IP 'systemctl stop queqiaod && mv /usr/local/bin/queqiaod.bak /usr/local/bin/queqiaod && systemctl start queqiaod'
```

## 访问管理台

```bash
ssh -L 7610:127.0.0.1:7610 root@YOUR_SERVER_IP
# 浏览器打开 http://127.0.0.1:7610
```
