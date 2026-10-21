# 部署手册（服务端 YOUR_SERVER_IP + 本机 Agent）

## 0. 前置检查

- Linux x86_64（systemd），root 权限
- 一个公网可达的控制端口（默认 `:7600`；如果云安全组只放行固定端口段，可以把控制端口放进该段，例如 `:39999`）
- 一段可用于公网映射的 TCP 端口（示例 `39000-40000`），需要在 firewalld 与云安全组放行
- 管理台默认只监听 `127.0.0.1:7610`，通过 SSH 隧道访问
- 如果 80/443 已被其它服务占用，HTTP 域名映射可先用 `:39080`

端口规划（示例）：

| 用途 | 端口 | 备注 |
|------|------|------|
| 控制端口 | 7600/tcp（或 39999） | TLS；需防火墙 + 安全组放行 |
| 公网映射 | 39000-40000/tcp | 规则里的 `remote_port` 必须落在此区间 |
| 管理台 | 127.0.0.1:7610 | 默认只监听本机，用 SSH 隧道访问 |
| HTTP 域名映射 | 可配（默认关） | 80/443 被占用时可先用 `:39080` |

> 如果云安全组只放行映射段（例如 39000-40000），可把 `control_addr` 设为 `:39999`、`port_range` 设为 `[39000, 39998]`，把 39999 留给控制端口。

## 1. 构建

```bash
# 在本机仓库根目录
make linux-server           # -> dist/queqiaod-linux-amd64
make agent                  # -> dist/queqiao（本机 macOS）
```

## 2. 安装服务端

```bash
scp dist/queqiaod-linux-amd64 root@YOUR_SERVER_IP:/usr/local/bin/queqiaod
ssh root@YOUR_SERVER_IP '
  chmod +x /usr/local/bin/queqiaod
  mkdir -p /etc/queqiao /var/lib/queqiao
  chmod 700 /var/lib/queqiao
'
```

/etc/queqiao/queqiaod.json：

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

> 配置里的 `public_host` / `port_range` / `http_addr` 只在**首次启动、数据目录为空**时写入设置；
> 之后以网页「设置」为准（唯一事实源）。

systemd（deploy/queqiaod.service 可直接用）：

```bash
install -m 0644 deploy/queqiaod.service /etc/systemd/system/queqiaod.service
systemctl daemon-reload
systemctl enable --now queqiaod
systemctl status queqiaod --no-pager
journalctl -u queqiaod -f
```

防火墙 / 安全组：放行控制端口与映射段，例如

```bash
firewall-cmd --permanent --add-port=7600/tcp
firewall-cmd --permanent --add-port=39000-40000/tcp
firewall-cmd --reload
firewall-cmd --list-ports
```

云厂商安全组也要做同样放行（控制端口 + 映射段）。如果控制端口改成了 39999，就把上面的 7600 换成 39999。

启动后：

- TLS 自签证书自动生成在 `/var/lib/queqiao/tls/`（客户端首次连接会 TOFU 固定指纹）
- 初始管理员密码在 `/var/lib/queqiao/initial-admin.txt`（权限 0600，登录后请立即改密码；改密成功后该文件会被删除）

## 3. 访问管理台

无需开放 7610 到公网，SSH 隧道即可：

```bash
ssh -L 7610:127.0.0.1:7610 root@YOUR_SERVER_IP
# 打开 http://127.0.0.1:7610
```

如果要用域名 + HTTPS 暴露，见 deploy/nginx-queqiao.conf（nginx 终止 TLS 后反代到 127.0.0.1:7610）。

首次登录后：
1. 「设置」里确认公网主机、端口段；
2. 修改管理员密码（至少 12 位）；
3. 需要 HTTP 域名映射时，把 `http_addr` 设为 `:39080`（或替换 frp 后用 `:80`）。

## 4. 安装客户端（本机 macOS）

客户端数据统一放在当前用户目录下的 `.queqiao`（二进制、身份、状态、日志都在这里）：

```bash
make agent
mkdir -p ~/.queqiao/bin
install -m 0755 dist/queqiao ~/.queqiao/bin/queqiao

# 首次运行：登记设备（会打印密钥码并停在"待审批"）
~/.queqiao/bin/queqiao -s YOUR_SERVER_IP:39999

# 在管理台「设备」页批准（密钥码应一致），Agent 会自动连上
# 安装为开机自启（launchd），之后无需再手动运行：
~/.queqiao/bin/queqiao install-service -s YOUR_SERVER_IP:39999
~/.queqiao/bin/queqiao status
tail -f ~/.queqiao/queqiao.log
```

- macOS 的 LaunchAgent plist 仍在 `~/Library/LaunchAgents/io.queqiao.agent.plist`（launchd 固定要求），
  其 ProgramArguments 指向 `~/.queqiao/bin/queqiao`，StandardOut/ErrorPath 为 `~/.queqiao/queqiao.log` / `~/.queqiao/queqiao.err.log`。
