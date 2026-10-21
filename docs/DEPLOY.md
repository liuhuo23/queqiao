# 部署手册（服务端 39.107.230.175 + 本机 Agent）

## 0. 服务器现状（部署前必读）

- 系统：Alibaba Cloud Linux 3 / x86_64 / systemd 239 / root 可用
- 已占用端口：
  - `frps`：7000（bindPort）、7500（dashboard）、80（vhostHTTP）、443（vhostHTTPS）
  - `nps`：8024、2080、2443、43039
  - 宝塔面板：38888；nginx 1.20.1 已安装（当前 80 被 frps 占用）
- firewalld：已放行 `39000-40000/tcp`、22、80、443、7000 等
- **阿里云安全组需在控制台放行**：7600/tcp（控制端口）。39000-40000 大概率已放行（frp 在用）。

NRP 端口规划：

| 用途 | 端口 | 备注 |
|------|------|------|
| 控制端口 | 7600/tcp | TLS；需要 firewalld + 安全组 |
| 公网映射 | 39000-40000/tcp | 已放行；规则里的 `remote_port` 必须落在此区间 |
| 管理台 | 127.0.0.1:7610 | 默认只监听本机，用 SSH 隧道访问 |
| HTTP 域名映射 | 可配（默认关） | 80/443 被 frps 占用；切换期可先用 `:39080` |

> 若之后彻底替换 frp：停掉 `frps` 后，可把 `http_addr` 设为 `:80`，并把控制端口改回 7000。

## 1. 构建

```bash
# 在本机仓库根目录
make linux-server           # -> dist/nrpd-linux-amd64
make agent                  # -> dist/nrp-agent（本机 macOS）
```

## 2. 安装服务端

```bash
scp dist/nrpd-linux-amd64 root@39.107.230.175:/usr/local/bin/nrpd
ssh root@39.107.230.175 '
  chmod +x /usr/local/bin/nrpd
  mkdir -p /etc/nrp /var/lib/nrp
  chmod 700 /var/lib/nrp
'
```

/etc/nrp/nrpd.json：

```json
{
  "control_addr": ":7600",
  "web_addr": "127.0.0.1:7610",
  "data_dir": "/var/lib/nrp",
  "public_host": "39.107.230.175",
  "public_bind_addr": "0.0.0.0",
  "http_addr": "",
  "port_range": [39000, 40000],
  "log_level": "info",
  "log_format": "text"
}
```

> 配置里的 `public_host` / `port_range` / `http_addr` 只在**首次启动、数据目录为空**时写入设置；
> 之后以网页「设置」为准（唯一事实源）。

systemd（deploy/nrpd.service 可直接用）：

```bash
install -m 0644 deploy/nrpd.service /etc/systemd/system/nrpd.service
systemctl daemon-reload
systemctl enable --now nrpd
systemctl status nrpd --no-pager
journalctl -u nrpd -f
```

防火墙（阿里云安全组需人工在控制台放行 7600/tcp）：

```bash
firewall-cmd --permanent --add-port=7600/tcp
firewall-cmd --reload
firewall-cmd --list-ports
```

启动后：

- TLS 自签证书自动生成在 `/var/lib/nrp/tls/`（客户端首次连接会 TOFU 固定指纹）
- 初始管理员密码在 `/var/lib/nrp/initial-admin.txt`（权限 0600，登录后请立即改密码；改密成功后该文件会被删除）

## 3. 访问管理台

无需开放 7610 到公网，SSH 隧道即可：

```bash
ssh -L 7610:127.0.0.1:7610 root@39.107.230.175
# 打开 http://127.0.0.1:7610
```

如果要用域名 + HTTPS 暴露，见 deploy/nginx-nrp.conf（nginx 终止 TLS 后反代到 127.0.0.1:7610）。

首次登录后：
1. 「设置」里确认公网主机、端口段；
2. 修改管理员密码（至少 12 位）；
3. 需要 HTTP 域名映射时，把 `http_addr` 设为 `:39080`（或替换 frp 后用 `:80`）。

## 4. 安装客户端（本机 macOS）

```bash
make agent
sudo install -m 0755 dist/nrp-agent /usr/local/bin/nrp-agent

# 首次运行：登记设备（会打印密钥码并停在"待审批"）
nrp-agent -s 39.107.230.175:7600

# 在管理台「设备」页批准（密钥码应一致），Agent 会自动连上
# 安装为开机自启（launchd），之后无需再手动运行：
nrp-agent install-service
nrp-agent status
tail -f `~/Library/Logs/nrp-agent.log`
```

Linux 客户端：`nrp-agent -s host:7600` 后执行 `sudo nrp-agent install-service`（systemd）。
Windows：`nrp-agent.exe -s host:7600` 首次运行，然后用任务计划程序/服务管理器设置开机启动。

## 5. 添加第一条映射

管理台「映射」→ 新建：

| 字段 | 示例 | 说明 |
|------|------|------|
| 设备 | macbook-pro | 已批准的设备 |
| 协议 | tcp | http 用于域名映射 |
| 本地地址 | 127.0.0.1:8080 | Agent 所在主机上的服务 |
| 公网端口 | 39001 | 必须在端口段内且未被占用（可先点「检测端口」） |

保存后立即生效。验证：

```bash
curl -v http://39.107.230.175:39001/
# 或从本机：ssh -p 39022 root@39.107.230.175（若映射了 127.0.0.1:22 -> 39022）
```

## 6. 升级与回滚

```bash
# 升级
scp dist/nrpd-linux-amd64 root@39.107.230.175:/usr/local/bin/nrpd.new
ssh root@39.107.230.175 'systemctl stop nrpd && mv /usr/local/bin/nrpd /usr/local/bin/nrpd.bak && mv /usr/local/bin/nrpd.new /usr/local/bin/nrpd && systemctl start nrpd'

# 数据备份（设备/规则/审计/TLS 证书）
ssh root@39.107.230.175 'tar czf /root/nrp-data-$(date +%F).tar.gz -C /var/lib nrp'

# 回滚
ssh root@39.107.230.175 'systemctl stop nrpd && mv /usr/local/bin/nrpd.bak /usr/local/bin/nrpd && systemctl start nrpd'
```

> 重装服务端会重新生成 TLS 证书，已连接过的 Agent 会报「证书指纹变化」。
> 删掉 Agent 状态目录里的 `trust_pins`（或 `nrp-agent status` 查看路径）后重连即可。

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
