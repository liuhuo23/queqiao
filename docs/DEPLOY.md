# 部署手册（服务端 39.107.230.175 + 本机 Agent）

## 0. 服务器现状（部署前必读）

- 系统：Alibaba Cloud Linux 3 / x86_64 / systemd 239 / root 可用
- 已占用端口：
  - `frps`：7000（bindPort）、7500（dashboard）、80（vhostHTTP）、443（vhostHTTPS）
  - `nps`：8024、2080、2443、43039
  - 宝塔面板：38888；nginx 1.20.1 已安装（当前 80 被 frps 占用）
- firewalld：已放行 `39000-40000/tcp`、22、80、443、7000 等
- 阿里云安全组已放行 `39000-40000/tcp`；**实测 7600/tcp 未放行**。因此实际部署把控制端口放在 `39999/tcp`（已放行段内），规则端口范围设为 `39000-39998`。

鹊桥 端口规划：

| 用途 | 端口 | 备注 |
|------|------|------|
| 控制端口 | 39999/tcp | 已放行段内，无需改安全组（设计默认 7600，需安全组放行） |
| 公网映射 | 39000-39998/tcp | 已放行；规则里的 `remote_port` 必须落在此区间 |
| 管理台 | 127.0.0.1:7610 | 默认只监听本机，用 SSH 隧道访问 |
| HTTP 域名映射 | 可配（默认关） | 80/443 被 frps 占用；切换期可先用 `:39080` |

> 若之后彻底替换 frp：停掉 `frps` 后，可把 `http_addr` 设为 `:80`，并把控制端口改回 7000。

## 1. 构建

```bash
# 在本机仓库根目录
make linux-server           # -> dist/queqiaod-linux-amd64
make agent                  # -> dist/queqiao（本机 macOS）
```

## 2. 安装服务端

```bash
scp dist/queqiaod-linux-amd64 root@39.107.230.175:/usr/local/bin/queqiaod
ssh root@39.107.230.175 '
  chmod +x /usr/local/bin/queqiaod
  mkdir -p /etc/queqiao /var/lib/queqiao
  chmod 700 /var/lib/queqiao
'
```

/etc/queqiao/queqiaod.json：

```json
{
  "control_addr": ":39999",
  "web_addr": "127.0.0.1:7610",
  "data_dir": "/var/lib/queqiao",
  "public_host": "39.107.230.175",
  "public_bind_addr": "0.0.0.0",
  "http_addr": "",
  "port_range": [39000, 39998],
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

防火墙：本次使用的 `39999/tcp` 与映射段 `39000-39998` 都已在 firewalld 放行（`firewall-cmd --list-ports` 可见 `39000-40000/tcp`），无需改动。
若改用 7600 作为控制端口，需 `firewall-cmd --permanent --add-port=7600/tcp && firewall-cmd --reload`，并在阿里云控制台安全组放行 7600/tcp。

启动后：

- TLS 自签证书自动生成在 `/var/lib/queqiao/tls/`（客户端首次连接会 TOFU 固定指纹）
- 初始管理员密码在 `/var/lib/queqiao/initial-admin.txt`（权限 0600，登录后请立即改密码；改密成功后该文件会被删除）

## 3. 访问管理台

无需开放 7610 到公网，SSH 隧道即可：

```bash
ssh -L 7610:127.0.0.1:7610 root@39.107.230.175
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
~/.queqiao/bin/queqiao -s 39.107.230.175:39999

# 在管理台「设备」页批准（密钥码应一致），Agent 会自动连上
# 安装为开机自启（launchd），之后无需再手动运行：
~/.queqiao/bin/queqiao install-service -s 39.107.230.175:39999
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
scp dist/queqiaod-linux-amd64 root@39.107.230.175:/usr/local/bin/queqiaod.new
ssh root@39.107.230.175 'systemctl stop queqiaod && mv /usr/local/bin/queqiaod /usr/local/bin/queqiaod.bak && mv /usr/local/bin/queqiaod.new /usr/local/bin/queqiaod && systemctl start queqiaod'