- Linux 下 `install-service` 写 `/etc/systemd/system/queqiao.service`，日志进 journald。
- Windows：`queqiao.exe -s host:39999` 首次运行，然后用任务计划程序/服务管理器设置开机启动；状态目录为 `%USERPROFILE%\.queqiao`。

## 5. 添加第一条映射

管理台「映射」→ 新建：

| 字段 | 示例 | 说明 |
|------|------|------|
| 设备 | my-laptop | 已批准的设备 |
| 协议 | tcp | http 用于域名映射 |
| 本地地址 | 127.0.0.1:8080 | Agent 所在主机上的服务 |
| 公网端口 | 39001 | 必须在端口段内且未被占用（可先点「检测端口」） |

保存后立即生效。验证：

```bash
curl -v http://YOUR_SERVER_IP:39001/
# 或从本机：ssh -p 39022 root@YOUR_SERVER_IP（若映射了 127.0.0.1:22 -> 39022）
```

## 6. 升级与回滚

```bash
# 升级
scp dist/queqiaod-linux-amd64 root@YOUR_SERVER_IP:/usr/local/bin/queqiaod.new
ssh root@YOUR_SERVER_IP 'systemctl stop queqiaod && mv /usr/local/bin/queqiaod /usr/local/bin/queqiaod.bak && mv /usr/local/bin/queqiaod.new /usr/local/bin/queqiaod && systemctl start queqiaod'

# 数据备份（设备/规则/审计/TLS 证书）
ssh root@YOUR_SERVER_IP 'tar czf /root/queqiao-data-$(date +%F).tar.gz -C /var/lib queqiao'

# 回滚
ssh root@YOUR_SERVER_IP 'systemctl stop queqiaod && mv /usr/local/bin/queqiaod.bak /usr/local/bin/queqiaod && systemctl start queqiaod'
```

> 重装服务端会重新生成 TLS 证书，已连接过的 Agent 会报「证书指纹变化」。
> 删掉 Agent 状态目录里的 `trust_pins`（或 `queqiao status` 查看路径）后重连即可。

## 7. 常见问题

| 现象 | 原因 / 处理 |
|------|-------------|
| 公网端口连不上 | 检查安全组/防火墙、规则 state 是否 `active`、设备是否在线 |
| 规则 state=error | 端口被系统进程占用或不在端口段内（网页有原因） |
| Agent 一直待审批 | 管理台「设备」页批准；密钥码应与 Agent 打印的一致 |
| fingerprint_mismatch | 换了机器/重装系统：管理台「重绑定机器」，Agent 会自动重新登记 |
| 证书指纹不一致 | 服务端重装过证书：删 Agent 状态目录 trust_pins 里的对应条目 |
| Agent 日志 "拒绝本地地址" | 映射到私网地址需要 Agent 加 `-allow-lan` |
| 管理台 401 | 会话过期（默认 12h），重新登录 |

## 8. 部署自检清单

| 检查项 | 期望 |
|--------|------|
| 服务状态 | `systemctl is-active queqiaod` 输出 `active` |
| 端口监听 | `ss -lntp` 能看到控制端口（7600/39999）与 `127.0.0.1:7610` |
| 管理台 | SSH 隧道打开 `http://127.0.0.1:7610` 能正常登录 |
| 设备 | 管理台「设备」页显示客户端 `online` |
| 映射 | 新建一条 TCP 映射后，公网 `curl http://YOUR_SERVER_IP:39001/` 返回本地服务响应 |
| 日志 | `journalctl -u queqiaod -f` 无异常 |

生产使用提示：

- `/var/lib/queqiao` 里包含设备/规则/审计和 TLS 私钥，注意备份与权限（0700）。
- 更换服务端证书会导致所有 Agent 报「证书指纹变化」，需要清理客户端 `trust_pins` 后重连。
- 历史版本曾用名 **NRP**，二进制/服务名/目录都已统一为 **Queqiao**；旧部署的备份目录（`/var/lib/nrp`、`~/.nrp.bak`）可在确认稳定后删除。

常用运维命令：

```bash
# 服务端状态 / 日志
ssh root@YOUR_SERVER_IP 'systemctl status queqiaod --no-pager; journalctl -u queqiaod -f'
# 管理台（本机执行，保持隧道）
ssh -L 7610:127.0.0.1:7610 root@YOUR_SERVER_IP
# 客户端状态 / 重启 / 日志
launchctl print gui/$(id -u)/io.queqiao.agent | head -20
launchctl kickstart -k gui/$(id -u)/io.queqiao.agent
tail -f ~/.queqiao/queqiao.log
# 卸载客户端服务
~/.queqiao/bin/queqiao uninstall-service
```