# 数据备份（设备/规则/审计/TLS 证书）
ssh root@39.107.230.175 'tar czf /root/queqiao-data-$(date +%F).tar.gz -C /var/lib queqiao'

# 回滚
ssh root@39.107.230.175 'systemctl stop queqiaod && mv /usr/local/bin/queqiaod.bak /usr/local/bin/queqiaod && systemctl start queqiaod'
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

## 8. 实际部署记录（2026-10-22，已更名「鹊桥 Queqiao」）

| 项 | 实际值 |
|----|--------|
| 服务端 | 39.107.230.175（Alibaba Cloud Linux 3，systemd） |
| 服务端二进制 | `/usr/local/bin/queqiaod`（linux/amd64，版本 0.1.0） |
| 服务端配置 | `/etc/queqiao/queqiaod.json`（control `:39999`，web `127.0.0.1:7610`，port_range 39000-39998） |
| 服务 | `queqiaod.service`：enabled + active |
| 服务端数据 | `/var/lib/queqiao`（state.json / audit.jsonl / tls/）；旧目录 `/var/lib/nrp` 保留作回滚 |
| 控制端口 | 39999/tcp（公网可直接连通） |
| 管理台 | `ssh -L 7610:127.0.0.1:7610 root@39.107.230.175` → http://127.0.0.1:7610 |
| 客户端 | macOS LaunchAgent `io.queqiao.agent`（用户级，开机自启；plist 在 `~/Library/LaunchAgents/`） |
| 客户端二进制 | `~/.queqiao/bin/queqiao` |
| 客户端状态 | `~/.queqiao/{agent.json,identity.json}` |
| 客户端日志 | `~/.queqiao/queqiao.log`（stderr：`~/.queqiao/queqiao.err.log`） |
| 设备 | `macbook-pro` / 密钥码 `QIAO-QCDD-XXIJ-QKVA` / active / online |
| 公网映射 | `local-demo`：`127.0.0.1:8080 → :39001`（当前生效，演示页） |

验证结果：`curl http://39.107.230.175:39001/` 返回 200（Internet → 服务器:39001 → TLS/yamux → 本机 8080），
`launchctl print gui/$(id -u)/io.queqiao.agent` 为 running，管理台显示设备 online。

### 更名迁移（NRP → 鹊桥 Queqiao，2026-10-22）

- 代码：Go module `nrp → queqiao`，二进制 `nrpd → queqiaod`、`nrp-agent → queqiao`，CSRF 头 `X-Queqiao-CSRF`。
- 服务端：新 unit `queqiaod.service` + 新目录 `/etc/queqiao`、`/var/lib/queqiao`；数据从 `/var/lib/nrp` 完整复制（含 `tls/` 证书、`state.json` 设备/规则、`audit.jsonl`）。
- 客户端：用户目录 `~/.nrp → ~/.queqiao`，**identity.json 原样保留**，设备无需重新审批；launchd `io.nrp.agent → io.queqiao.agent`，日志改到 `~/.queqiao/queqiao.log`。
- 保留项：`machineid` 哈希域仍是 `nrp-machine-v1`（改名不改机器指纹，避免设备重新绑定）。
- 回滚：旧二进制 `/usr/local/bin/nrpd`、旧配置 `/etc/nrp`、旧数据 `/var/lib/nrp`、客户端 `~/.nrp.bak` 都还在；`systemctl disable --now queqiaod && systemctl enable --now nrpd` 可临时回退。
- 注意：Cookie 名改为 `queqiao_admin`，服务端重启后旧会话失效，管理台需要重新登录。


常用运维命令：

```bash
# 服务端状态 / 日志
ssh root@39.107.230.175 'systemctl status queqiaod --no-pager; journalctl -u queqiaod -f'
# 管理台（本机执行，保持隧道）
ssh -L 7610:127.0.0.1:7610 root@39.107.230.175
# 客户端状态 / 重启 / 日志
launchctl print gui/$(id -u)/io.queqiao.agent | head -20
launchctl kickstart -k gui/$(id -u)/io.queqiao.agent
tail -f ~/.queqiao/queqiao.log
# 卸载客户端服务
~/.queqiao/bin/queqiao uninstall-service
```
